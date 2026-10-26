package kv

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// testNow pins the clock so TTL puts give the same ExpiresAt in every store.
var testNow = time.Now()

func pin(stores ...*Store) {
	for _, s := range stores {
		s.now = func() time.Time { return testNow }
	}
}

// history applies a deterministic mix of puts, overwrites, deletes and TTL
// puts, numbered from start so tests can resume it.
func history(t *testing.T, s *Store, start, n int) {
	t.Helper()
	pin(s)
	for i := start; i < start+n; i++ {
		key := fmt.Sprintf("k%d", i%7)
		var err error
		switch {
		case i%5 == 4:
			err = s.Delete(key, nil)
			if err == ErrNotFound {
				err = nil
			}
		case i%3 == 0:
			_, _, err = s.PutTTL(key, fmt.Sprint(i), nil, time.Hour)
		default:
			_, _, err = s.Put(key, fmt.Sprint(i), nil)
		}
		if err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// state is everything a reopen must restore.
func state(s *Store) (map[string]Entry, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.liveEntries(), s.revision
}

func sameState(t *testing.T, got, want *Store) {
	t.Helper()
	gd, gr := state(got)
	wd, wr := state(want)
	if gr != wr {
		t.Errorf("revision = %d, want %d", gr, wr)
	}
	if len(gd) != len(wd) {
		t.Errorf("%d keys, want %d", len(gd), len(wd))
	}
	for k, e := range wd {
		if gd[k] != e {
			t.Errorf("key %q = %+v, want %+v", k, gd[k], e)
		}
	}
}

func TestSnapshot_TailReplayMatchesFullHistory(t *testing.T) {
	for _, mode := range []FsyncMode{FsyncOff, FsyncAlways, FsyncBatch} {
		t.Run(string(mode), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "kv.wal")
			ref := NewStore() // the same history with no WAL at all
			s, err := Open(path, mode, 10)
			if err != nil {
				t.Fatal(err)
			}
			history(t, s, 0, 95)
			history(t, ref, 0, 95)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			names := dirNames(t, dir)
			if !slices.Contains(names, "kv.snap") || slices.Contains(names, "kv.wal") {
				t.Errorf("files = %v, want kv.snap and no first segment", names)
			}
			if segs, _ := listSegments(path); len(segs) != 1 {
				t.Errorf("segments = %v, want only the newest segment", segs)
			}

			s, err = Open(path, mode, 10)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			sameState(t, s, ref)
			_, rev := state(ref)
			if v, _, _ := s.Put("next", "x", nil); v != rev+1 {
				t.Errorf("first version after reload = %d, want %d", v, rev+1)
			}
		})
	}
}

// switchByHand runs maybeSnapshot's locked section and waits for the switch.
func switchByHand(t *testing.T, s *Store) snapshot {
	t.Helper()
	s.mu.Lock()
	snap := snapshot{Revision: s.revision, Entries: s.liveEntries()}
	switched := s.switchTo(segmentPath(s.path, s.revision))
	s.mu.Unlock()
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	return snap
}

// TestSnapshot_CrashBetweenSteps rebuilds the data dir as a crash would
// leave it partway through a second snapshot (a first one already
// completed), then checks a reopen.
func TestSnapshot_CrashBetweenSteps(t *testing.T) {
	const (
		switched = iota // kv.wal.<R2> created, no new snapshot
		tmpWritten
		renamed
		deleted
	)
	cases := []struct {
		name  string
		tail  int // ops after the second switch
		stage int
	}{
		{"switched", 13, switched},
		{"switched, empty new segment", 0, switched},
		{"tmp written", 13, tmpWritten},
		{"renamed", 13, renamed},
		{"renamed, empty new segment", 0, renamed},
		{"old segment deleted", 13, deleted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "kv.wal")
			ref := NewStore()
			s, err := Open(path, FsyncBatch, 0)
			if err != nil {
				t.Fatal(err)
			}
			snapPath := snapshotPath(path)

			// A first snapshot, completed: kv.snap@R1 and kv.wal.<R1>.
			history(t, s, 0, 10)
			first := switchByHand(t, s)
			if err := writeFileSynced(snapPath, first); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}

			// The second one, interrupted at tc.stage.
			history(t, s, 10, 15)
			second := switchByHand(t, s)
			history(t, s, 25, tc.tail)
			history(t, ref, 0, 25+tc.tail)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if tc.stage >= tmpWritten {
				if err := writeFileSynced(snapPath+".next", second); err != nil {
					t.Fatal(err)
				}
				target := snapPath + ".tmp" // crash before the rename
				if tc.stage >= renamed {
					target = snapPath
				}
				if err := os.Rename(snapPath+".next", target); err != nil {
					t.Fatal(err)
				}
			}
			if tc.stage >= deleted {
				if err := os.Remove(segmentPath(path, first.Revision)); err != nil {
					t.Fatal(err)
				}
			}

			s, err = Open(path, FsyncBatch, 0)
			if err != nil {
				t.Fatal(err)
			}
			sameState(t, s, ref)
			if slices.Contains(dirNames(t, dir), "kv.snap.tmp") {
				t.Error("leftover kv.snap.tmp was not removed")
			}
			// The revision continues, and the write lands where a reopen finds it.
			_, rev := state(ref)
			if v, _, err := s.Put("after", "x", nil); err != nil || v != rev+1 {
				t.Errorf("Put after reopen = %d, %v; want %d", v, err, rev+1)
			}
			ref.Put("after", "x", nil)
			s.Close()
			s, err = Open(path, FsyncBatch, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			sameState(t, s, ref)
		})
	}
}

