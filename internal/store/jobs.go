package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// JobQueue is the PostgreSQL implementation of domain.JobQueue. Claims are made
// with FOR UPDATE SKIP LOCKED so many workers can poll the same table without
// contending on the same rows.
type JobQueue struct {
	pool    *pgxpool.Pool
	lockTTL time.Duration
}

// NewJobQueue builds a JobQueue. lockTTL is how long a claimed job stays leased
// before ReapStale may return it to the pending pool.
func NewJobQueue(pool *pgxpool.Pool, lockTTL time.Duration) *JobQueue {
	if lockTTL <= 0 {
		lockTTL = 30 * time.Second
	}
	return &JobQueue{pool: pool, lockTTL: lockTTL}
}

var _ domain.JobQueue = (*JobQueue)(nil)

// Enqueue inserts a standalone job.
func (q *JobQueue) Enqueue(ctx context.Context, j *domain.Job) error {
	return insertJob(ctx, q.pool, j)
}

// Claim atomically leases up to batch pending, due jobs to workerID. The CTE
// selects candidate rows with FOR UPDATE SKIP LOCKED and the surrounding UPDATE
// flips them to running, increments the attempt counter, and sets a lock lease.
func (q *JobQueue) Claim(ctx context.Context, workerID string, batch int) ([]domain.Job, error) {
	if batch <= 0 {
		batch = 1
	}
	rows, err := q.pool.Query(ctx, `
		WITH claimed AS (
			SELECT id
			FROM jobs
			WHERE status = 'pending' AND run_at <= now()
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE jobs j
		SET status = 'running',
		    attempts = attempts + 1,
		    locked_until = now() + make_interval(secs => $2),
		    updated_at = now()
		FROM claimed
		WHERE j.id = claimed.id
		RETURNING j.id, j.type, j.payload, j.status, j.attempts, j.max_attempts,
		          j.run_at, j.locked_until, COALESCE(j.last_error, ''), j.order_id,
		          COALESCE(j.idempotency_key, ''), j.created_at, j.updated_at, j.completed_at`,
		batch, q.lockTTL.Seconds(),
	)
	if err != nil {
		return nil, fmt.Errorf("claim jobs: %w", err)
	}
	defer rows.Close()

	var out []domain.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Complete marks a job completed.
func (q *JobQueue) Complete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'completed', completed_at = now(), locked_until = NULL, updated_at = now()
		WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	return nil
}

// Retry reschedules a job to run again at runAt, recording the cause.
func (q *JobQueue) Retry(ctx context.Context, id uuid.UUID, cause string, runAt time.Time) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'pending', run_at = $2, last_error = $3, locked_until = NULL, updated_at = now()
		WHERE id = $1`, id, runAt, cause)
	if err != nil {
		return fmt.Errorf("retry job: %w", err)
	}
	return nil
}

// Dead marks a job permanently failed.
func (q *JobQueue) Dead(ctx context.Context, id uuid.UUID, cause string) error {
	_, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'dead', last_error = $2, locked_until = NULL, updated_at = now()
		WHERE id = $1`, id, cause)
	if err != nil {
		return fmt.Errorf("mark job dead: %w", err)
	}
	return nil
}

// ReapStale returns running jobs whose lock lease has expired back to pending
// (e.g. after a worker crash) and reports how many were recovered.
func (q *JobQueue) ReapStale(ctx context.Context) (int, error) {
	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'pending', locked_until = NULL, updated_at = now()
		WHERE status = 'running' AND locked_until < now()`)
	if err != nil {
		return 0, fmt.Errorf("reap stale jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func scanJob(row pgx.Rows) (domain.Job, error) {
	var j domain.Job
	if err := row.Scan(
		&j.ID, &j.Type, &j.Payload, &j.Status, &j.Attempts, &j.MaxAttempts,
		&j.RunAt, &j.LockedUntil, &j.LastError, &j.OrderID,
		&j.IdempotencyKey, &j.CreatedAt, &j.UpdatedAt, &j.CompletedAt,
	); err != nil {
		return domain.Job{}, err
	}
	return j, nil
}
