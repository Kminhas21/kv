package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Kminhas21/kv/internal/mockhv"
)

func main() {
	addr := envOr("ADDR", ":8081")
	taskDelay := durationEnv("TASK_DELAY", 2*time.Second)
	hangFor := durationEnv("HANG_FOR", 10*time.Second)

	hv := mockhv.New(mockhv.Config{TaskDelay: taskDelay, HangFor: hangFor})

	slog.Info("mockhv listening", "addr", addr, "task_delay", taskDelay, "hang_for", hangFor)
	if err := http.ListenAndServe(addr, hv.Handler()); err != nil {
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
