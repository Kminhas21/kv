package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/Kminhas21/kv/internal/kv"
)

func main() {
	addr := envOr("ADDR", ":8080")
	walPath := envOr("WAL_PATH", "kv.wal")

	store, err := kv.Open(walPath)
	if err != nil {
		slog.Error("open store", "wal", walPath, "err", err)
		os.Exit(1)
	}
	defer store.Close()

	slog.Info("kv listening", "addr", addr, "wal", walPath)
	if err := http.ListenAndServe(addr, kv.NewHandler(store)); err != nil {
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
