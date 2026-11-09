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

	"github.com/HenriNorman/gregport/internal/attachments"
	"github.com/HenriNorman/gregport/internal/email"
	"github.com/HenriNorman/gregport/internal/housekeeping"
	"github.com/HenriNorman/gregport/internal/idempotency"
	"github.com/HenriNorman/gregport/internal/notifications"
	"github.com/HenriNorman/gregport/internal/outbox"
	"github.com/HenriNorman/gregport/internal/platform/audit"
	"github.com/HenriNorman/gregport/internal/platform/cache"
	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/logging"
	"github.com/HenriNorman/gregport/internal/queue"
	"github.com/HenriNorman/gregport/internal/ratings"
	"github.com/HenriNorman/gregport/internal/storage"
	"github.com/HenriNorman/gregport/internal/tickets"
	"github.com/HenriNorman/gregport/internal/webhooks"
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

	objectStore, err := storage.New(storage.Config{
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		Bucket:    cfg.S3Bucket,
		Region:    cfg.S3Region,
		UseSSL:    cfg.S3UseSSL,
	})
	if err != nil {
		return fmt.Errorf("object storage: %w", err)
	}
	if err := objectStore.EnsureBucket(signalCtx); err != nil {
		logger.Warn("object storage unavailable at startup", "error", err)
	}

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
	emailSvc.SetRatingLinks(
		ratings.NewTokenManager(cfg.RatingTokenSecret, cfg.RatingTokenTTL),
		cfg.PublicAppURL,
	)

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

	notifyRepo := notifications.NewRepository(pool)
	notifySvc := notifications.NewService(notifyRepo)
	notifyHandler := notifications.NewTaskHandler(notifySvc, notifyRepo, logger)

	webhookSvc := webhooks.NewService(webhooks.NewRepository(pool))
	webhookSvc.SetSender(webhooks.NewHTTPSender(webhooks.SenderOptions{Timeout: webhooks.DefaultTimeout}))
	webhookSvc.SetEnqueuer(queueClient)
	webhookHandler := webhooks.NewTaskHandler(webhookSvc, logger)

	attachmentRepo := attachments.NewRepository(pool)
	attachmentHandler := attachments.NewTaskHandler(
		attachmentRepo, objectStore, cfg.ThumbnailMaxDimension, logger,
	)

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

	autoCloseChecker := tickets.NewAutoCloseChecker(ticketsRepo, cfg.AutoCloseWindow)
	autoCloseHandler := asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		n, err := autoCloseChecker.CheckAutoClose(ctx, cfg.AutoCloseCheckBatch)
		if err != nil {
			return err
		}
		if n > 0 {
			logger.Info("auto close check", "closed", n)
		}
		return nil
	})

	idempotencyRepo := idempotency.NewRepository(pool)
	housekeepingChecker := housekeeping.NewChecker(
		func(ctx context.Context, before time.Time, batch int) (int64, error) {
			return idempotencyRepo.PurgeExpired(ctx, before)
		},
		func(ctx context.Context, before time.Time, batch int) (int64, error) {
			return ticketsRepo.PurgeDispatchedEvents(ctx, before, batch)
		},
		func(ctx context.Context, before time.Time, batch int) (int64, error) {
			return audit.PurgeOldLogs(ctx, pool, before, batch)
		},
		func(ctx context.Context, before time.Time, batch int) (int64, error) {
			return emailRepo.PurgeOldDeliveries(ctx, before, batch)
		},
	)
	housekeepingHandler := asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		summary, err := housekeepingChecker.Run(ctx, housekeeping.Config{
			IDempotencyTTL:         cfg.IDempotencyTTL,
			TicketEventRetention:   cfg.TicketEventRetention,
			AuditLogRetention:      cfg.AuditLogRetention,
			EmailDeliveryRetention: cfg.EmailDeliveryRetention,
			Batch:                  cfg.HousekeepingBatch,
		})
		if err != nil {
			return err
		}
		logger.Info("housekeeping run",
			"idempotency_keys_purged", summary.IDempotencyKeysPurged,
			"ticket_events_purged", summary.TicketEventsPurged,
			"audit_logs_purged", summary.AuditLogsPurged,
			"email_deliveries_purged", summary.EmailDeliveriesPurged,
		)
		return nil
	})

	mux := worker.NewMux(
		worker.HandlerRegistration{Type: queue.TaskEmailSend, Handler: emailHandler},
		worker.HandlerRegistration{Type: queue.TaskNotify, Handler: notifyHandler},
		worker.HandlerRegistration{Type: queue.TaskWebhookDispatch, Handler: asynq.HandlerFunc(webhookHandler.Dispatch)},
		worker.HandlerRegistration{Type: queue.TaskWebhookDeliver, Handler: asynq.HandlerFunc(webhookHandler.Deliver)},
		worker.HandlerRegistration{Type: queue.TaskAttachmentValidate, Handler: asynq.HandlerFunc(attachmentHandler.Validate)},
		worker.HandlerRegistration{Type: queue.TaskThumbnailGenerate, Handler: asynq.HandlerFunc(attachmentHandler.Thumbnail)},
		worker.HandlerRegistration{Type: queue.TaskSLABreachCheck, Handler: breachHandler},
		worker.HandlerRegistration{Type: queue.TaskAutoClose, Handler: autoCloseHandler},
		worker.HandlerRegistration{Type: queue.TaskHousekeeping, Handler: housekeepingHandler},
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

	// All background work shares workerCtx so a shutdown stops polling
	// immediately. Each goroutine closes its own done channel when it exits.
	workerCtx, cancelWorker := context.WithCancel(signalCtx)
	type donePair struct {
		done chan struct{}
		name string
	}
	var done []donePair

	// The dispatcher turns committed outbox rows into queue jobs.
	dispatcher := outbox.NewDispatcher(pool, queueClient, logger, cfg.OutboxBatch, cfg.OutboxInterval)
	dispatcherDone := make(chan struct{})
	go func() {
		dispatcher.Run(workerCtx)
		close(dispatcherDone)
	}()
	done = append(done, donePair{done: dispatcherDone, name: "outbox dispatcher"})

	// Queue statistics make dead letters visible without opening Asynqmon.
	inspector := queue.NewInspector(queueCfg)
	defer inspector.Close()
	statsDone := make(chan struct{})
	go func() {
		worker.RunQueueStats(workerCtx, inspector, logger, cfg.QueueStatsInterval)
		close(statsDone)
	}()
	done = append(done, donePair{done: statsDone, name: "queue statistics"})

	// SLA breach scheduler: enqueues a sla:breach_check task each tick.
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
		done = append(done, donePair{done: breachTickerDone, name: "sla breach scheduler"})
	}

	// Auto-close scheduler: enqueues a ticket:auto_close task each tick.
	if cfg.AutoCloseCheckInterval > 0 {
		autoCloseTickerDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(cfg.AutoCloseCheckInterval)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					close(autoCloseTickerDone)
					return
				case <-ticker.C:
					taskID := "ticket:auto_close:" + time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
					if err := queueClient.EnqueueJob(workerCtx, queue.Job{
						ID:       taskID,
						Type:     queue.TaskAutoClose,
						Queue:    queue.QueueDefault,
						Payload:  []byte(`{}`),
						MaxRetry: 5,
					}); err != nil && workerCtx.Err() == nil {
						logger.Error("enqueue ticket auto close", "error", err)
					}
				}
			}
		}()
		done = append(done, donePair{done: autoCloseTickerDone, name: "auto close scheduler"})
	}

	// Housekeeping scheduler: enqueues a housekeeping:run task each tick on the
	// low queue.
	if cfg.HousekeepingInterval > 0 {
		housekeepingTickerDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(cfg.HousekeepingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-workerCtx.Done():
					close(housekeepingTickerDone)
					return
				case <-ticker.C:
					taskID := "housekeeping:run:" + time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
					if err := queueClient.EnqueueJob(workerCtx, queue.Job{
						ID:       taskID,
						Type:     queue.TaskHousekeeping,
						Queue:    queue.QueueLow,
						Payload:  []byte(`{}`),
						MaxRetry: 5,
					}); err != nil && workerCtx.Err() == nil {
						logger.Error("enqueue housekeeping run", "error", err)
					}
				}
			}
		}()
		done = append(done, donePair{done: housekeepingTickerDone, name: "housekeeping scheduler"})
	}

	// Stop and join background work before any deferred Close runs, so it can
	// never write to a closed pool, Redis client or inspector.
	defer func() {
		cancelWorker()
		for _, pair := range done {
			waitBackground(pair.done, pair.name, logger, cfg.WorkerShutdownTimeout)
		}
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
	if cfg.SLABreachCheckInterval > 0 {
		logger.Info("sla breach scheduler enabled",
			"interval", cfg.SLABreachCheckInterval.String(),
			"batch", cfg.SLABreachCheckBatch,
		)
	} else {
		logger.Info("sla breach scheduler disabled")
	}
	if cfg.AutoCloseCheckInterval > 0 {
		logger.Info("auto close scheduler enabled",
			"interval", cfg.AutoCloseCheckInterval.String(),
			"window", cfg.AutoCloseWindow.String(),
			"batch", cfg.AutoCloseCheckBatch,
		)
	} else {
		logger.Info("auto close scheduler disabled")
	}
	if cfg.HousekeepingInterval > 0 {
		logger.Info("housekeeping scheduler enabled",
			"interval", cfg.HousekeepingInterval.String(),
			"batch", cfg.HousekeepingBatch,
			"ticket_event_retention", cfg.TicketEventRetention.String(),
			"audit_log_retention", cfg.AuditLogRetention.String(),
			"email_delivery_retention", cfg.EmailDeliveryRetention.String(),
			"idempotency_ttl", cfg.IDempotencyTTL.String(),
		)
	} else {
		logger.Info("housekeeping scheduler disabled")
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
