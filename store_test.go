package main

import (
	"testing"
)

func TestStore_PutAndGet(t *testing.T) {
	store := NewStore()
	key := "testKey"
	value := "testValue"
	store.Put(key, value)
	retrievedValue, ok := store.Get(key)
	if !ok {
		t.Errorf("Expected key %s to exist", key)
	}
	if retrievedValue != value {
		t.Errorf("Expected value %s, got %s", value, retrievedValue)
	}
}

func AllTests(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantNew bool
	}{
		{"new key", "key1", "value1", true},
		{"existing key", "key1", "value2", false},
		{"another new key", "key2", "value3", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore()
			store.Put(tt.key, tt.value)
			retrievedValue, ok := store.Get(tt.key)
			if !ok {
				t.Errorf("Expected key %s to exist", tt.key)
			}
			if retrievedValue != tt.value {
				t.Errorf("Expected value %s, got %s", tt.value, retrievedValue)
			}
		})
	}
}
