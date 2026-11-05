package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/HenriNorman/gregport/internal/email"
	"github.com/HenriNorman/gregport/internal/outbox"
	"github.com/HenriNorman/gregport/internal/platform/cache"
	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/queue"
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

	queueCfg := queue.Config{
		Redis: queue.RedisOptions{
			Addr:         cfg.RedisAddr,
			Password:     cfg.RedisPassword,
			DB:           cfg.RedisDB,
			DialTimeout:  cfg.RedisDialTimeout,
			ReadTimeout:  cfg.RedisReadTimeout,
			WriteTimeout: cfg.RedisWriteTimeout,
		},
		IOConcurrency:   cfg.WorkerConcurrency,
		CPUConcurrency:  cfg.WorkerCPUConcurrency,
		ShutdownTimeout: cfg.WorkerShutdownTimeout,
		Logger:          logger,
	}
	queueClient := queue.NewClient(queueCfg)
	defer queueClient.Close()

	emailRepo := email.NewRepository(pool)
	emailSvc := email.NewService(emailRepo)

	smtpSender := email.NewSMTPSender(
		cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPassword,
		cfg.SMTPFrom, cfg.SMTPFromName,
	)
	breaker := email.NewCircuitBreaker(
		smtpSender,
		5, // trip after 5 consecutive failures
		30*time.Second,
	)
	emailHandler := email.NewTaskHandler(emailSvc, breaker, logger)

	mux := worker.NewMux(emailHandler)
	ioServer := queue.NewIOServer(queueCfg)
	cpuServer := queue.NewCPUServer(queueCfg)

	if err := ioServer.Start(mux); err != nil {
		return fmt.Errorf("start io queue server: %w", err)
	}
	defer ioServer.Shutdown()
	if err := cpuServer.Start(mux); err != nil {
		return fmt.Errorf("start cpu queue server: %w", err)
	}
	defer cpuServer.Shutdown()

	logger.Info("queue servers started",
		"io_concurrency", cfg.WorkerConcurrency,
		"cpu_concurrency", cfg.WorkerCPUConcurrency,
		"queues", "critical,default,low,cpu",
	)

	// The dispatcher turns committed outbox rows into queue jobs. It shares the
	// process signal context, so a shutdown stops polling immediately.
	workerCtx, cancelWorker := context.WithCancel(signalCtx)
	dispatcher := outbox.NewDispatcher(pool, queueClient, logger, cfg.OutboxBatch, cfg.OutboxInterval)
	dispatcherDone := make(chan struct{})
	go func() {
		dispatcher.Run(workerCtx)
		close(dispatcherDone)
	}()

	// Queue statistics make dead letters visible without opening Asynqmon.
	inspector := queue.NewInspector(queueCfg)
	defer inspector.Close()
	statsDone := make(chan struct{})
	go func() {
		worker.RunQueueStats(workerCtx, inspector, logger, cfg.QueueStatsInterval)
		close(statsDone)
	}()

	// Stop and join background work before any deferred Close runs, so it can
	// never write to a closed pool, Redis client or inspector.
	defer func() {
		cancelWorker()
		waitBackground(dispatcherDone, "outbox dispatcher", logger, cfg.WorkerShutdownTimeout)
		waitBackground(statsDone, "queue statistics", logger, cfg.WorkerShutdownTimeout)
	}()

	logger.Info("outbox dispatcher started",
		"interval", cfg.OutboxInterval.String(),
		"batch", cfg.OutboxBatch,
	)
	if cfg.QueueStatsInterval > 0 {
		logger.Info("queue statistics enabled",
			"interval", cfg.QueueStatsInterval.String(),
		)
	} else {
		logger.Info("queue statistics disabled")
	}

	handler := worker.HealthHandler(pool, redisCache, queueClient)
	srv := newServer(cfg, handler)
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	defer ln.Close()

	logger.Info("worker health listening", "port", cfg.WorkerPort)
	if err := runServer(signalCtx, srv, ln, cfg.WorkerShutdownTimeout, logger); err != nil {
		return err
	}

	logger.Info("worker stopped")
	return nil
}

// waitBackground joins a background goroutine, warning instead of hanging past
// the shutdown budget.
func waitBackground(done <-chan struct{}, name string, logger *slog.Logger, timeout time.Duration) {
	select {
	case <-done:
	case <-time.After(timeout):
		logger.Warn("background task did not stop within the shutdown timeout", "task", name)
	}
}
