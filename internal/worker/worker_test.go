package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/config"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
)

// ---- In-memory fakes -----------------------------------------------------

type retryRec struct {
	id    uuid.UUID
	cause string
	runAt time.Time
}

type deadRec struct {
	id    uuid.UUID
	cause string
}

// recordingQueue is an in-memory domain.JobQueue that records the terminal
// action taken on each job and can signal when ReapStale has run.
type recordingQueue struct {
	mu         sync.Mutex
	claimBatch []domain.Job // returned by the next Claim, then cleared
	completed  []uuid.UUID
	retried    []retryRec
	dead       []deadRec
	reapCount  int
	reapReturn int
	reapSignal chan struct{}
}

var _ domain.JobQueue = (*recordingQueue)(nil)

func newRecordingQueue() *recordingQueue {
	return &recordingQueue{reapSignal: make(chan struct{}, 1)}
}

func (q *recordingQueue) Enqueue(context.Context, *domain.Job) error { return nil }

func (q *recordingQueue) Claim(context.Context, string, int) ([]domain.Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.claimBatch
	q.claimBatch = nil
	return out, nil
}

func (q *recordingQueue) Complete(_ context.Context, id uuid.UUID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completed = append(q.completed, id)
	return nil
}

func (q *recordingQueue) Retry(_ context.Context, id uuid.UUID, cause string, runAt time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.retried = append(q.retried, retryRec{id: id, cause: cause, runAt: runAt})
	return nil
}

func (q *recordingQueue) Dead(_ context.Context, id uuid.UUID, cause string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dead = append(q.dead, deadRec{id: id, cause: cause})
	return nil
}

func (q *recordingQueue) ReapStale(context.Context) (int, error) {
	q.mu.Lock()
	q.reapCount++
	n := q.reapReturn
	q.mu.Unlock()
	select {
	case q.reapSignal <- struct{}{}:
	default:
	}
	return n, nil
}

func (q *recordingQueue) counts() (complete, retry, dead int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.completed), len(q.retried), len(q.dead)
}

// fakeProcessor is a configurable Processor.
type fakeProcessor struct {
	mu                  sync.Mutex
	handleErr           error
	handleCalls         int
	compensateCalls     int
	lastCompensateCause string
}

func (p *fakeProcessor) Handle(context.Context, domain.Job) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handleCalls++
	return p.handleErr
}

func (p *fakeProcessor) Compensate(_ context.Context, _ domain.Job, cause string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.compensateCalls++
	p.lastCompensateCause = cause
	return nil
}

func (p *fakeProcessor) comps() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.compensateCalls
}

func newTestWorker(q domain.JobQueue, proc Processor) *Worker {
	return New(Deps{
		Jobs:      q,
		Processor: proc,
		Cfg:       config.WorkerConfig{Concurrency: 1, PollInterval: time.Hour, ClaimBatch: 10, ReapInterval: time.Hour},
		Metrics:   observability.NewMetrics(),
	})
}

// ---- Tests ---------------------------------------------------------------

func TestProcessJob_Success_Completes(t *testing.T) {
	q := newRecordingQueue()
	proc := &fakeProcessor{handleErr: nil}
	w := newTestWorker(q, proc)

	job := domain.Job{ID: uuid.New(), Type: domain.JobReserveInventory, Attempts: 1, MaxAttempts: 5}
	w.processJob(context.Background(), job)

	complete, retry, dead := q.counts()
	if complete != 1 || retry != 0 || dead != 0 {
		t.Fatalf("counts complete=%d retry=%d dead=%d, want 1/0/0", complete, retry, dead)
	}
	if q.completed[0] != job.ID {
		t.Fatalf("completed id = %s, want %s", q.completed[0], job.ID)
	}
	if proc.comps() != 0 {
		t.Fatalf("compensate called %d times, want 0", proc.comps())
	}
}

func TestProcessJob_TransientError_RetriesWithFutureRunAt(t *testing.T) {
	q := newRecordingQueue()
	proc := &fakeProcessor{handleErr: errors.New("transient upstream failure")}
	w := newTestWorker(q, proc)

	job := domain.Job{ID: uuid.New(), Type: domain.JobReserveInventory, Attempts: 1, MaxAttempts: 5}
	before := time.Now()
	w.processJob(context.Background(), job)

	complete, retry, dead := q.counts()
	if complete != 0 || retry != 1 || dead != 0 {
		t.Fatalf("counts complete=%d retry=%d dead=%d, want 0/1/0", complete, retry, dead)
	}
	r := q.retried[0]
	if r.id != job.ID {
		t.Fatalf("retried id = %s, want %s", r.id, job.ID)
	}
	if !r.runAt.After(before) {
		t.Fatalf("retry run_at %v not in the future (before=%v)", r.runAt, before)
	}
	// attempts=1 -> backoff ~2s; assert at least 1.5s of delay was scheduled.
	if r.runAt.Before(before.Add(1500 * time.Millisecond)) {
		t.Fatalf("retry run_at %v earlier than expected backoff", r.runAt)
	}
	if proc.comps() != 0 {
		t.Fatalf("compensate must not run on a retry, ran %d times", proc.comps())
	}
}

