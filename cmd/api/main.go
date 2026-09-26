// Command api runs the HTTP API server for the Order Fulfillment Orchestrator.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/api"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/app"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/cache"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/fulfillment"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/inventory"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/payment"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The API runs migrations on startup (idempotent) so it also works when run
	// standalone against a fresh database.
	base, err := app.NewBase(ctx, true)
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}
	defer base.Close(context.Background())

	// Wire adapters.
	orders := store.NewOrderStore(base.Pool)
	rdbCache := cache.NewCache(base.Redis)
	limiter := cache.NewRateLimiter(base.Redis)
	pay := payment.New(base.Pool, base.Logger, base.Cfg.Sim.PaymentDeclineRate)
	inv := inventory.New(base.Pool, rdbCache, base.Logger)
	ful := fulfillment.New(base.Pool, base.Logger, base.Cfg.Sim.FulfillmentFailureRate)

	router := api.NewRouter(api.Deps{
		Orders:      orders,
		Inventory:   inv,
		Payment:     pay,
		Fulfillment: ful,
		RateLimiter: limiter,
		Metrics:     base.Metrics,
		Logger:      base.Logger,
		DB:          base.Pool,
		Redis:       base.Redis,
	})

	stopMetrics := base.StartMetricsServer()

	srv := &http.Server{
		Addr:         base.Cfg.HTTP.Addr,
		Handler:      router,
		ReadTimeout:  base.Cfg.HTTP.ReadTimeout,
		WriteTimeout: base.Cfg.HTTP.WriteTimeout,
	}
	go func() {
		base.Logger.Info("api listening", "addr", base.Cfg.HTTP.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			base.Logger.Error("api server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	base.Logger.Info("shutting down api")
	shCtx, cancel := context.WithTimeout(context.Background(), base.Cfg.HTTP.ShutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(shCtx)
	_ = stopMetrics(shCtx)
}
