package kv

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("key not found")
	ErrConflict = errors.New("version conflict")
)

type Entry struct {
	Value     string
	Version   uint64
	ExpiresAt int64 // unix nanoseconds; 0 means the key never expires
}

// expired reports whether e is past its expiry at now (unix nanoseconds).
func (e Entry) expired(now int64) bool {
	return e.ExpiresAt != 0 && now >= e.ExpiresAt
}

type Item struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Version uint64 `json:"version"`
}

const (
	opPut    = "put"
	opDelete = "del"
)

// walRecord is one line of the write-ahead log.
type walRecord struct {
	Op      string `json:"op"`
	Key     string `json:"key"`
	Value   string `json:"value,omitempty"`
	Version uint64 `json:"version"`
	// Absolute, not a duration, so a restart never extends a key's life.
	// Absent in older WALs, which therefore replay as permanent keys.
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

// maxSweep caps how many keys one sweeper pass deletes per hold of s.mu,
// so a mass expiry cannot stall writers for long.
const maxSweep = 1000

// MaxTTL is the longest TTL accepted. Beyond ~year 2262 UnixNano overflows,
// so a cap well short of that keeps expires_at meaningful.
const MaxTTL = 100 * 365 * 24 * time.Hour

// FsyncMode says when a write is acknowledged relative to fsync (WAL_FSYNC).
type FsyncMode string

const (
	FsyncOff    FsyncMode = "off"    // write to the OS, never fsync: survives a process crash only
	FsyncAlways FsyncMode = "always" // one fsync per write
	FsyncBatch  FsyncMode = "batch"  // group commit: concurrent writes share one fsync
)

var errClosed = errors.New("store closed")

// commit is a record waiting for the flusher, and where to send its result.
type commit struct {
	rec  walRecord
	done chan error
}

type Store struct {
	mu       sync.RWMutex
	data     map[string]Entry
	revision uint64
	now      func() time.Time // time.Now; same-package tests inject a fake clock

	stopSweep func() // set by StartSweeper; cancels the sweeper and waits for it

	wal     *os.File
	bw      *bufio.Writer // batch/always only: the flusher's buffer in front of wal
	enc     *json.Encoder
	syncWAL func() error // wal.Sync; a field so tests can inject a failure

	// Group commit (batch/always). wake is nil in off mode.
	wake    chan struct{} // cap 1: "the queue is non-empty"
	flushed chan struct{} // closed when the flusher exits

	qmu    sync.Mutex // guards queue and walErr; taken after s.mu, never before
	queue  []commit
	walErr error
}

// NewStore returns a memory-only store (nothing survives a restart).
func NewStore() *Store {
	return &Store{data: make(map[string]Entry), now: time.Now}
}

// Open returns a store backed by the WAL at path, replaying any existing
// records first. The file is created if it does not exist.
func Open(path string, mode FsyncMode) (*Store, error) {
	switch mode {
	case FsyncOff, FsyncAlways, FsyncBatch:
	default:
		return nil, fmt.Errorf("unknown WAL fsync mode %q (want off, always or batch)", mode)
	}
	s := NewStore()
	if err := s.replayFile(path); err != nil {
		return nil, err
	}
	// Reopened append-only for writing: on Windows an O_APPEND handle cannot
	// truncate, so replay/repair uses its own read-write handle above.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, fmt.Errorf("open WAL for append: %w", err)
	}
	s.wal = f
	s.syncWAL = f.Sync
	if mode == FsyncOff {
		s.enc = json.NewEncoder(f)
		return s, nil
	}
	s.bw = bufio.NewWriter(f)
	s.enc = json.NewEncoder(s.bw)
	s.wake = make(chan struct{}, 1)
	s.flushed = make(chan struct{})
	go s.flusher(mode == FsyncAlways)
	return s, nil
}

func (s *Store) replayFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open WAL: %w", err)
	}
	defer f.Close()
	return s.replay(f)
}

// replay applies every complete record in f, then truncates a torn final
// line (a crash mid-append) so the next append starts on a clean line.
// A corrupt line that is followed by more data is real damage, not a torn
// tail, so it is an error rather than silently skipped.
func (s *Store) replay(f *os.File) error {
	r := bufio.NewReader(f)
	var good int64
	for n := 1; ; n++ {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				slog.Warn("discarding torn WAL tail", "line", n, "bytes", len(line))
			}
			break
		}
		if err != nil {
			return fmt.Errorf("read WAL: %w", err)
		}
		var rec walRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("corrupt WAL record on line %d: %w", n, err)
		}
		if rec.Op != opPut && rec.Op != opDelete {
			return fmt.Errorf("corrupt WAL record on line %d: unknown op %q", n, rec.Op)
		}
		s.apply(rec)
		good += int64(len(line))
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat WAL: %w", err)
	}
	if info.Size() > good {
		if err := f.Truncate(good); err != nil {
			return fmt.Errorf("truncate torn WAL tail: %w", err)
		}
	}
	slog.Info("WAL replayed", "keys", len(s.data), "revision", s.revision)
	return nil
}

