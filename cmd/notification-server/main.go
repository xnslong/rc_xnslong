package main

import (
	"context"
	"flag"
	"fmt"
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

const (
	routeHealthz = "/healthz"
	routeAPIBase = "/api/v1/notifications"
	routeAPIByID = "/api/v1/notifications/{id}"
)

const (
	consumerWorker  = "worker"
	consumerTrigger = "trigger"
)

const (
	httpClientTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
	workerConcurrency = 5
)

type appDeps struct {
	mq         *rabbitmq.Client
	worker     *delivery.WorkerPool
	dispatcher *routing.Dispatcher
	httpSrv    *http.Server
}

func main() {
	configDir := flag.String("config-dir", envOrDefault("CONFIG_DIR", "testdata"), "config directory")
	httpAddr := flag.String("http-addr", envOrDefault("HTTP_ADDR", ":8080"), "HTTP listen address")
	pgURL := flag.String("pg-url", envOrDefault("PG_URL", "postgres://notify:notify@localhost:5432/notification?sslmode=disable"), "PostgreSQL URL")
	mqURL := flag.String("mq-url", envOrDefault("MQ_URL", "amqp://notify:notify@localhost:5672/"), "RabbitMQ URL")
	flag.Parse()

	db, mqClient, cfg, err := initInfra(*pgURL, *mqURL, *configDir)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	defer db.Close()

	deps, err := initServices(db, mqClient, cfg)
	if err != nil {
		log.Fatalf("init services: %v", err)
	}
	deps.httpSrv.Addr = *httpAddr

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	if err := deps.worker.Start(workerCtx); err != nil {
		log.Fatalf("start worker pool: %v", err)
	}

	go runTriggerConsumer(workerCtx, deps.dispatcher, deps.mq)

	go func() {
		log.Printf("notification server listening on %s", *httpAddr)
		if err := deps.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received signal %v, shutting down...", sig)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer stopCancel()

	deps.worker.Stop(stopCtx)
	workerCancel()
	deps.httpSrv.Shutdown(stopCtx)
}

func initInfra(pgURL, mqURL, configDir string) (*postgres.Client, *rabbitmq.Client, *config.Loader, error) {
	db, err := postgres.NewClient(pgURL)
	if err != nil {
		return nil, nil, nil, fatalErr("create db client", err)
	}

	mqClient, err := rabbitmq.NewClient(mqURL)
	if err != nil {
		db.Close()
		return nil, nil, nil, fatalErr("create mq client", err)
	}

	cfg, err := config.NewLoader(configDir)
	if err != nil {
		mqClient.Close()
		db.Close()
		return nil, nil, nil, fatalErr("create config loader", err)
	}
	if err := cfg.Load(context.Background()); err != nil {
		mqClient.Close()
		db.Close()
		return nil, nil, nil, fatalErr("load config", err)
	}

	return db, mqClient, cfg, nil
}

func initServices(db *postgres.Client, mqClient *rabbitmq.Client, cfg *config.Loader) (*appDeps, error) {
	engine := mapping.NewEngine()

	deliveryEvents, err := mqClient.Consume(rabbitmq.DeliveryQueue, consumerWorker, false)
	if err != nil {
		return nil, fatalErr("consume delivery queue", err)
	}

	worker := delivery.NewWorkerPool(workerConcurrency, delivery.WorkerDeps{
		DB:     db,
		MQ:     mqClient,
		Config: cfg,
		Engine: engine,
		HTTPClient: &http.Client{
			Timeout: httpClientTimeout,
		},
		DeliveryEvents: deliveryEvents,
	})

	dispatcher := routing.NewDispatcher(db, mqClient, cfg)
	svc := ingestion.NewService(db, mqClient, cfg)
	h := handler.NewHandler(svc)

	return &appDeps{
		mq:         mqClient,
		worker:     worker,
		dispatcher: dispatcher,
		httpSrv:    &http.Server{Handler: buildRouter(h)},
	}, nil
}

func buildRouter(h *handler.Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get(routeHealthz, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	r.Post(routeAPIBase, h.Ingest)
	r.Get(routeAPIBase, h.List)
	r.Get(routeAPIByID, h.GetStatus)
	return r
}

func runTriggerConsumer(ctx context.Context, d *routing.Dispatcher, mqClient *rabbitmq.Client) {
	triggerEvents, err := mqClient.Consume(rabbitmq.TriggerQueue, consumerTrigger, false)
	if err != nil {
		log.Fatalf("consume trigger queue: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-triggerEvents:
			if !ok {
				return
			}
			notifID := string(msg.Body)
			log.Printf("dispatcher processing trigger %s", notifID)
			if err := d.Dispatch(context.Background(), notifID); err != nil {
				log.Printf("dispatch error for %s: %v", notifID, err)
			}
		}
	}
}

func fatalErr(msg string, err error) error {
	return fmt.Errorf("%s: %w", msg, err)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
