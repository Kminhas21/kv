package kv

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func BenchmarkGet(b *testing.B) {
	s := NewStore()
	for i := range 10_000 {
		s.data[fmt.Sprintf("key-%d", i)] = Entry{Value: "value", Version: uint64(i + 1)}
	}
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			s.Get("key-" + strconv.Itoa(i%10_000))
			i++
		}
	})
}

// buildWAL writes n unique keys in mode off. snapEvery > 0 leaves a snapshot
// plus a tail of n-snapEvery records (Close waits for the snapshot to finish).
func buildWAL(b *testing.B, path string, n int, snapEvery uint64) {
	s, err := Open(path, FsyncOff, snapEvery)
	if err != nil {
		b.Fatal(err)
	}
	for i := range n {
		if _, _, err := s.Put("key-"+strconv.Itoa(i), "value-of-moderate-length", nil); err != nil {
			b.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkReplay1M times Open (restart) on a WAL of KV_BENCH_REPLAY_N records
// (default 10k so the CI smoke stays fast; `make bench` sets 1000000).
func BenchmarkReplay1M(b *testing.B) {
	n := 10_000
	if v := os.Getenv("KV_BENCH_REPLAY_N"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 2000 {
			b.Fatalf("KV_BENCH_REPLAY_N=%q: want an integer >= 2000", v)
		}
	}
	const tail = 1000
	dir := b.TempDir()
	plain := filepath.Join(dir, "plain", "kv.wal")
	snap := filepath.Join(dir, "snap", "kv.wal")
	for _, p := range []string{plain, snap} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
	}
	buildWAL(b, plain, n, 0)
	buildWAL(b, snap, n, uint64(n-tail))

	for _, c := range []struct{ name, path string }{
		{"wal-only", plain}, {"snapshot+tail", snap},
	} {
		b.Run(fmt.Sprintf("%s/n=%d", c.name, n), func(b *testing.B) {
			for b.Loop() {
				s, err := Open(c.path, FsyncOff, 0)
				if err != nil {
					b.Fatal(err)
				}
				if len(s.data) != n {
					b.Fatalf("recovered %d keys, want %d", len(s.data), n)
				}
				s.Close()
			}
			b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "records/s")
		})
	}
}
