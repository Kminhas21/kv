package kv

import (
	"testing"
)

func TestStore_PutAndGet(t *testing.T) {
	s := NewStore()
	s.Put("k", "v")

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
		_, created := s.Put(st.key, st.value)
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
	v1, _ := s.Put("a", "1")
	v2, _ := s.Put("b", "2")
	v3, _ := s.Put("a", "3")
	if !(v1 < v2 && v2 < v3) {
		t.Fatalf("versions not increasing: %d, %d, %d", v1, v2, v3)
	}
	if _, got, _ := s.Get("a"); got != v3 {
		t.Errorf("Get(a) version = %d, want %d", got, v3)
	}

	s.Delete("a")
	v4, _ := s.Put("a", "again")
	if v4 <= v3+1 {
		t.Errorf("recreated key version = %d, want > %d (delete must consume a revision)", v4, v3+1)
	}
}

func TestStore_DeleteReportsExistence(t *testing.T) {
	s := NewStore()
	if s.Delete("missing") {
		t.Error("Delete(missing) = true, want false")
	}
	s.Put("k", "v")
	if !s.Delete("k") {
		t.Error("Delete(k) = false, want true")
	}
	if _, _, ok := s.Get("k"); ok {
		t.Error("key still present after Delete")
	}
}

func TestStore_ListFiltersAndSorts(t *testing.T) {
	s := NewStore()
	for _, k := range []string{"vms/b", "hosts/1", "vms/a", "vms/c"} {
		s.Put(k, "x")
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
