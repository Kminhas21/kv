package kv

import (
	"cmp"
	"slices"
	"strings"
	"sync"
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

func (s *Store) Put(key, value string) (version uint64, created bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.data[key]
	s.revision++
	s.data[key] = Entry{Value: value, Version: s.revision}
	return s.revision, !existed
}

func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return false
	}
	s.revision++
	delete(s.data, key)
	return true
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
