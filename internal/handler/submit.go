package handler

import "net/http"

type SubmitHandler struct {
}

func NewSubmitHandler() SubmitHandler {
	return SubmitHandler{}
}

func (sh *SubmitHandler) Submit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"id":"placeholder-id","status":"queued"}`))
}
