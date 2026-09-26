package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Database interface {
	Ping(context.Context) error
}

type Readiness interface {
	Ready() bool
}

func Handler(database Database, service Readiness) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(response http.ResponseWriter, _ *http.Request) {
		write(response, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /health/ready", func(response http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if !service.Ready() || database.Ping(ctx) != nil {
			write(response, http.StatusServiceUnavailable, "not_ready")
			return
		}
		write(response, http.StatusOK, "ready")
	})
	return mux
}

func write(response http.ResponseWriter, status int, value string) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"status": value})
}
