package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	maxResponseBytes   = 1 << 20
	exitSuccess        = 0
	exitProbeFailure   = 1
	exitCleanupFailure = 2
	cleanupTimeout     = 5 * time.Second
	connectionTimeout  = 20 * time.Second
	httpTimeout        = 10 * time.Second
)

var (
	errInvalidBaseURL       = errors.New("invalid server URL")
	errCreateRequest        = errors.New("session create failed")
	errInvalidAnswer        = errors.New("invalid session answer")
	errDeleteRequest        = errors.New("session cleanup failed")
	errOffer                = errors.New("offer generation failed")
	errProbeConfig          = errors.New("invalid probe configuration")
	errOfferCreate          = errors.New("offer create failed")
	errLocalDescription     = errors.New("local description failed")
	errICEGathering         = errors.New("ICE gathering failed")
	errRemoteDescription    = errors.New("remote description failed")
	errICEFailed            = errors.New("ICE failed")
	errPeerConnectionFailed = errors.New("peer connection failed")
	errConnectionTimeout    = errors.New("connection timeout")
)

type sessionClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type sessionAPI interface {
	Create(context.Context, string, string) (string, bool, error)
	Delete(context.Context, string) error
}

type peerProbe interface {
	Offer(context.Context) (string, error)
	ApplyAnswerAndWait(context.Context, string) error
	Close() error
}

type probeLogger struct {
	mu     sync.Mutex
	output io.Writer
}

func newProbeLogger(output io.Writer) *probeLogger {
	return &probeLogger{output: output}
}

func (l *probeLogger) event(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintln(l.output, name)
}

func (l *probeLogger) category(event, category string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.output, "%s category=%s\n", event, category)
}

func (l *probeLogger) state(event, state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.output, "%s state=%s\n", event, state)
}

func runProbe(ctx context.Context, api sessionAPI, peer peerProbe, logger *probeLogger, sessionID string) int {
	defer peer.Close()

	offer, err := peer.Offer(ctx)
	if err != nil {
		category := probeFailureCategory(err)
		if category == "" {
			category = "offer_create"
		}
		logger.category("probe_failed", category)
		return exitProbeFailure
	}

	answer, created, err := api.Create(ctx, sessionID, offer)
	if err != nil {
		category := "request"
		if created || errors.Is(err, errInvalidAnswer) {
			category = "answer_invalid"
		}
		logger.category("probe_failed", category)
		if created && cleanupSession(api, sessionID) != nil {
			reportCleanupFailure(logger)
		}
		return exitProbeFailure
	}
	logger.event("answer_received")

	if err := peer.ApplyAnswerAndWait(ctx, answer); err != nil {
		category := probeFailureCategory(err)
		if category == "" {
			category = "peer_connection_failed"
		}
		logger.category("probe_failed", category)
		if cleanupSession(api, sessionID) != nil {
			reportCleanupFailure(logger)
		}
		return exitProbeFailure
	}

	logger.event("probe_connected")
	if cleanupSession(api, sessionID) != nil {
		reportCleanupFailure(logger)
		return exitCleanupFailure
	}
	logger.event("probe_succeeded")
	return exitSuccess
}

func reportCleanupFailure(logger *probeLogger) {
	logger.category("probe_failed", "cleanup")
	logger.event("cleanup_failed")
}

func probeFailureCategory(err error) string {
	switch {
	case errors.Is(err, errOfferCreate):
		return "offer_create"
	case errors.Is(err, errLocalDescription):
		return "local_description"
	case errors.Is(err, errICEGathering):
		return "ice_gathering"
	case errors.Is(err, errRemoteDescription):
		return "remote_description"
	case errors.Is(err, errICEFailed):
		return "ice_failed"
	case errors.Is(err, errPeerConnectionFailed):
		return "peer_connection_failed"
	case errors.Is(err, errConnectionTimeout):
		return "timeout"
	default:
		return ""
	}
}

func cleanupSession(api sessionAPI, sessionID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	return api.Delete(ctx, sessionID)
}

type pionProbe struct {
	peer       *webrtc.PeerConnection
	logger     *probeLogger
	connection chan error
}

type probeOptions struct {
	stunURL      string
	includeVideo bool
}

func newPionProbe(options probeOptions, output io.Writer) (*pionProbe, error) {
	return newPionProbeWithLogger(options, newProbeLogger(output))
}

