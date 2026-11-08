package kvclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kminhas21/kv/internal/kv"
)

func newClient(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(kv.NewHandler(kv.NewStore()))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestClient_RoundTrip(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	v1, err := c.Put(ctx, "vms/a", `{"cpu":2}`, nil)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	value, ver, err := c.Get(ctx, "vms/a")
	if err != nil || value != `{"cpu":2}` || ver != v1 {
		t.Fatalf("Get = %q@%d, %v; want {\"cpu\":2}@%d", value, ver, err, v1)
	}

	c.Put(ctx, "vms/b", "x", nil)
	c.Put(ctx, "hosts/1", "x", nil)
	items, err := c.List(ctx, "vms/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 || items[0].Key != "vms/a" || items[1].Key != "vms/b" {
		t.Fatalf("List(vms/) = %+v, want vms/a, vms/b", items)
	}

	if err := c.Delete(ctx, "vms/a", nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := c.Get(ctx, "vms/a"); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("Get after delete err = %v, want kv.ErrNotFound", err)
	}
	if err := c.Delete(ctx, "vms/a", nil); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("second Delete err = %v, want kv.ErrNotFound", err)
	}
}

func TestClient_CompareAndSwap(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	zero := uint64(0)

	v1, err := c.Put(ctx, "hosts/h1", "seed", &zero)
	if err != nil {
		t.Fatalf("create-only on new key: %v", err)
	}
	if _, err := c.Put(ctx, "hosts/h1", "seed again", &zero); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("create-only on existing key err = %v, want kv.ErrConflict", err)
	}

	v2, err := c.Put(ctx, "hosts/h1", "update", &v1)
	if err != nil {
		t.Fatalf("Put with current version: %v", err)
	}

	current, err := c.Put(ctx, "hosts/h1", "lost update", &v1)
	if !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("Put with stale version err = %v, want kv.ErrConflict", err)
	}
	if current != v2 {
		t.Errorf("conflict reported current version %d, want %d", current, v2)
	}

	if err := c.Delete(ctx, "hosts/h1", &v1); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("Delete with stale version err = %v, want kv.ErrConflict", err)
	}
}

func TestClient_AwkwardKeysSurviveTheURL(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	for _, key := range []string{"hosts/host 1", "a?b", "x#y", "pct/100%"} {
		if _, err := c.Put(ctx, key, "v", nil); err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}
		items, err := c.List(ctx, key)
		if err != nil || len(items) != 1 || items[0].Key != key {
			t.Fatalf("List(%q) = %+v, %v; want exactly that key back", key, items, err)
		}
	}
}

func TestClient_UnexpectedStatusIsInspectable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "disk on fire", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := New(srv.URL).Put(context.Background(), "k", "v", nil)

	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *StatusError", err, err)
	}
	if se.Code != http.StatusInternalServerError || se.Body != "disk on fire" {
		t.Errorf("StatusError = %+v, want code 500 body \"disk on fire\"", se)
	}
}

func TestClient_PutTTLSendsTTLAndIfMatch(t *testing.T) {
	var gotQuery, gotIfMatch string
	h := kv.NewHandler(kv.NewStore())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotIfMatch = r.URL.RawQuery, r.Header.Get("If-Match")
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL)
	ctx := context.Background()

	zero := uint64(0)
	v, err := c.PutTTL(ctx, "leases/vmctl", "pod-a", &zero, 10*time.Second)
	if err != nil {
		t.Fatalf("PutTTL: %v", err)
	}
	if gotQuery != "ttl=10s" || gotIfMatch != "0" {
		t.Errorf("request query=%q If-Match=%q, want ttl=10s and 0", gotQuery, gotIfMatch)
	}
	if _, ver, err := c.Get(ctx, "leases/vmctl"); err != nil || ver != v {
		t.Errorf("Get after PutTTL = v%d, %v; want v%d", ver, err, v)
	}

	if _, err := c.Put(ctx, "leases/vmctl", "pod-a", &v); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("plain Put sent query %q, want none (permanent)", gotQuery)
	}
}

func TestClient_ContextDeadlineCancelsSlowCall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := New(srv.URL).Get(ctx, "k")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("call took %v; the deadline should have cut it off at ~50ms", elapsed)
	}
}
