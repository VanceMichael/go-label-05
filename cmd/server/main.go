package main

import (
	"context"
	"go-base/internal/config"
	"go-base/internal/httpapi"
	"go-base/internal/store"
	"go-base/internal/worker"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("herdcycle stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.Bootstrap(ctx); err != nil {
		return err
	}
	api := httpapi.New(db, cfg.SessionTTL, slog.Default())
	w := worker.Outbox{DB: db, Publisher: worker.LogPublisher{}, Interval: cfg.WorkerInterval, Batch: 20, MaxAttempts: 5, ClaimTimeout: 2 * time.Minute}
	go w.Run(ctx)
	srv := &http.Server{Addr: cfg.Address, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdown)
	}()
	err = srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
