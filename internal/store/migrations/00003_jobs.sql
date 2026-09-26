-- +goose Up
-- +goose StatementBegin
CREATE TABLE jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type            TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    max_attempts    INT NOT NULL DEFAULT 5,
    run_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_until    TIMESTAMPTZ,
    last_error      TEXT,
    order_id        UUID REFERENCES orders (id) ON DELETE CASCADE,
    idempotency_key TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

-- Partial index that powers the FOR UPDATE SKIP LOCKED claim query: only
-- pending jobs are indexed, ordered by when they may run and their age.
CREATE INDEX idx_jobs_claim ON jobs (run_at, created_at) WHERE status = 'pending';

CREATE INDEX idx_jobs_order ON jobs (order_id);
CREATE INDEX idx_jobs_status ON jobs (status);

-- Prevent duplicate in-flight jobs sharing an idempotency key (e.g. two
-- reserve_inventory jobs for the same order). Completed/dead jobs are excluded
-- so the same key can legitimately be reused later if ever needed.
CREATE UNIQUE INDEX uq_jobs_active_idem ON jobs (idempotency_key)
    WHERE status IN ('pending', 'running') AND idempotency_key IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS jobs;
-- +goose StatementEnd