// Close waits for every queued write to be committed, fsyncs and closes the
// WAL. Writes after Close fail.
func (s *Store) Close() error {
	// Before taking s.mu: the sweeper needs s.mu to finish its pass.
	if s.stopSweep != nil {
		s.stopSweep()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wal == nil {
		return nil
	}
	// Holding s.mu means nothing new can be queued, so the flusher drains
	// what is there and exits.
	if s.wake != nil {
		close(s.wake)
		<-s.flushed
	}
	prev := s.err()     // a poisoning error, surfaced so shutdown logs show it
	s.poison(errClosed) // keeps an earlier error; either way walErr is now set
	err := errors.Join(prev, s.wal.Sync(), s.wal.Close())
	s.wal = nil
	return err
}

// Writable reports whether the store still accepts writes: false once a
// WAL append or fsync has failed, and after Close.
func (s *Store) Writable() bool {
	return s.err() == nil
}

func (s *Store) Get(key string) (value string, version uint64, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.lookup(key)
	return e.Value, e.Version, ok
}

// lookup returns key's entry, treating an expired key as missing whether or
// not the sweeper has removed it yet (lazy expiry). Callers must hold s.mu.
func (s *Store) lookup(key string) (Entry, bool) {
	e, ok := s.data[key]
	if !ok || e.expired(s.now().UnixNano()) {
		return Entry{}, false
	}
	return e, true
}

// Put writes value under key with no expiry. If ifMatch is non-nil, the
// write only happens when the key's current version equals *ifMatch; a
// missing (or expired) key counts as version 0, so *ifMatch == 0 means
// "create only". On conflict it returns the current version and ErrConflict.
func (s *Store) Put(key, value string, ifMatch *uint64) (version uint64, created bool, err error) {
	return s.PutTTL(key, value, ifMatch, 0)
}

// PutTTL is Put with a TTL: the key expires ttl from now. ttl <= 0 makes
// the key permanent; ttl > MaxTTL is an error. Every write resets the expiry.
func (s *Store) PutTTL(key, value string, ifMatch *uint64, ttl time.Duration) (version uint64, created bool, err error) {
	if ttl > MaxTTL {
		return 0, false, fmt.Errorf("ttl %v exceeds the maximum %v", ttl, MaxTTL)
	}
	s.mu.Lock()
	cur, existed := s.lookup(key)
	if ifMatch != nil && *ifMatch != cur.Version {
		s.mu.Unlock()
		return cur.Version, false, ErrConflict
	}
	rec := walRecord{Op: opPut, Key: key, Value: value, Version: s.revision + 1}
	if ttl > 0 {
		now := s.now()
		rec.ExpiresAt = now.Add(ttl).UnixNano()
		if rec.ExpiresAt <= now.UnixNano() { // UnixNano wrapped: possible only with a clock past ~2162
			s.mu.Unlock()
			return 0, false, fmt.Errorf("ttl %v overflows the expiry time", ttl)
		}
	}
	done, err := s.write(rec)
	s.mu.Unlock()
	if err == nil && done != nil {
		err = <-done // wait for the fsync without blocking readers or other writers
	}
	if err != nil {
		return 0, false, err
	}
	return rec.Version, !existed, nil
}

// Delete removes key, with the same ifMatch semantics as Put.
func (s *Store) Delete(key string, ifMatch *uint64) error {
	s.mu.Lock()
	cur, existed := s.lookup(key)
	if ifMatch != nil && *ifMatch != cur.Version {
		s.mu.Unlock()
		return ErrConflict
	}
	if !existed {
		s.mu.Unlock()
		return ErrNotFound
	}
	rec := walRecord{Op: opDelete, Key: key, Version: s.revision + 1}
	done, err := s.write(rec)
	s.mu.Unlock()
	if err == nil && done != nil {
		err = <-done
	}
	return err
}

// write logs and applies rec. Callers must hold s.mu, so log order matches
// revision order. In off mode the record goes straight to the file, then is
// applied. Otherwise it is applied now and queued for the flusher, and the
// caller waits on the returned channel after releasing s.mu: readers may see
// the write before it is durable, but it is not acknowledged until it is
// (ADR-0001).
func (s *Store) write(rec walRecord) (<-chan error, error) {
	if err := s.err(); err != nil {
		return nil, err
	}
	if s.wake == nil {
		// off mode encodes under s.mu: legacy semantics, kept so off is no slower.
		if s.wal != nil {
			if err := s.enc.Encode(rec); err != nil {
				return nil, s.poison(fmt.Errorf("append to WAL: %w", err))
			}
		}
		s.apply(rec)
		return nil, nil
	}
	s.apply(rec)
	done := make(chan error, 1)
	s.qmu.Lock()
	s.queue = append(s.queue, commit{rec: rec, done: done})
	s.qmu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
	return done, nil
}

// flusher is the only goroutine that writes the WAL in batch/always mode.
// It sleeps until a record is queued, then commits everything queued (one
// record per fsync when one is true). There is no timer: a batch is
// whatever piled up while the previous fsync ran.
func (s *Store) flusher(one bool) {
	defer close(s.flushed)
	for range s.wake {
		for s.flushBatch(one) {
		}
	}
}

// flushBatch commits the records at the head of the queue and wakes their
// writers. It reports false if the queue was empty.
func (s *Store) flushBatch(one bool) bool {
	s.qmu.Lock()
	n := len(s.queue)
	if n == 0 {
		s.qmu.Unlock()
		return false
	}
	if one {
		n = 1
	}
	batch := s.queue[:n:n]
	s.queue = s.queue[n:]
	err := s.walErr
	s.qmu.Unlock()

	if err == nil {
		if err = s.commitBatch(batch); err != nil {
			s.poison(err)
		}
	}
	for _, c := range batch {
		c.done <- err
	}
	return true
}

func (s *Store) commitBatch(batch []commit) error {
	for _, c := range batch {
		if err := s.enc.Encode(c.rec); err != nil {
			return fmt.Errorf("append to WAL: %w", err)
		}
	}
	if err := s.bw.Flush(); err != nil {
		return fmt.Errorf("append to WAL: %w", err)
	}
	if err := s.syncWAL(); err != nil {
		return fmt.Errorf("fsync WAL: %w", err)
	}
	return nil
}

// poison makes every later write fail (the first error wins). After a
// failed append the file may end in a partial line, and after a failed
// fsync we no longer know what is on disk, so carrying on is unsafe.
func (s *Store) poison(err error) error {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if s.walErr == nil {
		s.walErr = err
	}
	return s.walErr
}

func (s *Store) err() error {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	return s.walErr
}

// apply mutates memory from a record. It is the only code that changes
// s.data, used by both live writes and replay. Callers must hold s.mu.
func (s *Store) apply(rec walRecord) {
	switch rec.Op {
	case opPut:
		e := Entry{Value: rec.Value, Version: rec.Version, ExpiresAt: rec.ExpiresAt}
		if e.expired(s.now().UnixNano()) {
			delete(s.data, rec.Key) // replay: already expired, so the key is gone; revision still advances
		} else {
			s.data[rec.Key] = e
		}
	case opDelete:
		delete(s.data, rec.Key)
	}
	s.revision = rec.Version
}

func (s *Store) List(prefix string) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []Item{}
	now := s.now().UnixNano()
	for key, e := range s.data {
		if strings.HasPrefix(key, prefix) && !e.expired(now) {
			items = append(items, Item{Key: key, Value: e.Value, Version: e.Version})
		}
	}
	slices.SortFunc(items, func(a, b Item) int { return cmp.Compare(a.Key, b.Key) })
	return items
}

