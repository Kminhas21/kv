package kv

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// On-disk layout, all derived from WAL_PATH (say /data/kv.wal):
//
//	kv.wal       first segment (and the whole WAL of a store that never snapshotted)
//	kv.wal.<R>   segment started when the snapshot at revision R was taken;
//	             it holds only records with revision > R
//	kv.snap      the snapshot; kv.snap.tmp while it is being written
//
// A segment's base revision is 0 for kv.wal and R for kv.wal.<R>, so
// "segments older than the snapshot at R" means exactly "base < R".

// snapshot is the store at one revision. The revision is what lets replay
// know which WAL records the snapshot already contains.
type snapshot struct {
	Revision uint64           `json:"revision"`
	Entries  map[string]Entry `json:"entries"`
}

type segment struct {
	base uint64
	path string
}

func segmentPath(walPath string, base uint64) string {
	return walPath + "." + strconv.FormatUint(base, 10)
}

func snapshotPath(walPath string) string {
	return strings.TrimSuffix(walPath, filepath.Ext(walPath)) + ".snap"
}

// listSegments returns the WAL segments next to walPath, oldest first.
func listSegments(walPath string) ([]segment, error) {
	dir, name := filepath.Dir(walPath), filepath.Base(walPath)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list WAL segments: %w", err)
	}
	var segs []segment
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		var base uint64
		if e.Name() != name {
			rest, ok := strings.CutPrefix(e.Name(), name+".")
			if !ok {
				continue
			}
			if base, err = strconv.ParseUint(rest, 10, 64); err != nil {
				continue // not a segment
			}
		}
		segs = append(segs, segment{base: base, path: filepath.Join(dir, e.Name())})
	}
	slices.SortFunc(segs, func(a, b segment) int { return cmp.Compare(a.base, b.base) })
	return segs, nil
}

// load restores the store from the snapshot (if any) and every segment, and
// returns the segment new records should be appended to.
func (s *Store) load() (current string, err error) {
	snapPath := snapshotPath(s.path)
	// A leftover tmp never made it to the rename, so it may be incomplete.
	if err := os.Remove(snapPath + ".tmp"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("remove leftover snapshot tmp: %w", err)
	}
	if err := s.loadSnapshot(snapPath); err != nil {
		return "", err
	}
	segs, err := listSegments(s.path)
	if err != nil {
		return "", err
	}
	if len(segs) == 0 {
		segs = []segment{{path: s.path}} // fresh data dir: replayFile creates it
	}
	// Segments the snapshot covers may still exist if we crashed before
	// deleting them; skipping records <= the snapshot revision makes that safe.
	after := s.revision
	for _, seg := range segs {
		if err := s.replayFile(seg.path, after); err != nil {
			return "", err
		}
	}
	return segs[len(segs)-1].path, nil
}

func (s *Store) loadSnapshot(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // no snapshot yet: replay the WAL from the start
	}
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()
	var snap snapshot
	if err := json.NewDecoder(bufio.NewReader(f)).Decode(&snap); err != nil {
		// Only a fully written, fsynced file is ever renamed to kv.snap, and
		// the segments it replaced may be gone, so refuse rather than guess.
		return fmt.Errorf("corrupt snapshot %s: %w", path, err)
	}
	now := s.now().UnixNano()
	for k, e := range snap.Entries {
		if !e.expired(now) {
			s.data[k] = e
		}
	}
	s.revision = snap.Revision
	slog.Info("snapshot loaded", "keys", len(s.data), "revision", s.revision)
	return nil
}

// maybeSnapshot counts a write and, every snapEvery writes, starts a
// snapshot. Callers must hold s.mu: copying the map and switching segment
// in one critical section is what makes "every record above the snapshot
// revision is in the new segment" true.
func (s *Store) maybeSnapshot() {
	if s.snapEvery == 0 || s.path == "" {
		return
	}
	s.sinceSnap++
	if s.sinceSnap < s.snapEvery || s.snapping.Load() {
		return // a snapshot still in flight: try again on the next write
	}
	s.sinceSnap = 0 // also on failure: the retry comes at the next threshold
	s.snapping.Store(true)
	start := time.Now()
	snap := snapshot{Revision: s.revision, Entries: s.liveEntries()}
	switched := s.switchTo(segmentPath(s.path, s.revision))
	slog.Info("snapshot started", "revision", snap.Revision, "keys", len(snap.Entries), "write_pause", time.Since(start))
	s.snapWG.Add(1)
	go s.writeSnapshot(snap, switched)
}

