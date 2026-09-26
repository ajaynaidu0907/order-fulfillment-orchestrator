package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// JobStatus is the lifecycle state of a background job.
type JobStatus string

const (
	JobPending   JobStatus = "pending"   // ready to be claimed once run_at has passed
	JobRunning   JobStatus = "running"   // claimed by a worker; protected by locked_until
	JobCompleted JobStatus = "completed" // finished successfully
	JobFailed    JobStatus = "failed"    // failed but may be retried (see attempts)
	JobDead      JobStatus = "dead"      // exhausted retries; needs operator attention
)

// JobType identifies the operation a worker performs for a job. The forward
// steps drive the happy path; the compensation types unwind partial work.
type JobType string

const (
	JobReserveInventory   JobType = "reserve_inventory"
	JobAuthorizePayment   JobType = "authorize_payment"
	JobPrepareFulfillment JobType = "prepare_fulfillment"
	JobShipOrder          JobType = "ship_order"
	JobConfirmDelivery    JobType = "confirm_delivery"

	// Compensation jobs.
	JobReleaseInventory JobType = "release_inventory"
	JobRefundPayment    JobType = "refund_payment"
)

// Job is a unit of asynchronous work stored in the PostgreSQL jobs table. Jobs
// are inserted in the same transaction as the state change that schedules them
// (the transactional outbox pattern), so work is never lost or double-scheduled.
type Job struct {
	ID          uuid.UUID       `json:"id"`
	Type        JobType         `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	Status      JobStatus       `json:"status"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	RunAt       time.Time       `json:"run_at"`
	LockedUntil *time.Time      `json:"locked_until,omitempty"`
	LastError   string          `json:"last_error,omitempty"`
	OrderID     *uuid.UUID      `json:"order_id,omitempty"`
	// IdempotencyKey lets the underlying operation de-duplicate retried work.
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// DefaultMaxAttempts is applied when a job is enqueued without an explicit cap.
const DefaultMaxAttempts = 5

// JobPayload is the standard payload carried by workflow jobs.
type JobPayload struct {
	OrderID uuid.UUID `json:"order_id"`
	// Reason is populated on compensation jobs to explain why they were scheduled.
	Reason string `json:"reason,omitempty"`
}

// NewOrderJob builds a workflow job for an order with a JobPayload body.
func NewOrderJob(t JobType, orderID uuid.UUID, runAt time.Time) (Job, error) {
	body, err := json.Marshal(JobPayload{OrderID: orderID})
	if err != nil {
		return Job{}, err
	}
	oid := orderID
	return Job{
		ID:             uuid.New(),
		Type:           t,
		Payload:        body,
		Status:         JobPending,
		MaxAttempts:    DefaultMaxAttempts,
		RunAt:          runAt,
		OrderID:        &oid,
		IdempotencyKey: string(t) + ":" + orderID.String(),
	}, nil
}
