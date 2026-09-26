package api

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/domain"
	"github.com/reddy-aravind/order-fulfillment-orchestrator/internal/observability"
)

// recoverer converts a panic in a downstream handler into a JSON 500 response
// instead of crashing the connection, logging the panic at error level.
func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if logger != nil {
						logger.ErrorContext(r.Context(), "panic recovered",
							"panic", fmt.Sprintf("%v", rec),
							"method", r.Method,
							"path", r.URL.Path,
							"request_id", middleware.GetReqID(r.Context()),
						)
					}
					writeJSON(w, http.StatusInternalServerError, errorBody{
						Error: errorDetail{Code: "internal_error", Message: "internal server error"},
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// requestLogger emits one structured log line per request once it completes.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			if logger == nil {
				return
			}
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", middleware.GetReqID(r.Context())),
			)
		})
	}
}

// prometheusTiming records request counts and durations, labelled by the chi
// route pattern so cardinality stays bounded.
func prometheusTiming(m *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			if m == nil {
				return
			}
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			m.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()
			m.HTTPDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		})
	}
}

// rateLimit enforces a per-client-IP request budget backed by the Redis
// RateLimiter. It fails open (allows the request) if the limiter itself errors
// so a Redis blip does not take down the API.
func rateLimit(rl domain.RateLimiter, logger *slog.Logger, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rl != nil {
				key := "ratelimit:" + clientIP(r)
				allowed, err := rl.Allow(r.Context(), key, limit, window)
				switch {
				case err != nil:
					if logger != nil {
						logger.WarnContext(r.Context(), "rate limiter unavailable; allowing request", "error", err.Error())
					}
				case !allowed:
					writeError(w, r, logger, fmt.Errorf("%w: limit of %d requests per %s exceeded", domain.ErrRateLimited, limit, window))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the host portion of the request's remote address. RealIP
// middleware has already resolved X-Forwarded-For / X-Real-IP into RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
