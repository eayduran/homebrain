package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func validEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"PUBLIC_IP":         "8.8.8.8",
		"SESSION_API_TOKEN": "test-secret",
		"RECORDINGS_DIR":    t.TempDir(),
	}
}

func envGetter(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadAppliesDefaultsAndParsesValues(t *testing.T) {
	env := validEnv(t)
	env["UDP_PORT_MIN"] = "41000"
	env["UDP_PORT_MAX"] = "41010"
	env["SESSION_TTL"] = "2m"
	env["LOG_LEVEL"] = "debug"

	got, err := Load(envGetter(env))
	if err != nil {
		t.Fatal(err)
	}
	if got.HTTPAddr != ":8080" || got.PublicIP.String() != "8.8.8.8" {
		t.Fatalf("unexpected address values: %#v", got)
	}
	if got.UDPPortMin != 41000 || got.UDPPortMax != 41010 {
		t.Fatalf("unexpected ports: %d-%d", got.UDPPortMin, got.UDPPortMax)
	}
	if got.SessionTTL != 2*time.Minute || got.LogLevel != slog.LevelDebug {
		t.Fatalf("unexpected duration/level: %#v", got)
	}
}

func TestLoadCreatesRecordingsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "recordings")
	env := validEnv(t)
	env["RECORDINGS_DIR"] = dir

	if _, err := Load(envGetter(env)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("recordings directory not created: %v", err)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, map[string]string)
	}{
		{"missing public IP", func(_ *testing.T, env map[string]string) { env["PUBLIC_IP"] = "" }},
		{"private IPv4", func(_ *testing.T, env map[string]string) { env["PUBLIC_IP"] = "10.0.0.1" }},
		{"loopback IPv4", func(_ *testing.T, env map[string]string) { env["PUBLIC_IP"] = "127.0.0.1" }},
		{"link local IPv4", func(_ *testing.T, env map[string]string) { env["PUBLIC_IP"] = "169.254.1.1" }},
		{"CGNAT IPv4", func(_ *testing.T, env map[string]string) { env["PUBLIC_IP"] = "100.64.1.1" }},
		{"documentation IPv4", func(_ *testing.T, env map[string]string) { env["PUBLIC_IP"] = "203.0.113.10" }},
		{"IPv6", func(_ *testing.T, env map[string]string) { env["PUBLIC_IP"] = "2001:4860:4860::8888" }},
		{"invalid IP", func(_ *testing.T, env map[string]string) { env["PUBLIC_IP"] = "not-an-ip" }},
		{"empty token", func(_ *testing.T, env map[string]string) { env["SESSION_API_TOKEN"] = "" }},
		{"zero minimum port", func(_ *testing.T, env map[string]string) { env["UDP_PORT_MIN"] = "0" }},
		{"port overflow", func(_ *testing.T, env map[string]string) { env["UDP_PORT_MAX"] = "65536" }},
		{"reversed ports", func(_ *testing.T, env map[string]string) {
			env["UDP_PORT_MIN"], env["UDP_PORT_MAX"] = "50000", "40000"
		}},
		{"invalid TTL", func(_ *testing.T, env map[string]string) { env["SESSION_TTL"] = "soon" }},
		{"zero TTL", func(_ *testing.T, env map[string]string) { env["SESSION_TTL"] = "0s" }},
		{"invalid log level", func(_ *testing.T, env map[string]string) { env["LOG_LEVEL"] = "verbose" }},
		{"recordings path is a file", func(t *testing.T, env map[string]string) {
			path := filepath.Join(t.TempDir(), "recordings")
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			env["RECORDINGS_DIR"] = path
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv(t)
			tt.mutate(t, env)
			if _, err := Load(envGetter(env)); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}
