package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/garv2003/code-execution-engine/internal/models"
	_ "github.com/lib/pq"
)

type PostgresDB struct {
	db *sql.DB
}

func NewPostgresDB(databaseURL string) (*PostgresDB, error) {
	if databaseURL == "" {
		return nil, nil // Postgres not configured
	}

	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	// Create table if not exists
	schema := `
	CREATE TABLE IF NOT EXISTS jobs (
		id VARCHAR(36) PRIMARY KEY,
		language VARCHAR(50) NOT NULL,
		status VARCHAR(20) NOT NULL,
		timeout_ms BIGINT NOT NULL,
		memory_limit_mb BIGINT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		started_at TIMESTAMP,
		finished_at TIMESTAMP,
		result JSONB,
		error TEXT
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	slog.Info("Connected to PostgreSQL database")
	return &PostgresDB{db: db}, nil
}

func (p *PostgresDB) UpsertJobRecord(ctx context.Context, record *models.JobRecord) error {
	resultJSON, _ := json.Marshal(record.Result)
	if record.Result == nil {
		resultJSON = nil
	}

	query := `
	INSERT INTO jobs (id, language, status, timeout_ms, memory_limit_mb, created_at, started_at, finished_at, result, error)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	ON CONFLICT (id) DO UPDATE SET
		status = EXCLUDED.status,
		started_at = EXCLUDED.started_at,
		finished_at = EXCLUDED.finished_at,
		result = EXCLUDED.result,
		error = EXCLUDED.error;
	`

	_, err := p.db.ExecContext(ctx, query,
		record.ID, record.Language, record.Status, record.TimeoutMS, record.MemoryLimitMB,
		record.CreatedAt, record.StartedAt, record.FinishedAt, resultJSON, record.Error,
	)
	return err
}

func (p *PostgresDB) GetRecentJobs(ctx context.Context, limit int) ([]models.JobRecord, error) {
	query := `
	SELECT id, language, status, timeout_ms, memory_limit_mb, created_at, started_at, finished_at, result, error
	FROM jobs
	ORDER BY created_at DESC
	LIMIT $1
	`
	rows, err := p.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []models.JobRecord
	for rows.Next() {
		var job models.JobRecord
		var resultData []byte
		var errorStr sql.NullString
		var startedAt, finishedAt sql.NullTime

		err := rows.Scan(
			&job.ID, &job.Language, &job.Status, &job.TimeoutMS, &job.MemoryLimitMB,
			&job.CreatedAt, &startedAt, &finishedAt, &resultData, &errorStr,
		)
		if err != nil {
			return nil, err
		}

		if startedAt.Valid {
			job.StartedAt = &startedAt.Time
		}
		if finishedAt.Valid {
			job.FinishedAt = &finishedAt.Time
		}
		if errorStr.Valid {
			job.Error = errorStr.String
		}
		if len(resultData) > 0 {
			var res models.ExecutionResult
			if err := json.Unmarshal(resultData, &res); err == nil {
				job.Result = &res
			}
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}
