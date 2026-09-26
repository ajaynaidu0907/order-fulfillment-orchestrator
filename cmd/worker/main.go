// Command worker runs the background job worker for the Order Fulfillment
// Orchestrator: it claims jobs from PostgreSQL and drives the saga.
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/app"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/cache"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/fulfillment"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/inventory"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/orchestrator"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/payment"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Migrations are owned by the api / migrate step; the worker only connects.
	base, err := app.NewBase(ctx, false)
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}
	defer base.Close(context.Background())

	orders := store.NewOrderStore(base.Pool)
	jobs := store.NewJobQueue(base.Pool, base.Cfg.Worker.LockTTL)
	rdbCache := cache.NewCache(base.Redis)
	pay := payment.New(base.Pool, base.Logger, base.Cfg.Sim.PaymentDeclineRate)
	inv := inventory.New(base.Pool, rdbCache, base.Logger)
	ful := fulfillment.New(base.Pool, base.Logger, base.Cfg.Sim.FulfillmentFailureRate)

	orch := orchestrator.New(orchestrator.Deps{
		Orders:      orders,
		Jobs:        jobs,
		Payment:     pay,
		Inventory:   inv,
		Fulfillment: ful,
		Metrics:     base.Metrics,
		Logger:      base.Logger,
	})

	w := worker.New(worker.Deps{
		Jobs:      jobs,
		Processor: orch,
		Cfg:       base.Cfg.Worker,
		Metrics:   base.Metrics,
		Logger:    base.Logger,
	})

	stopMetrics := base.StartMetricsServer()

	base.Logger.Info("worker starting")
	if err := w.Run(ctx); err != nil {
		base.Logger.Error("worker stopped with error", "error", err)
	}

	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = stopMetrics(shCtx)
}
