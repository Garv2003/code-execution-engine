package pushsub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/garv2003/code-execution-engine/internal/models"
	"github.com/redis/go-redis/v9"
)

type RedisClient struct {
	client *redis.Client
}

const resultTTL = 15 * time.Minute
const jobTTL = 24 * time.Hour

func NewRedisClient(redisURL string) (*RedisClient, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisClient{client: rdb}, nil
}

func (r *RedisClient) PushJob(ctx context.Context, job *models.Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return r.client.LPush(ctx, "cee:job_queue", data).Err()
}

func (r *RedisClient) PopJob(ctx context.Context) (*models.Job, error) {
	results, err := r.client.BRPop(ctx, 0, "cee:job_queue").Result()
	if err != nil {
		return nil, err
	}

	jobJSON := results[1]
	var job models.Job
	if err := json.Unmarshal([]byte(jobJSON), &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *RedisClient) PublishResult(ctx context.Context, jobID string, result *models.ExecutionResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	resultKey := fmt.Sprintf("cee:result:%s", jobID)
	if err := r.client.Set(ctx, resultKey, data, resultTTL).Err(); err != nil {
		return err
	}
	channel := fmt.Sprintf("result:%s", jobID)
	return r.client.Publish(ctx, channel, data).Err()
}

func (r *RedisClient) StoreJobRecord(ctx context.Context, record *models.JobRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	jobKey := fmt.Sprintf("cee:job:%s", record.ID)
	return r.client.Set(ctx, jobKey, data, jobTTL).Err()
}

func (r *RedisClient) UpdateJobRecord(ctx context.Context, record *models.JobRecord) error {
	return r.StoreJobRecord(ctx, record)
}

func (r *RedisClient) SubscribeResult(ctx context.Context, jobID string) *redis.PubSub {
	channel := fmt.Sprintf("result:%s", jobID)
	return r.client.Subscribe(ctx, channel)
}

func (r *RedisClient) GetResult(ctx context.Context, jobID string) (string, bool, error) {
	resultKey := fmt.Sprintf("cee:result:%s", jobID)
	data, err := r.client.Get(ctx, resultKey).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return data, true, nil
}

func (r *RedisClient) GetJobRecord(ctx context.Context, jobID string) (*models.JobRecord, bool, error) {
	jobKey := fmt.Sprintf("cee:job:%s", jobID)
	data, err := r.client.Get(ctx, jobKey).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	var record models.JobRecord
	if err := json.Unmarshal([]byte(data), &record); err != nil {
		return nil, false, err
	}
	return &record, true, nil
}
