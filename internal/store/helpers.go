package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
)

// execer is satisfied by both *pgxpool.Pool and pgx.Tx, so helpers can run
// either standalone or inside a caller's transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// insertJob inserts a job, applying defaults. It maps a unique-violation on the
// active-idempotency index to domain.ErrConflict so retried enqueues are safe.
func insertJob(ctx context.Context, q execer, j *domain.Job) error {
	if j.ID == uuid.Nil {
		j.ID = uuid.New()
	}
	if j.Status == "" {
		j.Status = domain.JobPending
	}
	if j.MaxAttempts == 0 {
		j.MaxAttempts = domain.DefaultMaxAttempts
	}
	if j.RunAt.IsZero() {
		j.RunAt = time.Now()
	}
	if len(j.Payload) == 0 {
		j.Payload = []byte("{}")
	}
	err := q.QueryRow(ctx, `
		INSERT INTO jobs (id, type, payload, status, attempts, max_attempts, run_at, order_id, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''))
		RETURNING created_at, updated_at`,
		j.ID, j.Type, j.Payload, j.Status, j.Attempts, j.MaxAttempts, j.RunAt, j.OrderID, j.IdempotencyKey,
	).Scan(&j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: duplicate active job %q", domain.ErrConflict, j.IdempotencyKey)
		}
		return fmt.Errorf("insert job: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// nz returns m, or an empty map when m is nil, so JSONB columns never receive
// a SQL null where an object is expected.
func nz(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