func newPionProbeWithLogger(options probeOptions, logger *probeLogger) (*pionProbe, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, errOffer
	}
	if options.includeVideo {
		for _, codec := range []webrtc.RTPCodecParameters{
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:    webrtc.MimeTypeH264,
					ClockRate:   90000,
					SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
				},
				PayloadType: 102,
			},
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:    webrtc.MimeTypeH264,
					ClockRate:   90000,
					SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f",
				},
				PayloadType: 112,
			},
		} {
			if err := mediaEngine.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
				return nil, errOffer
			}
		}
	}

	configuration := webrtc.Configuration{}
	if options.stunURL != "" {
		configuration.ICEServers = []webrtc.ICEServer{{URLs: []string{options.stunURL}}}
	}
	peer, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine)).NewPeerConnection(configuration)
	if err != nil {
		return nil, errOffer
	}
	if _, err := peer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendrecv,
	}); err != nil {
		_ = peer.Close()
		return nil, errOffer
	}
	if options.includeVideo {
		if _, err := peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionSendrecv,
		}); err != nil {
			_ = peer.Close()
			return nil, errOffer
		}
	}

	probe := &pionProbe{
		peer:       peer,
		logger:     logger,
		connection: make(chan error, 1),
	}
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		probe.logger.state("ice_connection_state", state.String())
		if state == webrtc.ICEConnectionStateFailed {
			probe.signalConnection(errICEFailed)
		}
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		probe.logger.state("peer_connection_state", state.String())
		switch state {
		case webrtc.PeerConnectionStateConnected:
			probe.signalConnection(nil)
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			if probe.peer.ICEConnectionState() == webrtc.ICEConnectionStateFailed {
				probe.signalConnection(errICEFailed)
			} else {
				probe.signalConnection(errPeerConnectionFailed)
			}
		}
	})
	return probe, nil
}

func (p *pionProbe) signalConnection(result error) {
	select {
	case p.connection <- result:
	default:
	}
}

func (p *pionProbe) Offer(ctx context.Context) (string, error) {
	offer, err := p.peer.CreateOffer(nil)
	if err != nil {
		return "", errOfferCreate
	}
	p.logger.event("offer_created")
	gathered := webrtc.GatheringCompletePromise(p.peer)
	if err := p.peer.SetLocalDescription(offer); err != nil {
		return "", errLocalDescription
	}
	p.logger.event("local_description_set")
	select {
	case <-ctx.Done():
		return "", errICEGathering
	case <-gathered:
	}
	local := p.peer.LocalDescription()
	if local == nil || local.SDP == "" {
		return "", errICEGathering
	}
	p.logger.event("ice_gathering_completed")
	return local.SDP, nil
}

func (p *pionProbe) ApplyAnswerAndWait(ctx context.Context, answer string) error {
	if err := p.peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		return errRemoteDescription
	}
	p.logger.event("remote_description_applied")
	waitCtx, cancel := context.WithTimeout(ctx, connectionTimeout)
	defer cancel()
	select {
	case result := <-p.connection:
		return result
	case <-waitCtx.Done():
		return errConnectionTimeout
	}
}

func (p *pionProbe) Close() error {
	p.peer.OnICEConnectionStateChange(nil)
	p.peer.OnConnectionStateChange(nil)
	return p.peer.Close()
}

func run(ctx context.Context, getenv func(string) string, output io.Writer) int {
	logger := newProbeLogger(output)
	baseURL := getenv("RTC_SERVER_URL")
	token := getenv("RTC_SERVER_TOKEN")
	if baseURL == "" || token == "" {
		logger.category("probe_failed", "configuration")
		return exitProbeFailure
	}
	includeVideo, err := parseIncludeVideo(getenv("RTC_PROBE_INCLUDE_VIDEO"))
	if err != nil {
		logger.category("probe_failed", "configuration")
		return exitProbeFailure
	}

	client, err := newSessionClient(baseURL, token, &http.Client{Timeout: httpTimeout})
	if err != nil {
		logger.category("probe_failed", "configuration")
		return exitProbeFailure
	}
	peer, err := newPionProbeWithLogger(probeOptions{
		stunURL:      getenv("RTC_PROBE_STUN_URL"),
		includeVideo: includeVideo,
	}, logger)
	if err != nil {
		logger.category("probe_failed", "peer")
		return exitProbeFailure
	}
	sessionID, err := newSessionID()
	if err != nil {
		_ = peer.Close()
		logger.category("probe_failed", "session")
		return exitProbeFailure
	}
	return runProbe(ctx, client, peer, logger, sessionID)
}

func parseIncludeVideo(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	includeVideo, err := strconv.ParseBool(value)
	if err != nil {
		return false, errProbeConfig
	}
	return includeVideo, nil
}

func newSessionID() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("rtc-probe-%x", random), nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Getenv, os.Stdout))
}

func newSessionClient(baseURL, token string, httpClient *http.Client) (*sessionClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errInvalidBaseURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &sessionClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: httpClient,
	}, nil
}

func (c *sessionClient) Create(ctx context.Context, sessionID, offer string) (string, bool, error) {
	body, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		OfferSDP  string `json:"offerSdp"`
	}{SessionID: sessionID, OfferSDP: offer})
	if err != nil {
		return "", false, errCreateRequest
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/rtc/sessions", bytes.NewReader(body))
	if err != nil {
		return "", false, errCreateRequest
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(req)
	if err != nil {
		return "", false, errCreateRequest
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return "", false, errCreateRequest
	}

	var payload struct {
		AnswerSDP string `json:"answerSdp"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(&payload); err != nil || payload.AnswerSDP == "" {
		return "", true, errInvalidAnswer
	}
	return payload.AnswerSDP, true, nil
}

func (c *sessionClient) Delete(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/rtc/sessions/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return errDeleteRequest
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(req)
	if err != nil {
		return errDeleteRequest
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errDeleteRequest
	}
	return nil
}