// lockedBuffer is an io.Writer safe for the snapshot goroutine's logging.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func puts(t *testing.T, s *Store, start, n int) {
	t.Helper()
	pin(s)
	for i := start; i < start+n; i++ {
		if _, _, err := s.Put(fmt.Sprintf("p%d", i), fmt.Sprint(i), nil); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
}

func TestSnapshot_FailureDoesNotPoisonAndRetries(t *testing.T) {
	var logs lockedBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	dir := t.TempDir()
	path := filepath.Join(dir, "kv.wal")
	s, err := Open(path, FsyncBatch, 10)
	if err != nil {
		t.Fatal(err)
	}
	// A non-empty directory where the tmp file goes makes os.Create fail.
	block := filepath.Join(dir, "kv.snap.tmp", "x")
	if err := os.MkdirAll(block, 0o755); err != nil {
		t.Fatal(err)
	}
	puts(t, s, 0, 10) // the 10th write crosses the threshold; the snapshot fails
	s.snapWG.Wait()

	names := dirNames(t, dir)
	if !slices.Contains(names, "kv.wal.10") || slices.Contains(names, "kv.snap") || !slices.Contains(names, "kv.wal") {
		t.Fatalf("files = %v, want kv.wal and kv.wal.10 kept and no kv.snap", names)
	}
	failed := ""
	for line := range strings.Lines(logs.String()) {
		if strings.Contains(line, "snapshot failed") {
			failed = line
		}
	}
	if failed == "" {
		t.Fatalf("no snapshot failure logged; logs:\n%s", logs.String())
	}
	t.Logf("logged: %s", strings.TrimSpace(failed))
	if !s.Writable() {
		t.Fatal("a failed snapshot poisoned the store")
	}

	if err := os.RemoveAll(filepath.Dir(block)); err != nil {
		t.Fatal(err)
	}
	puts(t, s, 10, 10) // the next threshold (revision 20) retries
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	names = dirNames(t, dir)
	if !slices.Equal(names, []string{"kv.snap", "kv.wal.20"}) {
		t.Errorf("files after retry = %v, want [kv.snap kv.wal.20]", names)
	}

	ref := NewStore()
	puts(t, ref, 0, 20)
	s, err = Open(path, FsyncBatch, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sameState(t, s, ref)
}

func TestSnapshot_CorruptSnapshotRefusesToOpen(t *testing.T) {
	for name, content := range map[string]string{
		"truncated": `{"revision":5,"entries":{"k0":{"value":"x","ver`,
		"not json":  "garbage\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "kv.wal")
			if err := os.WriteFile(snapshotPath(path), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := Open(path, FsyncBatch, 0)
			if err == nil {
				s.Close()
				t.Fatal("Open succeeded with a corrupt kv.snap")
			}
			if !strings.Contains(err.Error(), "corrupt snapshot") {
				t.Errorf("err = %v, want it to say corrupt snapshot", err)
			}
		})
	}
}

func TestSnapshot_SkipsExpiredAndKeepsDeadline(t *testing.T) {
	s, clk := newClockedStore()
	s.PutTTL("short", "x", nil, time.Minute)
	s.PutTTL("long", "y", nil, time.Hour)
	s.Put("forever", "z", nil)
	clk.Advance(2 * time.Minute)

	got := s.liveEntries()
	if _, ok := got["short"]; ok {
		t.Error("snapshot kept an expired key")
	}
	if got["long"].ExpiresAt != s.data["long"].ExpiresAt || got["long"].ExpiresAt == 0 {
		t.Errorf("long ExpiresAt = %d, want %d", got["long"].ExpiresAt, s.data["long"].ExpiresAt)
	}
	if _, ok := got["forever"]; !ok {
		t.Error("snapshot dropped a permanent key")
	}
}

func TestSnapshot_SegmentOrderIsNumeric(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kv.wal")
	for _, name := range []string{"kv.wal.100", "kv.wal", "kv.wal.9", "kv.snap", "kv.snap.tmp", "kv.wal.x"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	segs, err := listSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	var bases []uint64
	for _, seg := range segs {
		bases = append(bases, seg.base)
	}
	if !slices.Equal(bases, []uint64{0, 9, 100}) {
		t.Errorf("segment bases = %v, want [0 9 100]", bases)
	}
}

// BenchmarkSnapshotCopy100k measures liveEntries, the part of a snapshot
// that holds s.mu and so pauses every writer.
func BenchmarkSnapshotCopy100k(b *testing.B) {
	s := NewStore()
	for i := range 100_000 {
		s.data[fmt.Sprintf("key-%06d", i)] = Entry{Value: "value-of-moderate-length", Version: uint64(i + 1)}
	}
	for b.Loop() {
		s.mu.Lock()
		s.liveEntries()
		s.mu.Unlock()
	}
}
