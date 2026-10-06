package kv

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
)

var (
	ErrNotFound = errors.New("key not found")
	ErrConflict = errors.New("version conflict")
)

type Entry struct {
	Value   string
	Version uint64
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
}

type Store struct {
	mu       sync.RWMutex
	data     map[string]Entry
	revision uint64

	wal    *os.File
	enc    *json.Encoder
	walErr error
}

// NewStore returns a memory-only store (nothing survives a restart).
func NewStore() *Store {
	return &Store{data: make(map[string]Entry)}
}

// Open returns a store backed by the WAL at path, replaying any existing
// records first. The file is created if it does not exist.
func Open(path string) (*Store, error) {
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
	s.enc = json.NewEncoder(f)
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

// Close closes the WAL file. The store must not be used afterwards.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wal == nil {
		return nil
	}
	return s.wal.Close()
}

func (s *Store) Get(key string) (value string, version uint64, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	return e.Value, e.Version, ok
}

// Put writes value under key. If ifMatch is non-nil, the write only happens
// when the key's current version equals *ifMatch; a missing key counts as
// version 0, so *ifMatch == 0 means "create only". On conflict it returns
// the current version and ErrConflict.
func (s *Store) Put(key, value string, ifMatch *uint64) (version uint64, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, existed := s.data[key]
	if ifMatch != nil && *ifMatch != cur.Version {
		return cur.Version, false, ErrConflict
	}
	rec := walRecord{Op: opPut, Key: key, Value: value, Version: s.revision + 1}
	if err := s.log(rec); err != nil {
		return 0, false, err
	}
	s.apply(rec)
	return rec.Version, !existed, nil
}

// Delete removes key, with the same ifMatch semantics as Put.
func (s *Store) Delete(key string, ifMatch *uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, existed := s.data[key]
	if ifMatch != nil && *ifMatch != cur.Version {
		return ErrConflict
	}
	if !existed {
		return ErrNotFound
	}
	rec := walRecord{Op: opDelete, Key: key, Version: s.revision + 1}
	if err := s.log(rec); err != nil {
		return err
	}
	s.apply(rec)
	return nil
}

// log appends rec to the WAL. Callers must hold s.mu so log order matches
// revision order. After any write failure the store refuses further writes:
// a partial line mid-file would make every later record unreadable.
func (s *Store) log(rec walRecord) error {
	if s.wal == nil {
		return nil
	}
	if s.walErr != nil {
		return s.walErr
	}
	if err := s.enc.Encode(rec); err != nil {
		s.walErr = fmt.Errorf("append to WAL: %w", err)
		return s.walErr
	}
	return nil
}

// apply mutates memory from a record. It is the only code that changes
// s.data, used by both live writes and replay. Callers must hold s.mu.
func (s *Store) apply(rec walRecord) {
	switch rec.Op {
	case opPut:
		s.data[rec.Key] = Entry{Value: rec.Value, Version: rec.Version}
	case opDelete:
		delete(s.data, rec.Key)
	}
	s.revision = rec.Version
}

func (s *Store) List(prefix string) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []Item{}
	for key, e := range s.data {
		if strings.HasPrefix(key, prefix) {
			items = append(items, Item{Key: key, Value: e.Value, Version: e.Version})
		}
	}
	slices.SortFunc(items, func(a, b Item) int { return cmp.Compare(a.Key, b.Key) })
	return items
}
