package handler

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/garv2003/code-execution-engine/internal/models"
	"github.com/garv2003/code-execution-engine/internal/pushsub"
)

type SubmitHandler struct {
	redisClient        *pushsub.RedisClient
	supportedLanguages map[string]bool
}

func NewSubmitHandler(rc *pushsub.RedisClient, supported map[string]bool) SubmitHandler {
	return SubmitHandler{
		redisClient:        rc,
		supportedLanguages: supported,
	}
}

type submitRequest struct {
	Language  string `json:"language"`
	Code      string `json:"code"`
	Stdin     string `json:"stdin"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (sh *SubmitHandler) Submit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid request payload"}`))
		return
	}

	if req.Language == "" || req.Code == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"language and code are required fields"}`))
		return
	}

	if !sh.supportedLanguages[req.Language] {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Unsupported language"}`))
		return
	}

	timeout := 5 * time.Second
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}

	jobID := generateUUID()
	job := &models.Job{
		ID:        jobID,
		Language:  req.Language,
		Code:      req.Code,
		Stdin:     req.Stdin,
		Timeout:   timeout,
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
