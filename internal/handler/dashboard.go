package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/garv2003/code-execution-engine/internal/db"
)

type DashboardHandler struct {
	pgDB *db.PostgresDB
}

func NewDashboardHandler(pgDB *db.PostgresDB) DashboardHandler {
	return DashboardHandler{pgDB: pgDB}
}

func (h *DashboardHandler) Jobs(w http.ResponseWriter, r *http.Request) {
	if h.pgDB == nil {
		http.Error(w, "Dashboard requires PostgreSQL to be configured", http.StatusNotImplemented)
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	jobs, err := h.pgDB.GetRecentJobs(r.Context(), limit)
	if err != nil {
		http.Error(w, "Failed to fetch jobs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jobs)
}
