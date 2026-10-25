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
	leaseTTL := durationEnv("LEASE_TTL", 10*time.Second)
	leaseRenew := durationEnv("LEASE_RENEW", 3*time.Second)
	if leaseRenew >= leaseTTL {
		slog.Error("LEASE_RENEW must be shorter than LEASE_TTL", "ttl", leaseTTL, "renew", leaseRenew)
		os.Exit(1)
	}
	id := os.Getenv("POD_NAME") // Downward API in the cluster
	if id == "" {
		id, _ = os.Hostname()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kvc := kvclient.New(kvAddr)
	repo := vm.NewRepo(kvc)

	if err := vm.SeedHosts(ctx, repo, vm.DefaultHosts()); err != nil {
		slog.Error("seed hosts", "err", err)
		os.Exit(1)
	}

	hv := hvclient.New(hvAddr, hvTimeout)
	lease := vm.NewLease(kvc, id, leaseTTL, leaseRenew)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		lease.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		vm.NewReconciler(repo, hv).LeaderOnly(lease.IsLeader).Run(ctx, interval)
	}()

	slog.Info("vmctl listening", "addr", addr, "id", id, "kv", kvAddr, "hypervisor", hvAddr, "hv_timeout", hvTimeout,
		"lease_ttl", leaseTTL, "lease_renew", leaseRenew)
	err := serve.Run(ctx, "vmctl", addr, vm.NewAPI(vm.NewService(repo)))
	stop() // if the server failed on its own, stop the reconciler too
	wg.Wait()
	// Only now: the reconciler has stopped and no renewal can recreate the key.
	releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	lease.Release(releaseCtx)
	cancel()
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
