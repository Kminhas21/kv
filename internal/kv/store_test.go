package kv

import (
	"errors"
	"testing"
)

func TestStore_PutAndGet(t *testing.T) {
	s := NewStore()
	s.Put("k", "v", nil)

	got, _, ok := s.Get("k")
	if !ok {
		t.Fatal("expected key to exist")
	}
	if got != "v" {
		t.Errorf("Get = %q, want %q", got, "v")
	}
}

func TestStore_PutReportsCreated(t *testing.T) {
	s := NewStore()
	steps := []struct {
		name        string
		key, value  string
		wantCreated bool
	}{
		{"new key", "key1", "value1", true},
		{"existing key", "key1", "value2", false},
		{"another new key", "key2", "value3", true},
	}
	for _, st := range steps {
		_, created, err := s.Put(st.key, st.value, nil)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", st.name, err)
		}
		if created != st.wantCreated {
			t.Errorf("%s: created = %v, want %v", st.name, created, st.wantCreated)
		}
		got, _, _ := s.Get(st.key)
		if got != st.value {
			t.Errorf("%s: Get = %q, want %q", st.name, got, st.value)
		}
	}
}

func TestStore_VersionsStrictlyIncrease(t *testing.T) {
	s := NewStore()
	v1, _, _ := s.Put("a", "1", nil)
	v2, _, _ := s.Put("b", "2", nil)
	v3, _, _ := s.Put("a", "3", nil)
	if !(v1 < v2 && v2 < v3) {
		t.Fatalf("versions not increasing: %d, %d, %d", v1, v2, v3)
	}
	if _, got, _ := s.Get("a"); got != v3 {
		t.Errorf("Get(a) version = %d, want %d", got, v3)
	}

	s.Delete("a", nil)
	v4, _, _ := s.Put("a", "again", nil)
	if v4 <= v3+1 {
		t.Errorf("recreated key version = %d, want > %d (delete must consume a revision)", v4, v3+1)
	}
}

func TestStore_PutIfMatch(t *testing.T) {
	tests := []struct {
		name        string
		existing    bool
		ifMatch     func(current uint64) uint64
		wantErr     error
		wantCreated bool
	}{
		{"create-only on missing key", false, func(uint64) uint64 { return 0 }, nil, true},
		{"create-only on existing key", true, func(uint64) uint64 { return 0 }, ErrConflict, false},
		{"version on missing key", false, func(uint64) uint64 { return 7 }, ErrConflict, false},
		{"matching version", true, func(cur uint64) uint64 { return cur }, nil, false},
		{"stale version", true, func(cur uint64) uint64 { return cur - 1 }, ErrConflict, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStore()
			s.Put("other", "bump the revision", nil)
			var current uint64
			if tt.existing {
				current, _, _ = s.Put("k", "old", nil)
			}

			ifMatch := tt.ifMatch(current)
			version, created, err := s.Put("k", "new", &ifMatch)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if created != tt.wantCreated {
				t.Errorf("created = %v, want %v", created, tt.wantCreated)
			}
			got, gotVersion, _ := s.Get("k")
			if tt.wantErr != nil {
				if version != current {
					t.Errorf("conflict returned version %d, want current %d", version, current)
				}
				if tt.existing && got != "old" {
					t.Errorf("value changed to %q on conflict", got)
				}
				return
			}
			if got != "new" || gotVersion != version {
				t.Errorf("Get = %q@%d, want \"new\"@%d", got, gotVersion, version)
			}
		})
	}
}

func TestStore_Delete(t *testing.T) {
	s := NewStore()
	if err := s.Delete("missing", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(missing) err = %v, want ErrNotFound", err)
	}

	v, _, _ := s.Put("k", "v", nil)
	stale := v - 1
	if err := s.Delete("k", &stale); !errors.Is(err, ErrConflict) {
		t.Errorf("Delete with stale version err = %v, want ErrConflict", err)
	}
	if _, _, ok := s.Get("k"); !ok {
		t.Fatal("key deleted despite conflict")
	}

	if err := s.Delete("k", &v); err != nil {
		t.Errorf("Delete with current version err = %v, want nil", err)
	}
	if _, _, ok := s.Get("k"); ok {
		t.Error("key still present after Delete")
	}
}

func TestStore_ListFiltersAndSorts(t *testing.T) {
	s := NewStore()
	for _, k := range []string{"vms/b", "hosts/1", "vms/a", "vms/c"} {
		s.Put(k, "x", nil)
	}

	got := s.List("vms/")
	want := []string{"vms/a", "vms/b", "vms/c"}
	if len(got) != len(want) {
		t.Fatalf("List returned %d items, want %d: %+v", len(got), len(want), got)
	}
	for i, item := range got {
		if item.Key != want[i] {
			t.Errorf("item %d = %q, want %q", i, item.Key, want[i])
		}
	}

	if all := s.List(""); len(all) != 4 {
		t.Errorf("List(\"\") returned %d items, want 4", len(all))
	}
}
