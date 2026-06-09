// Package api wires the chi router, middleware stack and health checks.
package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/stepanok/beacon-server/internal/auth"
	"github.com/stepanok/beacon-server/internal/handler"
)

// accessLog emits one structured slog line per request with the chi request id.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			defer func() {
				logger.Info("http",
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"durationMs", time.Since(start).Milliseconds(),
					"requestId", middleware.GetReqID(r.Context()),
					"remote", r.RemoteAddr,
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

func unauthorized(w http.ResponseWriter, code, msg string, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `","message":"` + msg + `"}`))
}

// authenticate parses an optional Bearer JWT and attaches the user to the context.
// It never rejects — public endpoints work anonymously; gating is done by the
// requireAnalyst / requireMutator wrappers below.
func authenticate(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); tok != "" {
				if u, err := auth.Parse(tok, secret); err == nil {
					r = r.WithContext(handler.WithUser(r.Context(), &u))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireAnalyst gates analyst reads (full report list / stats / export) — any
// authenticated analyst role.
func requireAnalyst(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler.UserFromContext(r.Context()) == nil {
			unauthorized(w, "unauthorized", "analyst login required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireMutator gates verify/dispatch — any analyst role except the read-only
// external viewer.
func requireMutator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := handler.UserFromContext(r.Context())
		if u == nil {
			unauthorized(w, "unauthorized", "analyst login required", http.StatusUnauthorized)
			return
		}
		if !u.CanMutate() {
			unauthorized(w, "forbidden", "your role is read-only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
