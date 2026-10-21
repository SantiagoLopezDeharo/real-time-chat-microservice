package server

import (
	"net/http"
	"net/http/pprof"
	"time"

	"chat-microservice/internal/config"
	"chat-microservice/internal/httpapi"
	"chat-microservice/internal/middleware"

	"golang.org/x/time/rate"
)

func Routes(cfg *config.Config, h *httpapi.Handler) http.Handler {
	auth := middleware.NewAuthMiddleware(cfg.JWTSecret).Verify
	limit := middleware.NewRateLimiter(rate.Limit(cfg.RateLimitRPS), cfg.RateLimitBurst).Middleware
	protected := func(fn http.HandlerFunc) http.Handler { return auth(limit(fn)) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.Health)
	mux.Handle("GET /ws", auth(http.HandlerFunc(h.HandleWebsocket)))
	mux.Handle("POST /api/messages", protected(h.HandleSendMessage))
	mux.Handle("GET /api/messages/get", protected(h.HandleGetMessages))
	mux.HandleFunc("POST /api/connections", h.HandleGetUserConnections)
	return mux
}

func New(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
}

func Pprof(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
}
