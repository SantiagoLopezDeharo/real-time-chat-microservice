package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chat-microservice/internal/config"
	"chat-microservice/internal/httpapi"
	"chat-microservice/internal/repository"
	"chat-microservice/internal/server"
	"chat-microservice/internal/service"
	"chat-microservice/internal/ws"

	"github.com/joho/godotenv"
)

const (
	connectTimeout  = 10 * time.Second
	shutdownTimeout = 15 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), connectTimeout)
	repo, err := repository.NewMongoRepository(connectCtx, cfg.MongoURI, cfg.MongoDB, cfg.MongoCollection)
	cancelConnect()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := ws.NewHub(ws.DefaultOptions())
	chat := service.NewChatService(repo, hub, service.DefaultOptions(cfg.RetryAttempts))
	handler := httpapi.NewHandler(chat, hub, cfg.MaxBodyBytes)
	srv := server.New(":"+cfg.Port, server.Routes(cfg, handler))
	errs := make(chan error, 2)

	go func() { errs <- serve(srv) }()
	slog.Info("server listening", "port", cfg.Port)

	if cfg.PprofAddr != "" {
		debug := server.Pprof(cfg.PprofAddr)
		go func() { errs <- serve(debug) }()
		defer debug.Close()
		slog.Info("pprof listening", "addr", cfg.PprofAddr)
	}

	var serveErr error
	select {
	case serveErr = <-errs:
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return errors.Join(
		serveErr,
		srv.Shutdown(shutdownCtx),
		hub.Shutdown(shutdownCtx),
		chat.Close(shutdownCtx),
		repo.Close(shutdownCtx),
	)
}

func serve(srv *http.Server) error {
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
