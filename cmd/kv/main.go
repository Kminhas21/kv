package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/Kminhas21/kv/internal/kv"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	handler := kv.NewHandler(kv.NewStore())

	slog.Info("kv listening", "addr", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
