// kvbench is a closed-loop HTTP load generator for kv: each of -c goroutines
// sends one request, waits for the reply, then sends the next. Closed-loop
// load understates tail latency (coordinated omission); see docs/benchmarks.md.
package main

import (
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", "http://localhost:8080", "kv base URL")
	c := flag.Int("c", 64, "concurrent connections")
	d := flag.Duration("d", 10*time.Second, "measurement duration")
	reads := flag.Int("reads", 100, "read percentage (0 = writes only)")
	keys := flag.Int("keys", 10_000, "key space size")
	size := flag.Int("size", 128, "value size in bytes")
	flag.Parse()
	if *c < 1 || *keys < 1 || *size < 0 || *reads < 0 || *reads > 100 {
		fmt.Fprintln(os.Stderr, "kvbench: invalid flags")
		os.Exit(2)
	}

	// MaxIdleConnsPerHost >= c: otherwise connections are closed and redialled
	// between requests and we would be timing TCP handshakes.
	client := &http.Client{Transport: &http.Transport{
		MaxIdleConns:        *c,
		MaxIdleConnsPerHost: *c,
		MaxConnsPerHost:     *c,
	}}
	value := strings.Repeat("x", *size)
	url := func(i int) string { return fmt.Sprintf("%s/keys/bench-%d", *addr, i) }

	// do returns false for a transport error or a status >= 300.
	do := func(method, u string) bool {
		var body io.Reader
		if method == http.MethodPut {
			body = strings.NewReader(value)
		}
		req, err := http.NewRequest(method, u, body)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		io.Copy(io.Discard, resp.Body) // drain so the connection is reused
		resp.Body.Close()
		return resp.StatusCode < 300
	}

	// Pre-fill (concurrently, unmeasured) so reads hit existing keys.
	var wg sync.WaitGroup
	fillErrs := make([]int, *c)
	for w := range *c {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < *keys; i += *c {
				if !do(http.MethodPut, url(i)) {
					fillErrs[w]++
				}
			}
		}()
	}
	wg.Wait()
	for _, n := range fillErrs {
		if n > 0 {
			fmt.Fprintln(os.Stderr, "kvbench: pre-fill failed; is kv running at", *addr, "?")
			os.Exit(1)
		}
	}

	lats := make([][]time.Duration, *c) // one slice per goroutine: no locking on the hot path
	errs := make([]int, *c)
	deadline := time.Now().Add(*d)
	start := time.Now()
	for w := range *c {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				method := http.MethodPut
				if rand.IntN(100) < *reads {
					method = http.MethodGet
				}
				t0 := time.Now()
				ok := do(method, url(rand.IntN(*keys)))
				lats[w] = append(lats[w], time.Since(t0))
				if !ok {
					errs[w]++
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	var all []time.Duration
	nerr := 0
	for w := range *c {
		all = append(all, lats[w]...)
		nerr += errs[w]
	}
	slices.Sort(all)
	fmt.Printf("c=%d d=%s reads=%d%% keys=%d size=%dB\n", *c, *d, *reads, *keys, *size)
	fmt.Printf("ops/s=%.0f total=%d errors=%d\n", float64(len(all))/elapsed.Seconds(), len(all), nerr)
	if len(all) > 0 {
		fmt.Printf("p50=%v p99=%v p999=%v max=%v\n",
			percentile(all, 50), percentile(all, 99), percentile(all, 99.9), all[len(all)-1])
	}
}

// percentile returns the nearest-rank p-th percentile of sorted: the smallest
// value with at least p% of samples at or below it.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p/100*float64(len(sorted)) + 0.999999999) // ceil, tolerant of float error
	return sorted[min(max(rank, 1), len(sorted))-1]
}
