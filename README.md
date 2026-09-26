# Order Fulfillment Orchestrator

A production-grade, containerized **order fulfillment orchestration** service written in Go.
It drives an order through its lifecycle — reserving inventory, authorizing payment, shipping,
and delivering — using a **saga pattern** with automatic **compensation** (rollback) when a
step fails. Asynchronous work runs on a **PostgreSQL-backed job queue** (no Kafka required),
claimed safely with `FOR UPDATE SKIP LOCKED`.

> Built as a showcase of production patterns: transactional outbox, idempotent workers,
> optimistic concurrency, distributed tracing, metrics, and containerized deployment.

---

## Architecture

```
                 ┌───────────────┐        ┌──────────────────────────────┐
   HTTP client ─▶│   API (chi)   │        │        Background Worker      │
                 │  cmd/api      │        │        cmd/worker             │
                 └──────┬────────┘        └───────────────┬──────────────┘
                        │ order tx + job (same tx)         │ claim job
                        ▼                                  ▼  FOR UPDATE SKIP LOCKED
                 ┌─────────────────────────────────────────────────────────┐
                 │                     PostgreSQL                            │
                 │   orders · order_items · order_events · payments          │
                 │   inventory · inventory_reservations · shipments · jobs   │
                 └─────────────────────────────────────────────────────────┘
                        ▲                                  │
                        │ cache / locks / rate limit       │ payment · inventory · fulfillment
                        ▼                                  ▼   (idempotent operations)
                 ┌───────────────┐                 ┌──────────────────────────┐
                 │     Redis     │                 │  Retry · Complete ·       │
                 └───────────────┘                 │  Compensate (saga)        │
                                                   └──────────────────────────┘

   Traces ─▶ Jaeger (:16686)      Metrics ─▶ Prometheus (:9091)
```

### Why a PostgreSQL job queue instead of Kafka?

The order transaction and the job that continues its workflow are written in the **same database
transaction** (the *transactional outbox* pattern). Work can never be lost or double-scheduled,
and there is no second system to keep consistent. Workers claim work with:

```sql
SELECT * FROM jobs
WHERE status = 'pending' AND run_at <= now()
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT 1;
```

`SKIP LOCKED` lets many workers poll the same table concurrently without blocking each other.

---

## Order workflow

```
CREATED
  → INVENTORY_RESERVED
  → PAYMENT_AUTHORIZED
  → READY_FOR_FULFILLMENT
  → SHIPPED
  → DELIVERED
```

Each transition is driven by a job. On success the worker advances the state and enqueues the
next job **atomically**. On failure it retries with exponential backoff, and once retries are
exhausted it runs the appropriate **compensations**:

| Failure                | Compensation                              |
| ---------------------- | ----------------------------------------- |
| Payment failure        | Release inventory                         |
| Fulfillment failure    | Refund payment **and** release inventory  |
| Cancellation           | Compensate based on the current state     |

Terminal states are `DELIVERED`, `CANCELLED`, and `FAILED`.

---

## Tech stack

| Concern            | Choice                                             |
| ------------------ | -------------------------------------------------- |
| Language / router  | Go + [chi](https://github.com/go-chi/chi)          |
| System of record   | PostgreSQL (via `pgx`)                             |
| Cache/locks/limits | Redis (`go-redis`)                                 |
| Async work         | PostgreSQL `jobs` table + Go worker                |
| Migrations         | `goose` (embedded SQL)                             |
| Tracing            | OpenTelemetry → Jaeger                             |
| Metrics            | Prometheus (`client_golang`)                       |
| Packaging          | Docker (distroless) + Docker Compose               |
| Integration tests  | Testcontainers                                     |

---

## Quick start

Prerequisites: Docker Desktop running.

```bash
cp env.example .env          # synthetic dev config
make run                     # build + start the full stack
make seed                    # load synthetic mock data
make logs                    # watch it work
```

Endpoints:

- API — http://localhost:8080
- Metrics (API) — http://localhost:9090/metrics
- Jaeger UI — http://localhost:16686
- Prometheus — http://localhost:9091

Stop and clean up:

```bash
make stop      # stop, keep data
make clean     # stop and delete volumes
```

---

## Project layout

```
cmd/
  api/       HTTP API server
  worker/    background job worker
  seed/      synthetic mock-data loader
  migrate/   one-shot migration runner
internal/
  domain/         core types, state machine, and interfaces (ports)
  config/         environment configuration
  platform/       Postgres pool, Redis client, logger
  store/          PostgreSQL adapters (orders, jobs) + migrations
  observability/  OpenTelemetry tracing + Prometheus metrics
  app/            shared bootstrap for all binaries
  api/            chi handlers, routing, middleware
  orchestrator/   saga engine + state transitions
  worker/         job dispatch loop
  payment/        payment service (mock provider)
  inventory/      inventory service
  fulfillment/    shipping/fulfillment service
  cache/          Redis cache, locks, rate limiting
  mockdata/       synthetic data generators
deploy/
  prometheus/     Prometheus scrape config
test/
  e2e/            end-to-end + integration tests (Testcontainers)
```

---

## Development

```bash
make build             # compile everything
make test              # unit tests (no Docker)
make test-integration  # integration/e2e tests (needs Docker)
make vet               # go vet
```

---

## Security & data notes

- All data in this repo is **synthetic** — realistic-looking but not real people or organizations.
- Credentials in `env.example` / `docker-compose.yml` are **dev-only placeholders**. Never commit
  real secrets; use a secrets manager in any real deployment.
- Containers run as a non-root user on a distroless base image.
