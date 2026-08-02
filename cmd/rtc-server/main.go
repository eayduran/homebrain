package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"home-brain-rtc/internal/config"
	"home-brain-rtc/internal/httpapi"
	"home-brain-rtc/internal/recording"
	"home-brain-rtc/internal/rtc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("server_exit", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	recorderFactory := recording.NewFactory(cfg.RecordingsDir, time.Now)
	rtcServer, err := rtc.NewServer(rtc.Options{
		PublicIP:        cfg.PublicIP,
		UDPPortMin:      cfg.UDPPortMin,
		UDPPortMax:      cfg.UDPPortMax,
		ICELite:         cfg.ICELite,
		Recordings:      recorderFactory,
		Logger:          logger,
		AnswerTimeout:   4500 * time.Millisecond,
		DisconnectGrace: 5 * time.Second,
	})
	if err != nil {
		return err
	}
	manager := rtc.NewManager(rtcServer, cfg.SessionTTL, logger)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.New(cfg.SessionAPIToken, manager, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      6 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	logger.Info("server_started", "httpAddr", listener.Addr().String())

	select {
	case err := <-serveResult:
		_ = manager.CloseAll()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownErr := shutdownHTTPServer(server, serveResult, 10*time.Second)
		closeErr := manager.CloseAll()
		return errors.Join(shutdownErr, closeErr)
	}
}

func shutdownHTTPServer(server *http.Server, serveResult <-chan error, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	shutdownErr := server.Shutdown(shutdownCtx)
	cancel()
	if shutdownErr != nil {
		if closeErr := server.Close(); closeErr != nil {
			return errors.Join(shutdownErr, closeErr)
		}
		if errors.Is(shutdownErr, context.DeadlineExceeded) {
			shutdownErr = nil
		}
	}

	wait := time.NewTimer(timeout)
	defer wait.Stop()
	select {
	case serveErr := <-serveResult:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	case <-wait.C:
		_ = server.Close()
		return errors.New("HTTP server did not stop within shutdown deadline")
	}
}
