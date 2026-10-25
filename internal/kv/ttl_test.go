package kv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock; tests move it instead of sleeping. The
// mutex matters when an HTTP handler goroutine reads it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClockedStore() (*Store, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := NewStore()
	s.now = clk.Now
	return s, clk
}

func TestTTL_ExpiredKeyIsMissingBeforeSweep(t *testing.T) {
	s, clk := newClockedStore()
	s.PutTTL("tmp", "x", nil, 5*time.Second)
	s.Put("perm", "y", nil)

	clk.Advance(4 * time.Second)
	if _, _, ok := s.Get("tmp"); !ok {
		t.Fatal("key vanished before its TTL")
	}

	clk.Advance(time.Second) // exactly at expiry
	if _, _, ok := s.Get("tmp"); ok {
		t.Error("Get returned an expired key")
	}
	if items := s.List(""); len(items) != 1 || items[0].Key != "perm" {
		t.Errorf("List = %v, want only perm", items)
	}
	if err := s.Delete("tmp", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(expired) err = %v, want ErrNotFound", err)
	}
	if _, ok := s.data["tmp"]; !ok {
		t.Error("expired key should still be in memory until the sweeper runs (lazy expiry only)")
	}
}

func TestTTL_PutResetsExpiryAndPlainPutMakesPermanent(t *testing.T) {
	s, clk := newClockedStore()
	s.PutTTL("k", "1", nil, 5*time.Second)
	clk.Advance(4 * time.Second)
	s.PutTTL("k", "2", nil, 5*time.Second) // renew: now expires at t=9s
	clk.Advance(4 * time.Second)
	if _, _, ok := s.Get("k"); !ok {
		t.Fatal("renewed key expired on its old deadline")
	}
	s.Put("k", "3", nil)
	clk.Advance(time.Hour)
	if v, _, ok := s.Get("k"); !ok || v != "3" {
		t.Errorf("Get after plain Put = %q ok=%v, want permanent \"3\"", v, ok)
	}
}

func TestTTL_IfMatchTreatsExpiredKeyAsMissing(t *testing.T) {
	s, clk := newClockedStore()
	v1, _, _ := s.PutTTL("lease", "a", nil, time.Second)
	clk.Advance(time.Second)

	if _, _, err := s.Put("lease", "late renew", &v1); !errors.Is(err, ErrConflict) {
		t.Errorf("renew of expired key with its old version err = %v, want ErrConflict", err)
	}
	if err := s.Delete("lease", &v1); !errors.Is(err, ErrConflict) {
		t.Errorf("Delete(expired, If-Match old) err = %v, want ErrConflict (current version is 0)", err)
	}
	zero := uint64(0)
	v2, created, err := s.PutTTL("lease", "b", &zero, time.Second)
	if err != nil || !created || v2 <= v1 {
		t.Fatalf("If-Match 0 on expired key = v%d created=%v err=%v, want a new version > %d, created", v2, created, err, v1)
	}
}

func TestTTL_SweeperRemovesInChunksWithoutBumpingRevision(t *testing.T) {
	s, clk := newClockedStore()
	for i := range 2500 {
		s.PutTTL(fmt.Sprintf("k%d", i), "x", nil, time.Second)
	}
	s.Put("perm", "y", nil)
	rev := s.revision
	clk.Advance(time.Second)

	if n := s.sweepChunk(); n != maxSweep {
		t.Fatalf("first chunk removed %d, want %d", n, maxSweep)
	}

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.sweepLoop(ctx, ticks) }()
	ticks <- time.Time{}
	ticks <- time.Time{} // unbuffered: accepted only once the first sweep has finished
	cancel()
	<-done

	if len(s.data) != 1 {
		t.Errorf("after sweep %d keys in memory, want 1 (perm)", len(s.data))
	}
	if s.revision != rev {
		t.Errorf("sweep changed revision %d -> %d, want unchanged", rev, s.revision)
	}
}

func TestTTL_CloseStopsSweeper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.wal")
	s := openT(t, path)
	s.StartSweeper(time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// stopSweep waits for the goroutine to exit; a second call must not block or panic.
	s.stopSweep()

	m := NewStore()
	m.StartSweeper(time.Millisecond)
	m.Close() // memory-only stores stop their sweeper too
}

func TestTTL_ReplayDropsExpiredAndKeepsDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.wal")
	s := openT(t, path)
	s.now = func() time.Time { return time.Now().Add(-time.Hour) } // write "an hour ago"
	s.PutTTL("old", "x", nil, time.Minute)                         // expired 59m ago in real time
	s.now = time.Now
	last, _, _ := s.PutTTL("live", "y", nil, time.Hour)
	want := s.data["live"].ExpiresAt
	s.Close()

	s = openT(t, path)
	defer s.Close()
	if _, ok := s.data["old"]; ok {
		t.Error("replay kept a record whose expiry had passed")
	}
	if got := s.data["live"].ExpiresAt; got != want {
		t.Errorf("ExpiresAt after restart = %d, want unchanged %d", got, want)
	}
	if s.revision != last {
		t.Errorf("revision after replay = %d, want %d (dropped records still count)", s.revision, last)
	}
}

func TestTTL_OldWALWithoutExpiryIsPermanent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.wal")
	if err := os.WriteFile(path, []byte(`{"op":"put","key":"a","value":"1","version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := openT(t, path)
	defer s.Close()
	s.now = func() time.Time { return time.Now().Add(100 * 365 * 24 * time.Hour) }
	if v, _, ok := s.Get("a"); !ok || v != "1" {
		t.Errorf("Get(a) from old WAL = %q ok=%v, want permanent \"1\"", v, ok)
	}
}

func TestServer_TTL(t *testing.T) {
	s, clk := newClockedStore()
	srv := httptest.NewServer(NewHandler(s))
	defer srv.Close()
	put := func(query string) int {
		req, _ := http.NewRequest("PUT", srv.URL+"/keys/tmp"+query, strings.NewReader("x"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, bad := range []string{"?ttl=", "?ttl=abc", "?ttl=0s", "?ttl=-5s"} {
		if code := put(bad); code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", bad, code)
		}
	}
	if code := put("?ttl=5s"); code != http.StatusCreated {
		t.Fatalf("PUT ?ttl=5s = %d, want 201", code)
	}
	clk.Advance(5 * time.Second)
	resp, err := http.Get(srv.URL + "/keys/tmp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET expired key = %d, want 404", resp.StatusCode)
	}
}
