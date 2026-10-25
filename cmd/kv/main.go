package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Kminhas21/kv/internal/kv"
	"github.com/Kminhas21/kv/internal/serve"
)

func main() {
	addr := envOr("ADDR", ":8080")
	walPath := envOr("WAL_PATH", "kv.wal")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := kv.Open(walPath)
	if err != nil {
		slog.Error("open store", "wal", walPath, "err", err)
		os.Exit(1)
	}

	slog.Info("kv listening", "addr", addr, "wal", walPath)
	err = serve.Run(ctx, "kv", addr, kv.NewHandler(store))
	// Only after Shutdown returns: no handler can touch the store any more.
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
