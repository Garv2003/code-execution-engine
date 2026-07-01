package handler

import "net/http"

type HealthHandler struct {
}

func (hh *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func NewHealthHandler() HealthHandler {
	return HealthHandler{}
}
