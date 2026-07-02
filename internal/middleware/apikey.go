package middleware

import (
	"encoding/json"
	"net/http"
	"strings"
)

const apiKeyQueryParam = "api_key"

func APIKey(keys []string, next http.Handler) http.Handler {
	if len(keys) == 0 {
		return next
	}

	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key != "" {
			allowed[key] = struct{}{}
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get("X-API-Key")
		if key == "" {
			key = r.URL.Query().Get(apiKeyQueryParam)
		}

		if _, ok := allowed[key]; !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isPublicPath(path string) bool {
	return path == "/health" || path == "/playground" || strings.HasPrefix(path, "/playground/")
}
