package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/Kminhas21/kv/internal/kvclient"
	"github.com/Kminhas21/kv/internal/vm"
)

func main() {
	addr := envOr("ADDR", ":8090")
	kvAddr := envOr("KV_ADDR", "http://localhost:8080")

	repo := vm.NewRepo(kvclient.New(kvAddr))
	svc := vm.NewService(repo)

	slog.Info("vmctl listening", "addr", addr, "kv", kvAddr)
	if err := http.ListenAndServe(addr, vm.NewAPI(svc)); err != nil {
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
