package main

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestRunStartsAndGracefullyStopsOnContextCancellation(t *testing.T) {
	env := map[string]string{
		"HTTP_ADDR":         "127.0.0.1:0",
		"PUBLIC_IP":         "8.8.8.8",
		"SESSION_API_TOKEN": "test-token",
		"RECORDINGS_DIR":    filepath.Join(t.TempDir(), "recordings"),
		"UDP_PORT_MIN":      "50200",
		"UDP_PORT_MAX":      "50210",
		"SESSION_TTL":       "1m",
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(25*time.Millisecond, cancel)
	if err := run(ctx, func(key string) string { return env[key] }); err != nil {
		t.Fatalf("run returned error during graceful shutdown: %v", err)
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	if err := run(context.Background(), func(string) string { return "" }); err == nil {
		t.Fatal("expected startup configuration error")
	}
}

func TestShutdownHTTPServerForcesBoundedCloseAfterGraceTimeout(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(requestStarted)
		<-releaseHandler
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach test server")
	}

	started := time.Now()
	if err := shutdownHTTPServer(server, serveResult, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	close(releaseHandler)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown was not bounded: %s", elapsed)
	}
}