// StartSweeper starts a goroutine that removes expired keys from memory
// every interval (active expiry). Call it at most once, before the store is
// shared; Close stops it.
func (s *Store) StartSweeper(interval time.Duration) {
	if s.stopSweep != nil {
		panic("kv: StartSweeper called twice")
	}
	t := time.NewTicker(interval)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer t.Stop()
		s.sweepLoop(ctx, t.C)
	}()
	s.stopSweep = sync.OnceFunc(func() { cancel(); <-done })
}

// sweepLoop sweeps on every tick until ctx is cancelled. Ticks are a
// parameter so tests can drive it without a real ticker.
func (s *Store) sweepLoop(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			for ctx.Err() == nil && s.sweepChunk() == maxSweep {
			}
		}
	}
}

// sweepChunk deletes up to maxSweep expired keys under one hold of the
// write lock and reports how many it deleted. Expiry writes no WAL record
// and does not bump the revision: readers already treat the key as missing,
// and replay drops expired records by itself.
// shortcut: scans the map from the start on every chunk (O(keys)); sample
// keys like Redis or keep an expiry heap if the store grows to millions.
func (s *Store) sweepChunk() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UnixNano()
	n := 0
	for key, e := range s.data {
		if n == maxSweep {
			break
		}
		if e.expired(now) {
			delete(s.data, key)
			n++
		}
	}
	return n
}
