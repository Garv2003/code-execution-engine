package handler

import (
	"fmt"
	"net/http"
	"time"
)

type ResultHandler struct {
}

func (rh *ResultHandler) Result(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	_, _ = fmt.Fprintf(w, "data: Connecting to stream for job %s...\n\n", id)
	flusher.Flush()

	time.Sleep(1 * time.Second)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func NewResultHandler() ResultHandler {
	return ResultHandler{}
}
