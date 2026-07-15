package models

import "time"

type ExecutionResult struct {
	ID         string        `json:"id"`
	Stdout     string        `json:"stdout"`
	Stderr     string        `json:"stderr"`
	ExitCode   int           `json:"exit_code"`
	TimeUsed   time.Duration `json:"time_used"`
	MemoryUsed int64         `json:"memory_used_bytes"`
	Timeout         bool `json:"timeout"`
	OOM             bool `json:"oom"`
	OutputTruncated bool `json:"output_truncated"`
}
