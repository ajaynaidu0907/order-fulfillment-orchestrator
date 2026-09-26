// Package worker is the polling loop that drives the saga engine. It claims due
// jobs from the PostgreSQL-backed queue, processes them concurrently under a
// bounded semaphore, and applies the retry/backoff and compensation policy. A
// background reaper returns crashed workers' leased jobs to the pending pool.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/config"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
)

const (
	// backoffBase is the first retry delay; each attempt doubles it.
	backoffBase = 2 * time.Second
	// backoffMax caps the exponential backoff.
	backoffMax = 5 * time.Minute
)

// Processor executes a single job. It is satisfied by *orchestrator.Orchestrator.
type Processor interface {
	Handle(ctx context.Context, j domain.Job) error
	Compensate(ctx context.Context, j domain.Job, cause string) error
}

// Deps are the worker's dependencies, matching the constructor contract in
// docs/ARCHITECTURE.md.
type Deps struct {
	Jobs      domain.JobQueue
	Processor Processor
	Cfg       config.WorkerConfig
	Metrics   *observability.Metrics
	Logger    *slog.Logger
}

// Worker claims and processes jobs until its context is cancelled.
type Worker struct {
	jobs     domain.JobQueue
	proc     Processor
	cfg      config.WorkerConfig
	metrics  *observability.Metrics
	logger   *slog.Logger
	workerID string
}

// New builds a Worker from its dependencies, applying safe defaults.
func New(d Deps) *Worker {
	if d.Metrics == nil {
		d.Metrics = observability.NewMetrics()
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Cfg.Concurrency < 1 {
		d.Cfg.Concurrency = 1
	}
	if d.Cfg.PollInterval <= 0 {
		d.Cfg.PollInterval = time.Second
	}
	if d.Cfg.ClaimBatch < 1 {
		d.Cfg.ClaimBatch = 1
	}
	if d.Cfg.ReapInterval <= 0 {
		d.Cfg.ReapInterval = 15 * time.Second
	}
	return &Worker{
		jobs:     d.Jobs,
		proc:     d.Processor,
		cfg:      d.Cfg,
		metrics:  d.Metrics,
		logger:   d.Logger,
		workerID: newWorkerID(),
	}
}

// Run blocks until ctx is cancelled. It starts a reaper goroutine and a poll
// loop; on shutdown it stops both tickers and waits for all in-flight jobs to
// finish before returning nil.
func (w *Worker) Run(ctx context.Context) error {
	w.logger.Info("worker starting",
		"worker_id", w.workerID,
		"concurrency", w.cfg.Concurrency,
		"poll_interval", w.cfg.PollInterval,
		"claim_batch", w.cfg.ClaimBatch,
		"reap_interval", w.cfg.ReapInterval,
	)

	sem := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup

	reapTicker := time.NewTicker(w.cfg.ReapInterval)
	defer reapTicker.Stop()
	pollTicker := time.NewTicker(w.cfg.PollInterval)
	defer pollTicker.Stop()

	// Reaper goroutine: recover jobs whose lease expired (crashed workers).
	var reaperWG sync.WaitGroup
	reaperWG.Add(1)
	go func() {
		defer reaperWG.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-reapTicker.C:
				w.reap(ctx)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("worker draining: waiting for in-flight jobs", "worker_id", w.workerID)
			wg.Wait()
			reaperWG.Wait()
			w.logger.Info("worker stopped", "worker_id", w.workerID)
			return nil
		case <-pollTicker.C:
			w.pollOnce(ctx, sem, &wg)
		}
	}
}

// pollOnce claims a batch of jobs and dispatches each into a bounded goroutine.
func (w *Worker) pollOnce(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	jobs, err := w.jobs.Claim(ctx, w.workerID, w.cfg.ClaimBatch)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down
		}
		w.logger.Warn("claim jobs failed", "worker_id", w.workerID, "error", err)
		return
	}
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			// Stop launching new work; unlaunched claims will be reaped later.
			return
		case sem <- struct{}{}:
			wg.Add(1)
			go func(j domain.Job) {
				defer wg.Done()
				defer func() { <-sem }()
				w.processJob(ctx, j)
			}(job)
		}
	}
}

// processJob runs one job through the processor and applies the completion,
// retry, or dead-letter policy. It is exported to tests via the package.
func (w *Worker) processJob(ctx context.Context, job domain.Job) {
	jobType := string(job.Type)

	w.metrics.JobsInflight.Inc()
	defer w.metrics.JobsInflight.Dec()

	start := time.Now()
	err := w.proc.Handle(ctx, job)
	w.metrics.JobDuration.WithLabelValues(jobType).Observe(time.Since(start).Seconds())

	if err == nil {
		if cerr := w.jobs.Complete(ctx, job.ID); cerr != nil {
			w.logger.Error("complete job failed", "job_id", job.ID, "type", jobType, "error", cerr)
		}
		w.metrics.JobsProcessed.WithLabelValues(jobType, "success").Inc()
		return
	}

	cause := err.Error()
	permanent := errors.Is(err, domain.ErrPaymentDeclined) ||
		errors.Is(err, domain.ErrInsufficientInventory) ||
		job.Attempts >= job.MaxAttempts

	if permanent {
		if derr := w.jobs.Dead(ctx, job.ID, cause); derr != nil {
			w.logger.Error("mark job dead failed", "job_id", job.ID, "type", jobType, "error", derr)
		}
		if cerr := w.proc.Compensate(ctx, job, cause); cerr != nil {
			w.logger.Error("compensate failed", "job_id", job.ID, "type", jobType, "error", cerr)
		}
		w.metrics.JobsProcessed.WithLabelValues(jobType, "dead").Inc()
		w.logger.Warn("job dead-lettered",
			"job_id", job.ID, "type", jobType, "attempts", job.Attempts,
			"max_attempts", job.MaxAttempts, "cause", cause)
		return
	}

	runAt := time.Now().Add(backoffFor(job.Attempts))
	if rerr := w.jobs.Retry(ctx, job.ID, cause, runAt); rerr != nil {
		w.logger.Error("retry job failed", "job_id", job.ID, "type", jobType, "error", rerr)
	}
	w.metrics.JobsProcessed.WithLabelValues(jobType, "retry").Inc()
	w.logger.Warn("job scheduled for retry",
		"job_id", job.ID, "type", jobType, "attempts", job.Attempts, "run_at", runAt, "cause", cause)
}

// reap recovers stale (leased-but-abandoned) jobs and logs when any were found.
func (w *Worker) reap(ctx context.Context) {
	n, err := w.jobs.ReapStale(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.logger.Warn("reap stale jobs failed", "worker_id", w.workerID, "error", err)
		return
	}
	if n > 0 {
		w.logger.Info("reaped stale jobs", "worker_id", w.workerID, "count", n)
	}
}

// backoffFor computes the exponential backoff for the given attempt count:
// min(base * 2^(attempts-1), max). attempts is the value after Claim increments
// it, so the first failure (attempts=1) waits base.
func backoffFor(attempts int) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 30 { // guard against int64 overflow on the shift
		return backoffMax
	}
	d := backoffBase << uint(shift)
	if d <= 0 || d > backoffMax {
		return backoffMax
	}
	return d
}

// newWorkerID builds a stable per-process identifier: hostname-pid.
func newWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}
