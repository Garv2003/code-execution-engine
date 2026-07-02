package handler

import (
	"encoding/json"
	"net/http"

	"github.com/garv2003/code-execution-engine/internal/pushsub"
)

type JobHandler struct {
	redisClient *pushsub.RedisClient
}

func NewJobHandler(rc *pushsub.RedisClient) JobHandler {
	return JobHandler{redisClient: rc}
}

func (jh *JobHandler) Job(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing job ID", http.StatusBadRequest)
		return
	}

	record, found, err := jh.redisClient.GetJobRecord(r.Context(), id)
	if err != nil {
		http.Error(w, "Failed fetching job", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(record)
}
