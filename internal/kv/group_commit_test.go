package kv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var allModes = []FsyncMode{FsyncOff, FsyncAlways, FsyncBatch}

// lastSyncedVersion returns the version of the last complete record in the
// WAL file, i.e. the newest revision on disk.
func lastSyncedVersion(t *testing.T, path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read WAL: %v", err)
		return 0
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	var rec walRecord
	if err := json.Unmarshal(lines[len(lines)-1], &rec); err != nil {
		t.Errorf("parse last WAL line: %v", err)
	}
	return rec.Version
}

func TestGroupCommit_ConcurrentAcksAreDurableAndReplayed(t *testing.T) {
	const writers = 32
	for _, mode := range allModes {
		t.Run(string(mode), func(t *testing.T) {
			perWriter := 25
			if mode == FsyncAlways {
				perWriter = 3 // one fsync per write is ~15ms on Windows
			}
			path := filepath.Join(t.TempDir(), "kv.wal")
			s, err := Open(path, mode)
			if err != nil {
				t.Fatal(err)
			}
			// Wrap fsync to count calls and remember the newest revision it made
			// durable, so each writer can check its ack came after its fsync.
			var syncs atomic.Int64
			var durable atomic.Uint64
			s.syncWAL = func() error {
				if err := s.wal.Sync(); err != nil {
					return err
				}
				syncs.Add(1)
				durable.Store(lastSyncedVersion(t, path))
				return nil
			}

			var mu sync.Mutex
			acked := map[string]uint64{}
			var wg sync.WaitGroup
			for w := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range perWriter {
						key := fmt.Sprintf("w%d-%d", w, i)
						v, _, err := s.Put(key, "x", nil)
						if err != nil {
							t.Errorf("Put(%s): %v", key, err)
							return
						}
						if mode != FsyncOff && v > durable.Load() {
							t.Errorf("Put(%s) acked version %d, but only %d is fsynced", key, v, durable.Load())
						}
						mu.Lock()
						acked[key] = v
						mu.Unlock()
					}
				}()
			}
			wg.Wait()

			total := int64(writers * perWriter)
			switch mode {
			case FsyncOff:
				if n := syncs.Load(); n != 0 {
					t.Errorf("off mode fsynced %d times, want 0", n)
				}
			case FsyncAlways:
				if n := syncs.Load(); n != total {
					t.Errorf("always mode fsynced %d times for %d writes, want one each", n, total)
				}
			case FsyncBatch: // grouping is proven deterministically in TestGroupCommit_BatchesQueuedRecords
				t.Logf("%d writes in %d fsyncs", total, syncs.Load())
			}

			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path, mode)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for key, want := range acked {
				if _, v, ok := s.Get(key); !ok || v != want {
					t.Errorf("after reopen Get(%s) = @%d ok=%v, want @%d", key, v, ok, want)
				}
			}
		})
	}
}

// While the first fsync is held, n writers queue up; once it is released
// the flusher must commit all n in a single batch with a single fsync.
func TestGroupCommit_BatchesQueuedRecords(t *testing.T) {
	const n = 10
	path := filepath.Join(t.TempDir(), "kv.wal")
	s := openT(t, path)
	defer s.Close()

	entered, release := make(chan struct{}), make(chan struct{})
	var batchSizes []int // records per fsync; only the flusher appends
	lines := 0
	s.syncWAL = func() error {
		if len(batchSizes) == 0 {
			close(entered)
			<-release
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		now := bytes.Count(data, []byte("\n"))
		batchSizes = append(batchSizes, now-lines)
		lines = now
		return s.wal.Sync()
	}

	put := func(wg *sync.WaitGroup, key string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Put(key, "x", nil); err != nil {
				t.Errorf("Put(%s): %v", key, err)
			}
		}()
	}
	var wg sync.WaitGroup
	put(&wg, "first")
	<-entered // the flusher is now stuck inside the first fsync
	for i := range n {
		put(&wg, fmt.Sprintf("k%d", i))
	}
	for queued := 0; queued < n; runtime.Gosched() {
		s.qmu.Lock()
		queued = len(s.queue)
		s.qmu.Unlock()
	}
	close(release)
	wg.Wait()

	if want := []int{1, n}; !slices.Equal(batchSizes, want) {
		t.Errorf("records per fsync = %v, want %v", batchSizes, want)
	}
}

func TestGroupCommit_SyncFailurePoisonsStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.wal")
	s := openT(t, path)
	if _, _, err := s.Put("a", "1", nil); err != nil {
		t.Fatal(err)
	}
	s.syncWAL = func() error { return errors.New("disk on fire") }

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Put(fmt.Sprintf("k%d", i), "x", nil); err == nil || !strings.Contains(err.Error(), "fsync") {
				t.Errorf("Put during failing fsync err = %v, want an fsync error", err)
			}
		}()
	}
	wg.Wait()

	if s.Writable() {
		t.Error("Writable() = true after a failed fsync")
	}
	if _, _, err := s.Put("later", "x", nil); err == nil {
		t.Error("Put after poisoning succeeded")
	}
	if err := s.Delete("a", nil); err == nil {
		t.Error("Delete after poisoning succeeded")
	}
	if err := s.Close(); err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("Close on a poisoned store err = %v, want the fsync error", err)
	}
	s.Close() // a second Close on a poisoned store must not panic
}

func TestGroupCommit_CloseDrainsInFlightWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kv.wal")
	s := openT(t, path)

	var mu sync.Mutex
	acked := map[string]uint64{}
	var count atomic.Int64
	reached := make(chan struct{}) // closed at the 200th ack
	var wg sync.WaitGroup
	for w := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				key := fmt.Sprintf("w%d-%d", w, i)
				v, _, err := s.Put(key, "x", nil)
				if err != nil {
					if !errors.Is(err, errClosed) {
						t.Errorf("Put(%s) err = %v, want errClosed", key, err)
					}
					return
				}
				mu.Lock()
				acked[key] = v
				mu.Unlock()
				if count.Add(1) == 200 {
					close(reached)
				}
			}
		}()
	}
	<-reached
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if s.Writable() {
		t.Error("Writable() = true after Close")
	}

	s = openT(t, path)
	defer s.Close()
	for key, want := range acked {
		if _, v, ok := s.Get(key); !ok || v != want {
			t.Errorf("after reopen Get(%s) = @%d ok=%v, want @%d", key, v, ok, want)
		}
	}
}

func TestOpen_RejectsUnknownFsyncMode(t *testing.T) {
	if s, err := Open(filepath.Join(t.TempDir(), "kv.wal"), "sometimes"); err == nil {
		s.Close()
		t.Fatal("Open accepted fsync mode \"sometimes\"")
	}
}

func BenchmarkPut(b *testing.B) {
	for _, mode := range allModes {
		b.Run(string(mode), func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "kv.wal"), mode)
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			var n atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, _, err := s.Put(fmt.Sprintf("k%d", n.Add(1)%1024), "value", nil); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
