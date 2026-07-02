package handler

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/garv2003/code-execution-engine/internal/pushsub"
)

type ResultHandler struct {
	redisClient *pushsub.RedisClient
}

func NewResultHandler(rc *pushsub.RedisClient) ResultHandler {
	return ResultHandler{redisClient: rc}
}

func (rh *ResultHandler) Result(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing job ID", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	cachedResult, found, err := rh.redisClient.GetResult(r.Context(), id)
	if err != nil {
		slog.Error("Failed fetching cached result", "job_id", id, "error", err)
		http.Error(w, "Failed fetching result", http.StatusInternalServerError)
		return
	}
	if found {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", cachedResult)
		flusher.Flush()
		slog.Info("Cached result delivered to client", "job_id", id)
		return
	}

	pubsub := rh.redisClient.SubscribeResult(r.Context(), id)
	defer pubsub.Close()

	ch := pubsub.Channel()

	slog.Info("Client subscribed to SSE result stream", "job_id", id)
	_, _ = fmt.Fprintf(w, "data: {\"status\":\"subscribed\",\"id\":\"%s\"}\n\n", id)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			slog.Info("Client disconnected from SSE stream early", "job_id", id)
			return

		case msg, ok := <-ch:
			if !ok {
				slog.Warn("Redis pubsub channel closed unexpectedly", "job_id", id)
				return
			}

			_, _ = fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
			flusher.Flush()

			slog.Info("Result delivered to client, closing SSE stream", "job_id", id)
			return
		}
	}
}
