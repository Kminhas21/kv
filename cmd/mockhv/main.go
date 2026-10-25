package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Kminhas21/kv/internal/mockhv"
	"github.com/Kminhas21/kv/internal/serve"
)

func main() {
	addr := envOr("ADDR", ":8081")
	taskDelay := durationEnv("TASK_DELAY", 2*time.Second)
	hangFor := durationEnv("HANG_FOR", 10*time.Second)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hv := mockhv.New(mockhv.Config{TaskDelay: taskDelay, HangFor: hangFor})

	slog.Info("mockhv listening", "addr", addr, "task_delay", taskDelay, "hang_for", hangFor)
	if err := serve.Run(ctx, "mockhv", addr, hv.Handler()); err != nil {
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
