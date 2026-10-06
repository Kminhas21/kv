package kv

import (
	"sync"
	"time"
)

type Entry struct {
	Value     string
	ExpiresAt time.Time
}

type Store struct {
	store map[string]Entry
	mu    sync.RWMutex
}

func NewStore() *Store {
	return &Store{store: make(map[string]Entry)}
}

func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.store[key]
	return value.Value, ok
}

func (s *Store) Put(key string, value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.store[key]
	s.store[key] = Entry{Value: value}
	return ok
}

func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.store[key]
	delete(s.store, key)
	return ok
}

func (s *Store) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.store))
	for key := range s.store {
		keys = append(keys, key)
	}
	return keys
}
