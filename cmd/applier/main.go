// Package main is the entry point for the hyperfleet-applier binary.
// The applier runs on target clusters, polls desires from a store,
// reconciles them against the local kube-apiserver, and writes status back.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openshift-hyperfleet/hyperfleet-applier/internal/controller"
	redisstore "github.com/openshift-hyperfleet/hyperfleet-adapter/pkg/desire/store/redis"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Config from environment variables (POC: keep it simple)
	redisAddr := envOrDefault("REDIS_ADDRESS", "redis.hyperfleet.svc.cluster.local:6379")
	partition := envOrDefault("APPLIER_PARTITION", "default")
	pollIntervalStr := envOrDefault("APPLIER_POLL_INTERVAL", "5s")

	pollInterval, err := time.ParseDuration(pollIntervalStr)
	if err != nil {
		slog.Error("invalid poll interval", "value", pollIntervalStr, "error", err)
		os.Exit(1)
	}

	slog.Info("starting hyperfleet-applier",
		"redis", redisAddr,
		"partition", partition,
		"pollInterval", pollInterval,
	)

	// Create Redis store (acts as both SpecStore and StatusStore for POC)
	store, err := redisstore.New(redisstore.Config{
		Address: redisAddr,
	})
	if err != nil {
		slog.Error("failed to create redis store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	// Verify Redis connection
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := store.Ping(ctx); err != nil {
		slog.Error("failed to ping redis", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to redis")

	// Create the reconciler
	reconciler, err := controller.NewReconciler(controller.ReconcilerConfig{
		Store:        store,
		Partition:    partition,
		PollInterval: pollInterval,
		Logger:       logger,
	})
	if err != nil {
		slog.Error("failed to create reconciler", "error", err)
		os.Exit(1)
	}

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Run the reconciliation loop
	if err := reconciler.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("reconciler exited with error", "error", err)
		os.Exit(1)
	}

	slog.Info("applier shut down cleanly")
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func init() {
	// Ensure POC build doesn't fail on unused import
	_ = fmt.Sprintf
}
