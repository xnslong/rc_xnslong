package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xnslong/rc_xnslong/internal/api/handler"
	"github.com/xnslong/rc_xnslong/internal/config"
	"github.com/xnslong/rc_xnslong/internal/db/postgres"
	"github.com/xnslong/rc_xnslong/internal/delivery"
	"github.com/xnslong/rc_xnslong/internal/ingestion"
	"github.com/xnslong/rc_xnslong/internal/mapping"
	"github.com/xnslong/rc_xnslong/internal/mq/rabbitmq"
	"github.com/xnslong/rc_xnslong/internal/routing"
)

func main() {
	// Parse flags
	configDir := flag.String("config-dir", envOrDefault("CONFIG_DIR", "testdata"), "config directory")
	httpAddr := flag.String("http-addr", envOrDefault("HTTP_ADDR", ":8080"), "HTTP listen address")
	pgURL := flag.String("pg-url", envOrDefault("PG_URL", "postgres://notify:notify@localhost:5432/notification?sslmode=disable"), "PostgreSQL URL")
	mqURL := flag.String("mq-url", envOrDefault("MQ_URL", "amqp://notify:notify@localhost:5672/"), "RabbitMQ URL")
	flag.Parse()

	// 1. Create DB client
	db, err := postgres.NewClient(*pgURL)
	if err != nil {
		log.Fatalf("create db client: %v", err)
	}
	defer db.Close()

	// 2. Create MQ client (declares topology)
	mq, err := rabbitmq.NewClient(*mqURL)
	if err != nil {
		log.Fatalf("create mq client: %v", err)
	}
	defer mq.Close()

	// 3. Load config
	cfg, err := config.NewLoader(*configDir)
	if err != nil {
		log.Fatalf("create config loader: %v", err)
	}
	if err := cfg.Load(context.Background()); err != nil {
		log.Fatalf("load config: %v", err)
	}

	// 4. Create mapping engine
	engine := mapping.NewEngine()

	// 5. Create delivery message consumer channel
	deliveryEvents, err := mq.Consume(rabbitmq.DeliveryQueue, "worker", false)
	if err != nil {
		log.Fatalf("consume delivery queue: %v", err)
	}

	// 6. Create and start worker pool
	worker := delivery.NewWorkerPool(5, delivery.WorkerDeps{
		DB:     db,
		MQ:     mq,
		Config: cfg,
		Engine: engine,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		DeliveryEvents: deliveryEvents,
	})

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	if err := worker.Start(workerCtx); err != nil {
		log.Fatalf("start worker pool: %v", err)
	}

	// 7. Create dispatcher and start trigger consumer
	dispatcher := routing.NewDispatcher(db, mq, cfg)

	triggerEvents, err := mq.Consume(rabbitmq.TriggerQueue, "trigger", false)
	if err != nil {
		log.Fatalf("consume trigger queue: %v", err)
	}

	go func() {
		for {
			select {
			case <-workerCtx.Done():
				return
			case msg, ok := <-triggerEvents:
				if !ok {
					return
				}
				notifID := string(msg.Body)
				log.Printf("dispatcher processing trigger %s", notifID)
				if err := dispatcher.Dispatch(context.Background(), notifID); err != nil {
					log.Printf("dispatch error for %s: %v", notifID, err)
				}
			}
		}
	}()

	// 8. Create ingestion service and handler
	svc := ingestion.NewService(db, mq, cfg)
	h := handler.NewHandler(svc)

	// 9. Setup HTTP router
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	r.Post("/api/v1/notifications", h.Ingest)
	r.Get("/api/v1/notifications", h.List)
	r.Get("/api/v1/notifications/{id}", h.GetStatus)

	// 10. Start HTTP server
	httpSrv := &http.Server{
		Addr:    *httpAddr,
		Handler: r,
	}

	go func() {
		log.Printf("notification server listening on %s", *httpAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	// 11. Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received signal %v, shutting down...", sig)

	// 12. Graceful shutdown
	// Stop worker pool first (wait for inflight deliveries).
	// Worker.Stop signals workers via closeCh without cancelling their context,
	// so in-flight DB operations can complete.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()

	worker.Stop(stopCtx) // signals workers to stop, waits for in-flight
	workerCancel()       // stop trigger consumer after workers are done

	// Shutdown HTTP server
	httpSrv.Shutdown(stopCtx)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
