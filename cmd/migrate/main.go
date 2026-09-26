// Command migrate applies all pending database migrations and exits. It runs as
// a one-shot step in Docker Compose before the api and worker start.
package main

import (
	"log"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/config"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := store.Migrate(cfg.DB.URL()); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("migrations applied successfully")
}
