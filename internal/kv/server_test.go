package kv

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFaultRoutesAbsentByDefault(t *testing.T) {
	do := newTestServer(t)
	for _, p := range []string{"/debug/crash", "/debug/hang"} {
		if got := do("POST", p, "", "").StatusCode; got != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", p, got)
		}
	}
}

func TestReadyzUnwritableStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "kv.wal"), FsyncOff)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(s))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/readyz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", resp.StatusCode)
	}
	s.Close()
	resp, _ = http.Get(srv.URL + "/readyz")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz after Close = %d, want 503", resp.StatusCode)
	}
}

func TestHangBlocksHealthz(t *testing.T) {
	t.Setenv("FAULTS", "1")
	srv := httptest.NewServer(NewHandler(NewStore()))
	defer srv.Close()
	c := &http.Client{Timeout: 300 * time.Millisecond}
	if resp, err := c.Get(srv.URL + "/healthz"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz before hang: %v", err)
	}
	if resp, err := c.Post(srv.URL+"/debug/hang", "", nil); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("hang: %v", err)
	}
	if _, err := c.Get(srv.URL + "/healthz"); err == nil {
		t.Fatal("healthz answered after hang, want timeout")
	}
}

// newTestServer starts the kv handler on a real local port and returns a
// helper that sends one request with optional If-Match.
func newTestServer(t *testing.T) func(method, path, body, ifMatch string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(NewHandler(NewStore()))
	t.Cleanup(srv.Close)

	return func(method, path, body, ifMatch string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
}

func TestServer_IfMatch(t *testing.T) {
	send := newTestServer(t)

	resp := send("PUT", "/keys/a", "v1", "0")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create-only on new key = %d, want 201", resp.StatusCode)
	}
	v1 := resp.Header.Get("ETag")

	if resp = send("PUT", "/keys/a", "dup", "0"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("create-only on existing key = %d, want 409", resp.StatusCode)
	}

	resp = send("PUT", "/keys/a", "v2", v1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT with current version = %d, want 200", resp.StatusCode)
	}
	v2 := resp.Header.Get("ETag")

	resp = send("PUT", "/keys/a", "lost update", v1)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT with stale version = %d, want 409", resp.StatusCode)
	}
	if got := resp.Header.Get("ETag"); got != v2 {
		t.Errorf("409 ETag = %q, want current version %q", got, v2)
	}

	if resp = send("DELETE", "/keys/a", "", v1); resp.StatusCode != http.StatusConflict {
		t.Fatalf("DELETE with stale version = %d, want 409", resp.StatusCode)
	}
	if resp = send("DELETE", "/keys/a", "", v2); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE with current version = %d, want 204", resp.StatusCode)
	}

	if resp = send("PUT", "/keys/a", "x", "not-a-number"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad If-Match = %d, want 400", resp.StatusCode)
	}
}

func TestServer_KeysMayContainSlashes(t *testing.T) {
	send := newTestServer(t)

	if resp := send("PUT", "/keys/vms/web1", "v", ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT /keys/vms/web1 = %d, want 201", resp.StatusCode)
	}
	resp := send("GET", "/keys/vms/web1", "", "")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "v" {
		t.Fatalf("GET /keys/vms/web1 = %d %q, want 200 \"v\"", resp.StatusCode, body)
	}
	if resp := send("DELETE", "/keys/vms/web1", "", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /keys/vms/web1 = %d, want 204", resp.StatusCode)
	}
}

func TestServer_PutGetDeleteList(t *testing.T) {
	send := newTestServer(t)
	do := func(method, path, body string) *http.Response {
		t.Helper()
		return send(method, path, body, "")
	}

	resp := do("PUT", "/keys/a", "hello")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first PUT status = %d, want 201", resp.StatusCode)
	}
	firstETag := resp.Header.Get("ETag")

	resp = do("PUT", "/keys/a", "world")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second PUT status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("ETag") == firstETag {
		t.Error("ETag did not change after update")
	}

	resp = do("GET", "/keys/a", "")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "world" {
		t.Fatalf("GET = %d %q, want 200 \"world\"", resp.StatusCode, body)
	}

	resp = do("GET", "/keys?prefix=a", "")
	var items []Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Key != "a" {
		t.Fatalf("list = %+v, want one item with key a", items)
	}

	if resp = do("DELETE", "/keys/a", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}
	if resp = do("GET", "/keys/a", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete status = %d, want 404", resp.StatusCode)
	}
	if resp = do("DELETE", "/keys/a", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d, want 404", resp.StatusCode)
	}
}
