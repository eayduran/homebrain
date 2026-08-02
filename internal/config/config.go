package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr     = ":8080"
	defaultUDPPortMin   = uint16(40000)
	defaultUDPPortMax   = uint16(40020)
	defaultRecordings   = "/data/recordings"
	defaultSessionTTL   = 10 * time.Minute
	defaultAudioPrime   = 10 * time.Second
	audioPrimeFrame     = 20 * time.Millisecond
	defaultLogLevelName = "info"
)

type Config struct {
	HTTPAddr           string
	PublicIP           net.IP
	UDPPortMin         uint16
	UDPPortMax         uint16
	SessionAPIToken    string
	RecordingsDir      string
	SessionTTL         time.Duration
	LogLevel           slog.Level
	ICELite            bool
	AudioPrimeEnabled  bool
	AudioPrimeDuration time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("environment reader is required")
	}

	publicIP, err := parsePublicIPv4(getenv("PUBLIC_IP"))
	if err != nil {
		return Config{}, err
	}
	portMin, err := parsePort(getenv("UDP_PORT_MIN"), defaultUDPPortMin, "UDP_PORT_MIN")
	if err != nil {
		return Config{}, err
	}
	portMax, err := parsePort(getenv("UDP_PORT_MAX"), defaultUDPPortMax, "UDP_PORT_MAX")
	if err != nil {
		return Config{}, err
	}
	if portMin > portMax {
		return Config{}, errors.New("UDP_PORT_MIN must be less than or equal to UDP_PORT_MAX")
	}

	token := getenv("SESSION_API_TOKEN")
	if strings.TrimSpace(token) == "" {
		return Config{}, errors.New("SESSION_API_TOKEN must not be empty")
	}
	recordingsDir := valueOr(getenv("RECORDINGS_DIR"), defaultRecordings)
	if err := ensureWritableDirectory(recordingsDir); err != nil {
		return Config{}, fmt.Errorf("RECORDINGS_DIR: %w", err)
	}

	ttl := defaultSessionTTL
	if raw := getenv("SESSION_TTL"); raw != "" {
		ttl, err = time.ParseDuration(raw)
		if err != nil || ttl <= 0 {
			return Config{}, errors.New("SESSION_TTL must be a positive duration")
		}
	}
	level, err := parseLogLevel(valueOr(getenv("LOG_LEVEL"), defaultLogLevelName))
	if err != nil {
		return Config{}, err
	}
	iceLite, err := parseBool(getenv("ICE_LITE"), "ICE_LITE")
	if err != nil {
		return Config{}, err
	}
	audioPrimeEnabled, err := parseBool(getenv("RTC_AUDIO_PRIME_ENABLED"), "RTC_AUDIO_PRIME_ENABLED")
	if err != nil {
		return Config{}, err
	}
	audioPrimeDuration := defaultAudioPrime
	if raw := getenv("RTC_AUDIO_PRIME_DURATION"); raw != "" {
		audioPrimeDuration, err = time.ParseDuration(raw)
		if err != nil || audioPrimeDuration < audioPrimeFrame || audioPrimeDuration%audioPrimeFrame != 0 {
			return Config{}, errors.New("RTC_AUDIO_PRIME_DURATION must be a positive multiple of 20ms")
		}
	}

	return Config{
		HTTPAddr:           valueOr(getenv("HTTP_ADDR"), defaultHTTPAddr),
		PublicIP:           publicIP,
		UDPPortMin:         portMin,
		UDPPortMax:         portMax,
		SessionAPIToken:    token,
		RecordingsDir:      recordingsDir,
		SessionTTL:         ttl,
		LogLevel:           level,
		ICELite:            iceLite,
		AudioPrimeEnabled:  audioPrimeEnabled,
		AudioPrimeDuration: audioPrimeDuration,
	}, nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func parsePort(raw string, fallback uint16, name string) (uint16, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be an integer between 1 and 65535", name)
	}
	return uint16(value), nil
}

func parseBool(raw, name string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return value, nil
}

var nonPublicIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func parsePublicIPv4(raw string) (net.IP, error) {
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.Is4() {
		return nil, errors.New("PUBLIC_IP must be a valid public IPv4 address")
	}
	for _, prefix := range nonPublicIPv4Prefixes {
		if prefix.Contains(addr) {
			return nil, errors.New("PUBLIC_IP must be a valid public IPv4 address")
		}
	}
	return net.IP(addr.AsSlice()), nil
}

func ensureWritableDirectory(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path must not be empty")
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("path is not a directory")
	}
	probe, err := os.CreateTemp(path, ".write-probe-*")
	if err != nil {
		return errors.New("directory is not writable")
	}
	probePath := probe.Name()
	if closeErr := probe.Close(); closeErr != nil {
		_ = os.Remove(probePath)
		return closeErr
	}
	if err := os.Remove(probePath); err != nil {
		return err
	}
	return nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, or error")
	}
}
