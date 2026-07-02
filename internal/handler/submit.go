package handler

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/garv2003/code-execution-engine/internal/db"
	"github.com/garv2003/code-execution-engine/internal/models"
	"github.com/garv2003/code-execution-engine/internal/pushsub"
	"github.com/garv2003/code-execution-engine/internal/sandbox"
)

type SubmitHandler struct {
	redisClient *pushsub.RedisClient
	pgDB        *db.PostgresDB
	languages   map[string]sandbox.LanguageSpec
}

func NewSubmitHandler(rc *pushsub.RedisClient, pgDB *db.PostgresDB, languages map[string]sandbox.LanguageSpec) SubmitHandler {
	return SubmitHandler{
		redisClient: rc,
		pgDB:        pgDB,
		languages:   languages,
	}
}

type submitRequest struct {
	Language  string            `json:"language"`
	Code      string            `json:"code"`
	Files     map[string]string `json:"files"`
	Stdin     string            `json:"stdin"`
	TimeoutMS int               `json:"timeout_ms"`
}

func (sh *SubmitHandler) Submit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid request payload"}`))
		return
	}

	if req.Language == "" || (req.Code == "" && len(req.Files) == 0) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"language and code or files are required fields"}`))
		return
	}

	spec, exists := sh.languages[req.Language]
	if !exists {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Unsupported language"}`))
		return
	}

	timeout := 5 * time.Second
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	if spec.TimeoutMS > 0 {
		maxTimeout := time.Duration(spec.TimeoutMS) * time.Millisecond
		if timeout == 0 || timeout > maxTimeout {
			timeout = maxTimeout
		}
	}

	jobID := generateUUID()
	job := &models.Job{
		ID:            jobID,
		Language:      req.Language,
		Code:          req.Code,
		Files:         req.Files,
		Stdin:         req.Stdin,
		Timeout:       timeout,
		MemoryLimitMB: spec.MemoryMB,
	}

	record := &models.JobRecord{
		ID:            jobID,
		Language:      req.Language,
		Status:        models.JobStatusQueued,
		TimeoutMS:     int64(timeout / time.Millisecond),
		MemoryLimitMB: spec.MemoryMB,
		CreatedAt:     time.Now().UTC(),
	}

	if err := sh.redisClient.StoreJobRecord(r.Context(), record); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `{"error":"Failed to store job: %s"}`, err.Error())
		return
	}

	if sh.pgDB != nil {
		if err := sh.pgDB.UpsertJobRecord(r.Context(), record); err != nil {
			// Non-fatal if we can't save to pg but saved to redis, though we should log
		}
	}

	if err := sh.redisClient.PushJob(r.Context(), job); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `{"error":"Failed to queue job: %s"}`, err.Error())
		return
	}

	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, `{"id":"%s","status":"queued"}`, jobID)
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
