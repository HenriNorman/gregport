package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/HenriNorman/gregport/internal/platform/cache"
	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()

	cfg, err := config.LoadWorker()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("logger: %w", err)
	}
	logging.SetDefault(logger)

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.NewPool(signalCtx, cfg.DSN())
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()
	logger.Info("connected to postgres")

	redisCache := cache.NewRedis(cache.RedisOptions{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DB:           cfg.RedisDB,
		DialTimeout:  cfg.RedisDialTimeout,
		ReadTimeout:  cfg.RedisReadTimeout,
		WriteTimeout: cfg.RedisWriteTimeout,
		Timeout:      cfg.RedisReadTimeout,
	})
	defer redisCache.Close()

	handler := worker.HealthHandler(pool, redisCache)
	srv := newServer(cfg, handler)
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	defer ln.Close()

	logger.Info("worker health listening", "port", cfg.WorkerPort, "concurrency", cfg.WorkerConcurrency)
	if err := runServer(signalCtx, srv, ln, cfg.WorkerShutdownTimeout, logger); err != nil {
		return err
	}

	logger.Info("worker stopped")
	return nil
}
