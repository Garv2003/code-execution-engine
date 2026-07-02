package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/garv2003/code-execution-engine/internal/models"
	"github.com/garv2003/code-execution-engine/internal/pushsub"
	"github.com/garv2003/code-execution-engine/internal/sandbox"
)

type WorkerPool struct {
	redisClient *pushsub.RedisClient
	sandbox     *sandbox.DockerSandbox
	maxWorkers  int
	wg          sync.WaitGroup
	shutdown    chan struct{}
}

func NewWorkerPool(rc *pushsub.RedisClient, sb *sandbox.DockerSandbox, maxWorkers int) *WorkerPool {
	return &WorkerPool{
		redisClient: rc,
		sandbox:     sb,
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

			runCtx, runCancel := context.WithTimeout(context.Background(), job.Timeout)
			result, err := wp.sandbox.Run(runCtx, job)
			runCancel()

			if err != nil {
				slog.Error("Failed execution run", "job_id", job.ID, "error", err)
				result = &models.ExecutionResult{
					ID:       job.ID,
					ExitCode: -1,
					Stderr:   "Internal Sandbox Error: " + err.Error(),
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
