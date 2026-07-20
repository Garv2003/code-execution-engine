package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/garv2003/code-execution-engine/internal/db"
	"github.com/garv2003/code-execution-engine/internal/metrics"
	"github.com/garv2003/code-execution-engine/internal/models"
	"github.com/garv2003/code-execution-engine/internal/pushsub"
	"github.com/garv2003/code-execution-engine/internal/sandbox"
)

type WorkerPool struct {
	redisClient *pushsub.RedisClient
	sandbox     sandbox.Sandbox
	pgDB        *db.PostgresDB
	maxWorkers  int
	wg          sync.WaitGroup
	shutdown    chan struct{}
}

func NewWorkerPool(rc *pushsub.RedisClient, sb sandbox.Sandbox, pgDB *db.PostgresDB, maxWorkers int) *WorkerPool {
	return &WorkerPool{
		redisClient: rc,
		sandbox:     sb,
		pgDB:        pgDB,
		maxWorkers:  maxWorkers,
		shutdown:    make(chan struct{}),
	}
}

func (wp *WorkerPool) Start() {
	slog.Info("Starting worker pool", "workers_count", wp.maxWorkers)
	for i := 0; i < wp.maxWorkers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
	wp.wg.Add(1)
	go wp.reportQueueDepth()
}

func (wp *WorkerPool) reportQueueDepth() {
	defer wp.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-wp.shutdown:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			depth, err := wp.redisClient.JobQueueLength(ctx)
			cancel()
			if err != nil {
				slog.Debug("Failed to read job queue depth", "error", err)
				continue
			}
			metrics.QueueDepth.Set(float64(depth))
		}
	}
}

func (wp *WorkerPool) worker(workerID int) {
	defer wp.wg.Done()
	slog.Debug("Worker started", "worker_id", workerID)

	for {
		select {
		case <-wp.shutdown:
			slog.Debug("Worker shutting down", "worker_id", workerID)
			return
		default:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			job, err := wp.redisClient.PopJob(ctx)
			cancel()

			if err != nil {
				continue
			}

			slog.Info("Worker picked up job", "worker_id", workerID, "job_id", job.ID)

			startedAt := time.Now().UTC()
			if record, found, err := wp.redisClient.GetJobRecord(context.Background(), job.ID); err == nil && found {
				record.Status = models.JobStatusRunning
				record.StartedAt = &startedAt
				_ = wp.redisClient.UpdateJobRecord(context.Background(), record)
				if wp.pgDB != nil {
					_ = wp.pgDB.UpsertJobRecord(context.Background(), record)
				}
			}

			runCtx, runCancel := context.WithTimeout(context.Background(), job.Timeout)
			runStart := time.Now()
			result, err := wp.sandbox.Run(runCtx, job)
			metrics.JobExecutionSeconds.Observe(time.Since(runStart).Seconds())
			runCancel()

			finishedAt := time.Now().UTC()
			record, found, recordErr := wp.redisClient.GetJobRecord(context.Background(), job.ID)
			if recordErr == nil && found {
				record.FinishedAt = &finishedAt
			}

			if err != nil {
				slog.Error("Failed execution run", "job_id", job.ID, "error", err)
				metrics.JobsTotal.WithLabelValues(metrics.OutcomeFailed).Inc()
				result = &models.ExecutionResult{
					ID:       job.ID,
					ExitCode: -1,
					Stderr:   "Internal Sandbox Error: " + err.Error(),
				}
				if found {
					record.Status = models.JobStatusFailed
					record.Error = err.Error()
					record.Result = result
					_ = wp.redisClient.UpdateJobRecord(context.Background(), record)
					if wp.pgDB != nil {
						_ = wp.pgDB.UpsertJobRecord(context.Background(), record)
					}
				}
			} else if result.Timeout || result.OOM {
				if result.Timeout {
					metrics.JobsTotal.WithLabelValues(metrics.OutcomeTimeout).Inc()
				} else {
					metrics.JobsTotal.WithLabelValues(metrics.OutcomeOOM).Inc()
				}
				if found {
					record.Status = models.JobStatusFailed
					if result.Timeout {
						record.Error = "execution timed out"
					} else {
						record.Error = "out of memory"
					}
					record.Result = result
					_ = wp.redisClient.UpdateJobRecord(context.Background(), record)
					if wp.pgDB != nil {
						_ = wp.pgDB.UpsertJobRecord(context.Background(), record)
					}
				}
			} else {
				metrics.JobsTotal.WithLabelValues(metrics.OutcomeCompleted).Inc()
				if found {
					record.Status = models.JobStatusCompleted
					if result.ExitCode != 0 {
						record.Error = fmt.Sprintf("non-zero exit code: %d", result.ExitCode)
					}
					record.Result = result
					_ = wp.redisClient.UpdateJobRecord(context.Background(), record)
					if wp.pgDB != nil {
						_ = wp.pgDB.UpsertJobRecord(context.Background(), record)
					}
				}
			}

			pubCtx, pubCancel := context.WithTimeout(context.Background(), 3*time.Second)
			err = wp.redisClient.PublishResult(pubCtx, job.ID, result)
			pubCancel()

			if err != nil {
				slog.Error("Failed publishing result to Redis", "job_id", job.ID, "error", err)
			} else {
				slog.Info("Job processed successfully", "worker_id", workerID, "job_id", job.ID)
			}
		}
	}
}

func (wp *WorkerPool) Stop(ctx context.Context) error {
	slog.Info("Gracefully stopping worker pool...")
	close(wp.shutdown)

	done := make(chan struct{})
	go func() {
		wp.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("Worker pool stopped.")
		return nil
	case <-ctx.Done():
		slog.Warn("Worker pool shutdown timed out", "error", ctx.Err())
		return ctx.Err()
	}
}
