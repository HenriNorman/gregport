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

	"github.com/hibiken/asynq"
	"github.com/joho/godotenv"

	"github.com/HenriNorman/gregport/internal/email"
	"github.com/HenriNorman/gregport/internal/outbox"
	"github.com/HenriNorman/gregport/internal/platform/cache"
	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/tickets"
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

	ticketsRepo := tickets.NewRepository(pool)
	breachChecker := tickets.NewBreachChecker(ticketsRepo)
	breachHandler := asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		n, err := breachChecker.CheckBreaches(ctx, cfg.SLABreachCheckBatch)
		if err != nil {
			return err
		}
		if n > 0 {
			logger.Info("sla breach check", "breaches", n)
		}
		return nil
	})

	mux := worker.NewMux(
		worker.HandlerRegistration{Type: queue.TaskEmailSend, Handler: emailHandler},
		worker.HandlerRegistration{Type: queue.TaskSLABreachCheck, Handler: breachHandler},
	)
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

	// The SLA breach scheduler enqueues a sla:breach_check task on every tick.
	// The task ID is truncated to the current minute so duplicate enqueues
	// within the same minute are deduplicated by ErrTaskIDConflict, while a new
	// minute gets a fresh ID even if the previous task is still archived.
	if cfg.SLABreachCheckInterval > 0 {
		breachTickerDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(cfg.SLABreachCheckInterval)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					close(breachTickerDone)
					return
				case <-ticker.C:
					taskID := "sla:breach_check:" + time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
					if err := queueClient.EnqueueJob(workerCtx, queue.Job{
						ID:       taskID,
						Type:     queue.TaskSLABreachCheck,
						Queue:    queue.QueueDefault,
						Payload:  []byte(`{}`),
						MaxRetry: 5,
					}); err != nil && workerCtx.Err() == nil {
						logger.Error("enqueue sla breach check", "error", err)
					}
				}
			}
		}()

		defer func() {
			cancelWorker()
			waitBackground(dispatcherDone, "outbox dispatcher", logger, cfg.WorkerShutdownTimeout)
			waitBackground(statsDone, "queue statistics", logger, cfg.WorkerShutdownTimeout)
			waitBackground(breachTickerDone, "sla breach scheduler", logger, cfg.WorkerShutdownTimeout)
		}()
	} else {
		// Stop and join background work before any deferred Close runs, so it can
		// never write to a closed pool, Redis client or inspector.
		defer func() {
			cancelWorker()
			waitBackground(dispatcherDone, "outbox dispatcher", logger, cfg.WorkerShutdownTimeout)
			waitBackground(statsDone, "queue statistics", logger, cfg.WorkerShutdownTimeout)
		}()
	}

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
	if cfg.SLABreachCheckInterval > 0 {
		logger.Info("sla breach scheduler enabled",
			"interval", cfg.SLABreachCheckInterval.String(),
			"batch", cfg.SLABreachCheckBatch,
		)
	} else {
		logger.Info("sla breach scheduler disabled")
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
