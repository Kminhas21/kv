package kv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openT(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path, FsyncBatch)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestWAL_ReopenRestoresDataAndRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.wal")

	s := openT(t, path)
	s.Put("a", "1", nil)
	s.Put("b", "2", nil)
	lastBefore, _, _ := s.Put("b", "3", nil)
	s.Delete("a", nil)
	s.Close()

	s = openT(t, path)
	defer s.Close()

	if _, _, ok := s.Get("a"); ok {
		t.Error("deleted key a came back after reopen")
	}
	value, version, ok := s.Get("b")
	if !ok || value != "3" || version != lastBefore {
		t.Errorf("Get(b) = %q@%d ok=%v, want \"3\"@%d", value, version, ok, lastBefore)
	}

	next, _, _ := s.Put("c", "4", nil)
	if next <= lastBefore+1 {
		t.Errorf("first version after reopen = %d, want > %d (revision must resume, delete included)", next, lastBefore+1)
	}

	stale := lastBefore - 1
	if _, _, err := s.Put("b", "x", &stale); err != ErrConflict {
		t.Errorf("CAS with stale version after reopen err = %v, want ErrConflict", err)
	}
}

func TestWAL_TornTailIsDiscardedAndTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.wal")

	s := openT(t, path)
	s.Put("a", "1", nil)
	s.Close()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"op":"put","key":"b","va`)
	f.Close()

	s = openT(t, path)
	if _, _, ok := s.Get("b"); ok {
		t.Error("torn record for b was applied")
	}
	s.Put("c", "3", nil)
	s.Close()

	s = openT(t, path)
	defer s.Close()
	for _, k := range []string{"a", "c"} {
		if _, _, ok := s.Get(k); !ok {
			t.Errorf("key %q missing after second reopen (torn tail not truncated?)", k)
		}
	}
}

func TestWAL_MidFileCorruptionRefusesToOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.wal")
	content := strings.Join([]string{
		`{"op":"put","key":"a","value":"1","version":1}`,
		`this line is garbage`,
		`{"op":"put","key":"b","value":"2","version":2}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path, FsyncBatch)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded on a WAL with mid-file corruption, want error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error %q should name the corrupt line", err)
	}
}
