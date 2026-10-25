package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Kminhas21/kv/internal/kv"
	"github.com/Kminhas21/kv/internal/serve"
)

func main() {
	addr := envOr("ADDR", ":8080")
	walPath := envOr("WAL_PATH", "kv.wal")
	fsync := kv.FsyncMode(envOr("WAL_FSYNC", string(kv.FsyncBatch)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sweepEvery, err := time.ParseDuration(envOr("TTL_SWEEP_INTERVAL", "1s"))
	if err != nil || sweepEvery <= 0 {
		slog.Error("invalid TTL_SWEEP_INTERVAL: want a positive duration like 1s", "value", os.Getenv("TTL_SWEEP_INTERVAL"))
		os.Exit(1)
	}

	// 0 disables snapshots: the WAL then grows forever and replays from the start.
	snapEvery, err := strconv.ParseUint(envOr("SNAPSHOT_EVERY", "100000"), 10, 64)
	if err != nil {
		slog.Error("invalid SNAPSHOT_EVERY: want a record count, 0 to disable", "value", os.Getenv("SNAPSHOT_EVERY"))
		os.Exit(1)
	}

	store, err := kv.Open(walPath, fsync, snapEvery)
	if err != nil {
		slog.Error("open store", "wal", walPath, "err", err)
		os.Exit(1)
	}
	store.StartSweeper(sweepEvery)

	slog.Info("kv listening", "addr", addr, "wal", walPath, "fsync", fsync, "snapshot_every", snapEvery)
	err = serve.Run(ctx, "kv", addr, kv.NewHandler(store))
	// After Shutdown: normally no handler is left; if the drain timed out, stragglers may remain but the process exits next.
	if cerr := store.Close(); cerr != nil {
		slog.Error("close store", "err", cerr)
	} else {
		slog.Info("store closed")
	}
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
