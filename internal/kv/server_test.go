package kv

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServer_PutGetDeleteList(t *testing.T) {
	srv := httptest.NewServer(NewHandler(NewStore()))
	defer srv.Close()

	do := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
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
