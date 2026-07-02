package models

import "time"

type JobStatus string

const (
	JobStatusQueued    JobStatus = "queued"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
)

type Job struct {
	ID            string            `json:"id"`
	Language      string            `json:"language"`
	Code          string            `json:"code,omitempty"`
	Files         map[string]string `json:"files,omitempty"`
	Stdin         string            `json:"stdin"`
	Timeout       time.Duration     `json:"timeout"`
	MemoryLimitMB int64             `json:"memory_limit_mb"`
}

type JobRecord struct {
	ID            string           `json:"id"`
	Language      string           `json:"language"`
	Status        JobStatus        `json:"status"`
	TimeoutMS     int64            `json:"timeout_ms"`
	MemoryLimitMB int64            `json:"memory_limit_mb"`
	CreatedAt     time.Time        `json:"created_at"`
	StartedAt     *time.Time       `json:"started_at,omitempty"`
	FinishedAt    *time.Time       `json:"finished_at,omitempty"`
	Result        *ExecutionResult `json:"result,omitempty"`
	Error         string           `json:"error,omitempty"`
}
