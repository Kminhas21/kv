package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Kminhas21/kv/internal/hvclient"
	"github.com/Kminhas21/kv/internal/kvclient"
	"github.com/Kminhas21/kv/internal/serve"
	"github.com/Kminhas21/kv/internal/vm"
)

func main() {
	addr := envOr("ADDR", ":8090")
	kvAddr := envOr("KV_ADDR", "http://localhost:8080")
	hvAddr := envOr("HV_ADDR", "http://localhost:8081")
	hvTimeout := durationEnv("HV_TIMEOUT", 3*time.Second)
	interval := durationEnv("RECONCILE_INTERVAL", 2*time.Second)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repo := vm.NewRepo(kvclient.New(kvAddr))

	if err := vm.SeedHosts(ctx, repo, vm.DefaultHosts()); err != nil {
		slog.Error("seed hosts", "err", err)
		os.Exit(1)
	}

	hv := hvclient.New(hvAddr, hvTimeout)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		vm.NewReconciler(repo, hv).Run(ctx, interval)
	}()

	slog.Info("vmctl listening", "addr", addr, "kv", kvAddr, "hypervisor", hvAddr, "hv_timeout", hvTimeout)
	err := serve.Run(ctx, "vmctl", addr, vm.NewAPI(vm.NewService(repo)))
	stop() // if the server failed on its own, stop the reconciler too
	wg.Wait()
	slog.Info("vmctl stopped")
	if err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		slog.Error("invalid duration", "env", key, "value", v, "err", err)
		os.Exit(1)
	}
	return d
}