func TestProcessJob_MaxAttempts_DeadThenCompensate(t *testing.T) {
	q := newRecordingQueue()
	proc := &fakeProcessor{handleErr: errors.New("still failing")}
	w := newTestWorker(q, proc)

	// Attempts == MaxAttempts -> permanent.
	job := domain.Job{ID: uuid.New(), Type: domain.JobReserveInventory, Attempts: 5, MaxAttempts: 5}
	w.processJob(context.Background(), job)

	complete, retry, dead := q.counts()
	if complete != 0 || retry != 0 || dead != 1 {
		t.Fatalf("counts complete=%d retry=%d dead=%d, want 0/0/1", complete, retry, dead)
	}
	if q.dead[0].id != job.ID {
		t.Fatalf("dead id = %s, want %s", q.dead[0].id, job.ID)
	}
	if proc.comps() != 1 {
		t.Fatalf("compensate called %d times, want 1", proc.comps())
	}
}

func TestProcessJob_PaymentDeclined_IsPermanent(t *testing.T) {
	q := newRecordingQueue()
	// Wrapped sentinel, attempts well below max: still permanent.
	proc := &fakeProcessor{handleErr: fmt.Errorf("authorize_payment: %w", domain.ErrPaymentDeclined)}
	w := newTestWorker(q, proc)

	job := domain.Job{ID: uuid.New(), Type: domain.JobAuthorizePayment, Attempts: 1, MaxAttempts: 5}
	w.processJob(context.Background(), job)

	complete, retry, dead := q.counts()
	if complete != 0 || retry != 0 || dead != 1 {
		t.Fatalf("counts complete=%d retry=%d dead=%d, want 0/0/1 (decline is permanent)", complete, retry, dead)
	}
	if proc.comps() != 1 {
		t.Fatalf("compensate called %d times, want 1", proc.comps())
	}
	if proc.lastCompensateCause == "" {
		t.Fatalf("compensate cause was empty")
	}
}

func TestProcessJob_InsufficientInventory_IsPermanent(t *testing.T) {
	q := newRecordingQueue()
	proc := &fakeProcessor{handleErr: fmt.Errorf("reserve_inventory: %w", domain.ErrInsufficientInventory)}
	w := newTestWorker(q, proc)

	job := domain.Job{ID: uuid.New(), Type: domain.JobReserveInventory, Attempts: 1, MaxAttempts: 5}
	w.processJob(context.Background(), job)

	_, retry, dead := q.counts()
	if retry != 0 || dead != 1 {
		t.Fatalf("counts retry=%d dead=%d, want 0/1 (insufficient inventory is permanent)", retry, dead)
	}
	if proc.comps() != 1 {
		t.Fatalf("compensate called %d times, want 1", proc.comps())
	}
}

func TestBackoffFor(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 2 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{8, 256 * time.Second},
		{9, backoffMax}, // 512s capped to 5m
		{100, backoffMax},
	}
	for _, c := range cases {
		if got := backoffFor(c.attempts); got != c.want {
			t.Errorf("backoffFor(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}
}

func TestRun_ReaperRecoversStaleJobs(t *testing.T) {
	q := newRecordingQueue()
	q.reapReturn = 3
	proc := &fakeProcessor{}
	// Tiny reap interval so the reaper fires quickly; long poll interval so the
	// poll loop stays quiet during the test.
	w := New(Deps{
		Jobs:      q,
		Processor: proc,
		Cfg:       config.WorkerConfig{Concurrency: 1, PollInterval: time.Hour, ClaimBatch: 1, ReapInterval: 5 * time.Millisecond},
		Metrics:   observability.NewMetrics(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case <-q.reapSignal:
		// reaper ran at least once
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("reaper did not run within timeout")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	q.mu.Lock()
	n := q.reapCount
	q.mu.Unlock()
	if n == 0 {
		t.Fatalf("ReapStale was never called")
	}
}

func TestRun_ClaimsAndCompletesJobs(t *testing.T) {
	q := newRecordingQueue()
	id := uuid.New()
	q.claimBatch = []domain.Job{{ID: id, Type: domain.JobReserveInventory, Attempts: 1, MaxAttempts: 5}}
	proc := &fakeProcessor{}
	w := New(Deps{
		Jobs:      q,
		Processor: proc,
		Cfg:       config.WorkerConfig{Concurrency: 2, PollInterval: 5 * time.Millisecond, ClaimBatch: 10, ReapInterval: time.Hour},
		Metrics:   observability.NewMetrics(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	// Wait for the claimed job to be completed.
	deadline := time.After(2 * time.Second)
	for {
		complete, _, _ := q.counts()
		if complete >= 1 {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("claimed job was not completed within timeout")
		case <-time.After(2 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	if q.completed[0] != id {
		t.Fatalf("completed id = %s, want %s", q.completed[0], id)
	}
}