// liveEntries copies the map without expired keys. Callers must hold s.mu.
// shortcut: copies every entry while writers wait (~tens of ms per 100k
// keys); production stores use copy-on-write (fork, CoW B-trees) instead.
func (s *Store) liveEntries() map[string]Entry {
	now := s.now().UnixNano()
	m := make(map[string]Entry, len(s.data))
	for k, e := range s.data {
		if !e.expired(now) {
			m[k] = e // Entry is a value with no pointers: this is a deep copy
		}
	}
	return m
}

// switchTo makes path the segment for every record written after this
// call. Callers must hold s.mu, so no record is in between. In batch/always
// mode the switch goes through the queue: the flusher commits (and fsyncs)
// every record queued before it to the old segment, then switches.
func (s *Store) switchTo(path string) <-chan error {
	done := make(chan error, 1)
	if s.wake == nil {
		done <- s.switchSegment(path, false)
		return done
	}
	s.enqueue(commit{segment: path, done: done})
	return done
}

// switchSegment opens path and makes it the WAL. On error the old segment
// stays current and nothing is lost: the snapshot is abandoned instead.
// In batch/always mode only the flusher calls it, and the old segment is
// already fsynced, because the flusher commits a switch alone.
func (s *Store) switchSegment(path string, durable bool) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("create WAL segment: %w", err)
	}
	if durable {
		// fsync the new name too, or a machine crash could lose the file and
		// with it every acknowledged write in it.
		if err := syncDir(filepath.Dir(path)); err != nil {
			f.Close()
			os.Remove(path)
			return err
		}
	}
	if err := s.wal.Close(); err != nil {
		slog.Warn("close old WAL segment", "err", err) // its records are already written
	}
	s.wal, s.syncWAL = f, f.Sync
	if s.bw != nil {
		s.bw.Reset(f) // s.enc writes into s.bw
	} else {
		s.enc = json.NewEncoder(f)
	}
	slog.Info("WAL segment switched", "segment", path)
	return nil
}

// writeSnapshot runs in the background, off s.mu. A failure is logged and
// does not poison the store: the WAL is still the source of truth.
func (s *Store) writeSnapshot(snap snapshot, switched <-chan error) {
	defer s.snapWG.Done()
	defer s.snapping.Store(false)
	if err := s.persistSnapshot(snap, switched); err != nil {
		slog.Error("snapshot failed; retrying at the next threshold", "revision", snap.Revision, "err", err)
	}
}

func (s *Store) persistSnapshot(snap snapshot, switched <-chan error) error {
	// Wait for the switch first. It also means every record <= snap.Revision
	// is fsynced (they were queued before the switch), so the snapshot never
	// holds a write that could still fail.
	if err := <-switched; err != nil {
		return err
	}
	start := time.Now()
	path := snapshotPath(s.path)
	if err := writeFileSynced(path, snap); err != nil {
		return err
	}
	segs, err := listSegments(s.path)
	if err != nil {
		return err
	}
	var errs []error
	for _, seg := range segs {
		if seg.base < snap.Revision {
			errs = append(errs, os.Remove(seg.path))
		}
	}
	slog.Info("snapshot written", "revision", snap.Revision, "keys", len(snap.Entries), "took", time.Since(start))
	return errors.Join(errs...)
}

// writeFileSynced replaces path atomically: write a tmp file, fsync it,
// rename it over path, fsync the directory so the rename itself survives.
func writeFileSynced(path string, v any) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	bw := bufio.NewWriter(f)
	err = errors.Join(json.NewEncoder(bw).Encode(v), bw.Flush(), f.Sync(), f.Close())
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}
	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs a directory so a create or rename in it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	// Windows cannot fsync a directory handle (NTFS journals the metadata
	// itself), so it is best-effort there. Linux, where kv runs in k8s, needs it.
	if err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("fsync dir %s: %w", dir, err)
	}
	return nil
}
