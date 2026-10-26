package kv

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetricsEndpointExposesKVMetricsWithPatternRoutes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "kv.wal"), FsyncBatch, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := httptest.NewServer(NewHandler(s))
	defer srv.Close()

	req, _ := http.NewRequest("PUT", srv.URL+"/keys/some/secret-key", strings.NewReader("v"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if r, err := http.Get(srv.URL + "/nope"); err == nil {
		r.Body.Close()
	}

	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	out := string(b)

	for _, want := range []string{
		`http_request_duration_seconds_count{code="201",method="PUT",route="PUT /keys/{key...}"} 1`,
		`route="unmatched"`,
		"wal_fsync_duration_seconds_count 1",
		"wal_batch_records_count 1",
		"wal_replay_duration_seconds ",
		"kv_keys 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
	if strings.Contains(out, "secret-key") {
		t.Error("concrete key leaked into a label")
	}
}
