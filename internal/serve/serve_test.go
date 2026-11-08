package serve

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRun_FinishesInFlightRequestThenReturnsNil(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("done"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, "test", addr, h) }()

	status := make(chan int, 1)
	go func() {
		for range 50 {
			resp, err := http.Get("http://" + addr)
			if err == nil {
				resp.Body.Close()
				status <- resp.StatusCode
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		status <- 0
	}()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := <-status; got != 200 {
		t.Fatalf("in-flight request got %d, want 200", got)
	}
}
