package kv

import (
	"cmp"
	"errors"
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

type Store struct {
	mu       sync.RWMutex
	data     map[string]Entry
	revision uint64
}

func NewStore() *Store {
	return &Store{data: make(map[string]Entry)}
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
	s.revision++
	s.data[key] = Entry{Value: value, Version: s.revision}
	return s.revision, !existed, nil
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
	s.revision++
	delete(s.data, key)
	return nil
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
