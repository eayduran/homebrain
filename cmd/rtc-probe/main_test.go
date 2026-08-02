package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"home-brain-rtc/internal/recording"
	"home-brain-rtc/internal/rtc"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestSessionClientConstructsCreateAndDeleteRequests(t *testing.T) {
	t.Helper()

	const (
		token     = "test-token"
		sessionID = "rtc-probe-1234"
		offerSDP  = "test-offer"
	)

	requestNumber := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber++
		if got, want := r.Header.Get("Authorization"), "Bearer "+token; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}

		switch requestNumber {
		case 1:
			if got, want := r.Method, http.MethodPost; got != want {
				t.Errorf("method = %q, want %q", got, want)
			}
			if got, want := r.URL.Path, "/v1/rtc/sessions"; got != want {
				t.Errorf("path = %q, want %q", got, want)
			}
			if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
				t.Errorf("Content-Type = %q, want %q", got, want)
			}

			var body struct {
				SessionID string `json:"sessionId"`
				OfferSDP  string `json:"offerSdp"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if got, want := body.SessionID, sessionID; got != want {
				t.Errorf("sessionId = %q, want %q", got, want)
			}
			if got, want := body.OfferSDP, offerSDP; got != want {
				t.Errorf("offerSdp = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sessionId":"rtc-probe-1234","answerSdp":"test-answer"}`))
		case 2:
			if got, want := r.Method, http.MethodDelete; got != want {
				t.Errorf("method = %q, want %q", got, want)
			}
			if got, want := r.URL.Path, "/v1/rtc/sessions/"+sessionID; got != want {
				t.Errorf("path = %q, want %q", got, want)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %d", requestNumber)
		}
	}))
	defer server.Close()

	client, err := newSessionClient(server.URL, token, server.Client())
	if err != nil {
		t.Fatalf("newSessionClient: %v", err)
	}
	answer, created, err := client.Create(context.Background(), sessionID, offerSDP)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created {
		t.Fatal("Create created = false, want true")
	}
	if got, want := answer, "test-answer"; got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}
	if err := client.Delete(context.Background(), sessionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, want := requestNumber, 2; got != want {
		t.Fatalf("request count = %d, want %d", got, want)
	}
}

func TestSessionClientValidatesCreateResponse(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantAnswer  string
		wantCreated bool
		wantError   bool
	}{
		{name: "non-success status", status: http.StatusBadGateway, body: `upstream detail`, wantError: true},
		{name: "invalid JSON", status: http.StatusOK, body: `{`, wantCreated: true, wantError: true},
		{name: "missing answer", status: http.StatusOK, body: `{"sessionId":"rtc-probe-1"}`, wantCreated: true, wantError: true},
		{name: "empty answer", status: http.StatusOK, body: `{"answerSdp":""}`, wantCreated: true, wantError: true},
		{name: "wrong answer type", status: http.StatusOK, body: `{"answerSdp":42}`, wantCreated: true, wantError: true},
		{name: "valid answer", status: http.StatusCreated, body: `{"answerSdp":"answer"}`, wantAnswer: "answer", wantCreated: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client, err := newSessionClient(server.URL, "token", server.Client())
			if err != nil {
				t.Fatalf("newSessionClient: %v", err)
			}
			answer, created, err := client.Create(context.Background(), "rtc-probe-1", "offer")
			if got := err != nil; got != tt.wantError {
				t.Fatalf("error present = %v, want %v", got, tt.wantError)
			}
			if created != tt.wantCreated {
				t.Fatalf("created = %v, want %v", created, tt.wantCreated)
			}
			if answer != tt.wantAnswer {
				t.Fatalf("answer = %q, want %q", answer, tt.wantAnswer)
			}
		})
	}
}

func TestSessionClientRejectsUnsafeBaseURL(t *testing.T) {
	for _, baseURL := range []string{"", "ftp://example.com", "https://user:pass@example.com", "https://example.com?query=secret"} {
		t.Run(baseURL, func(t *testing.T) {
			if _, err := newSessionClient(baseURL, "token", http.DefaultClient); err == nil {
				t.Fatal("newSessionClient error = nil, want error")
			}
		})
	}
}

type fakeSessionAPI struct {
	answer      string
	created     bool
	createErr   error
	deleteErr   error
	deleteCalls int
}

func (f *fakeSessionAPI) Create(context.Context, string, string) (string, bool, error) {
	return f.answer, f.created, f.createErr
}

func (f *fakeSessionAPI) Delete(context.Context, string) error {
	f.deleteCalls++
	return f.deleteErr
}

type fakePeerProbe struct {
	offerErr   error
	connectErr error
	closeCalls int
}

func (f *fakePeerProbe) Offer(context.Context) (string, error) {
	return "offer", f.offerErr
}

func (f *fakePeerProbe) ApplyAnswerAndWait(context.Context, string) error {
	return f.connectErr
}

func (f *fakePeerProbe) Close() error {
	f.closeCalls++
	return nil
}

func TestRunProbeConnectedAndCleanupSuccessful(t *testing.T) {
	api := &fakeSessionAPI{answer: "answer", created: true}
	peer := &fakePeerProbe{}
	var output bytes.Buffer

	code := runProbe(context.Background(), api, peer, newProbeLogger(&output), "rtc-probe-1")

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d", code, exitSuccess)
	}
	if api.deleteCalls != 1 {
		t.Fatalf("DELETE calls = %d, want 1", api.deleteCalls)
	}
	if peer.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want 1", peer.closeCalls)
	}
	if got, want := output.String(), "answer_received\nprobe_connected\nprobe_succeeded\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunProbeConnectedButCleanupFailed(t *testing.T) {
	api := &fakeSessionAPI{answer: "answer", created: true, deleteErr: errors.New("delete detail")}
	peer := &fakePeerProbe{}
	var output bytes.Buffer

	code := runProbe(context.Background(), api, peer, newProbeLogger(&output), "rtc-probe-1")

	if code != exitCleanupFailure {
		t.Fatalf("exit code = %d, want %d", code, exitCleanupFailure)
	}
	if api.deleteCalls != 1 || peer.closeCalls != 1 {
		t.Fatalf("DELETE calls = %d, Close calls = %d; want 1, 1", api.deleteCalls, peer.closeCalls)
	}
	if got, want := output.String(), "answer_received\nprobe_connected\nprobe_failed category=cleanup\ncleanup_failed\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunProbeFailureAfterCreateAttemptsCleanupAndKeepsFailureCode(t *testing.T) {
	api := &fakeSessionAPI{created: true, createErr: errInvalidAnswer, deleteErr: errors.New("delete detail")}
	peer := &fakePeerProbe{}
	var output bytes.Buffer

	code := runProbe(context.Background(), api, peer, newProbeLogger(&output), "rtc-probe-1")

	if code != exitProbeFailure {
		t.Fatalf("exit code = %d, want %d", code, exitProbeFailure)
	}
	if api.deleteCalls != 1 || peer.closeCalls != 1 {
		t.Fatalf("DELETE calls = %d, Close calls = %d; want 1, 1", api.deleteCalls, peer.closeCalls)
	}
	if got, want := output.String(), "probe_failed category=answer_invalid\nprobe_failed category=cleanup\ncleanup_failed\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunProbeFailureBeforeSuccessfulPostSkipsDeleteAndClosesPeer(t *testing.T) {
	tests := []struct {
		name string
		api  *fakeSessionAPI
		peer *fakePeerProbe
	}{
		{name: "offer failure", api: &fakeSessionAPI{}, peer: &fakePeerProbe{offerErr: errors.New("offer detail")}},
		{name: "POST failure", api: &fakeSessionAPI{createErr: errors.New("HTTP detail")}, peer: &fakePeerProbe{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			code := runProbe(context.Background(), tt.api, tt.peer, newProbeLogger(&output), "rtc-probe-1")
			if code != exitProbeFailure {
				t.Fatalf("exit code = %d, want %d", code, exitProbeFailure)
			}
			if tt.api.deleteCalls != 0 {
				t.Fatalf("DELETE calls = %d, want 0", tt.api.deleteCalls)
			}
			if tt.peer.closeCalls != 1 {
				t.Fatalf("Close calls = %d, want 1", tt.peer.closeCalls)
			}
		})
	}
}

func TestRunProbeClassifiesOfferStageFailures(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		category string
	}{
		{name: "offer create", err: errOfferCreate, category: "offer_create"},
		{name: "local description", err: errLocalDescription, category: "local_description"},
		{name: "ICE gathering", err: errICEGathering, category: "ice_gathering"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeSessionAPI{}
			peer := &fakePeerProbe{offerErr: tt.err}
			var output bytes.Buffer
			code := runProbe(context.Background(), api, peer, newProbeLogger(&output), "rtc-probe-1")
			if code != exitProbeFailure {
				t.Fatalf("exit code = %d, want %d", code, exitProbeFailure)
			}
			if got, want := output.String(), "probe_failed category="+tt.category+"\n"; got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
			if api.deleteCalls != 0 || peer.closeCalls != 1 {
				t.Fatalf("DELETE calls = %d, Close calls = %d; want 0, 1", api.deleteCalls, peer.closeCalls)
			}
		})
	}
}

func TestRunProbeConnectionFailureAttemptsDelete(t *testing.T) {
	api := &fakeSessionAPI{answer: "answer", created: true}
	peer := &fakePeerProbe{connectErr: errConnectionTimeout}
	var output bytes.Buffer

	code := runProbe(context.Background(), api, peer, newProbeLogger(&output), "rtc-probe-1")

	if code != exitProbeFailure {
		t.Fatalf("exit code = %d, want %d", code, exitProbeFailure)
	}
	if api.deleteCalls != 1 || peer.closeCalls != 1 {
		t.Fatalf("DELETE calls = %d, Close calls = %d; want 1, 1", api.deleteCalls, peer.closeCalls)
	}
	if strings.Contains(output.String(), "probe_connected") || strings.Contains(output.String(), "probe_succeeded") {
		t.Fatalf("failure output includes success event: %q", output.String())
	}
	if got, want := output.String(), "answer_received\nprobe_failed category=timeout\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestRunProbeClassifiesRequestAndAnswerFailures(t *testing.T) {
	tests := []struct {
		name        string
		api         *fakeSessionAPI
		category    string
		wantDeletes int
	}{
		{name: "request", api: &fakeSessionAPI{createErr: errCreateRequest}, category: "request"},
		{name: "answer invalid", api: &fakeSessionAPI{created: true, createErr: errInvalidAnswer}, category: "answer_invalid", wantDeletes: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer := &fakePeerProbe{}
			var output bytes.Buffer
			code := runProbe(context.Background(), tt.api, peer, newProbeLogger(&output), "rtc-probe-1")
			if code != exitProbeFailure {
				t.Fatalf("exit code = %d, want %d", code, exitProbeFailure)
			}
			if got, want := output.String(), "probe_failed category="+tt.category+"\n"; got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
			if tt.api.deleteCalls != tt.wantDeletes {
				t.Fatalf("DELETE calls = %d, want %d", tt.api.deleteCalls, tt.wantDeletes)
			}
		})
	}
}

func TestRunProbeClassifiesConnectionStageFailures(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		category string
	}{
		{name: "remote description", err: errRemoteDescription, category: "remote_description"},
		{name: "ICE failed", err: errICEFailed, category: "ice_failed"},
		{name: "PeerConnection failed", err: errPeerConnectionFailed, category: "peer_connection_failed"},
		{name: "timeout", err: errConnectionTimeout, category: "timeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeSessionAPI{answer: "answer", created: true}
			peer := &fakePeerProbe{connectErr: tt.err}
			var output bytes.Buffer
			code := runProbe(context.Background(), api, peer, newProbeLogger(&output), "rtc-probe-1")
			if code != exitProbeFailure {
				t.Fatalf("exit code = %d, want %d", code, exitProbeFailure)
			}
			if got, want := output.String(), "answer_received\nprobe_failed category="+tt.category+"\n"; got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
			if api.deleteCalls != 1 || peer.closeCalls != 1 {
				t.Fatalf("DELETE calls = %d, Close calls = %d; want 1, 1", api.deleteCalls, peer.closeCalls)
			}
		})
	}
}

func TestPionProbeOfferUsesOpus111SendrecvAndCompletesGatheringWithoutMediaInput(t *testing.T) {
	var output bytes.Buffer
	probe, err := newPionProbe(probeOptions{}, &output)
	if err != nil {
		t.Fatalf("newPionProbe: %v", err)
	}
	defer probe.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	offer, err := probe.Offer(ctx)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	if got, want := output.String(), "offer_created\nlocal_description_set\nice_gathering_completed\n"; got != want {
		t.Fatalf("stage output = %q, want %q", got, want)
	}
	if !strings.Contains(offer, "m=audio 9 UDP/TLS/RTP/SAVPF 111") {
		t.Fatalf("offer does not negotiate audio payload 111")
	}
	if !strings.Contains(offer, "a=rtpmap:111 opus/48000/2") {
		t.Fatalf("offer does not map payload 111 to Opus")
	}
	if !strings.Contains(offer, "a=sendrecv") {
		t.Fatalf("offer does not contain sendrecv audio")
	}
	if got, want := probe.peer.ICEGatheringState(), webrtc.ICEGatheringStateComplete; got != want {
		t.Fatalf("ICE gathering state = %s, want %s", got, want)
	}
	transceivers := probe.peer.GetTransceivers()
	if len(transceivers) != 1 {
		t.Fatalf("transceiver count = %d, want 1", len(transceivers))
	}
	if transceivers[0].Direction() != webrtc.RTPTransceiverDirectionSendrecv {
		t.Fatalf("transceiver direction = %s, want sendrecv", transceivers[0].Direction())
	}
	if strings.Contains(offer, "m=video ") {
		t.Fatal("default offer contains a video m-line")
	}
	if strings.Contains(offer, "a=rtpmap:102") || strings.Contains(offer, "a=rtpmap:112") {
		t.Fatal("default offer contains opt-in video codecs")
	}
}

func TestPionProbeLogsRemoteDescriptionAppliedBeforeCanceledWait(t *testing.T) {
	var output synchronizedBuffer
	probe, err := newPionProbe(probeOptions{}, &output)
	if err != nil {
		t.Fatalf("newPionProbe: %v", err)
	}
	defer probe.Close()

	offerCtx, cancelOffer := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelOffer()
	offer, err := probe.Offer(offerCtx)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}
	answer := newPionAnswerForOffer(t, offer)

	waitCtx, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	err = probe.ApplyAnswerAndWait(waitCtx, answer)
	if !errors.Is(err, errConnectionTimeout) {
		t.Fatalf("ApplyAnswerAndWait error = %v, want timeout sentinel", err)
	}
	if !strings.Contains(output.String(), "remote_description_applied\n") {
		t.Fatalf("output %q does not contain remote description success marker", output.String())
	}
}

func newPionAnswerForOffer(t *testing.T, offer string) string {
	t.Helper()
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatalf("register Opus: %v", err)
	}
	peer, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("new answerer: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		t.Fatalf("set answerer remote offer: %v", err)
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("create answer: %v", err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(answer); err != nil {
		t.Fatalf("set answerer local description: %v", err)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		t.Fatal("answer ICE gathering timed out")
	}
	if peer.LocalDescription() == nil {
		t.Fatal("answerer local description is nil")
	}
	return peer.LocalDescription().SDP
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(value)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestPionProbeMixedOfferBundlesOpusAndDummyH264VideoTrack(t *testing.T) {
	probe, err := newPionProbe(probeOptions{includeVideo: true}, io.Discard)
	if err != nil {
		t.Fatalf("newPionProbe: %v", err)
	}
	defer probe.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	offer, err := probe.Offer(ctx)
	if err != nil {
		t.Fatalf("Offer: %v", err)
	}

	if !sdpHasLine(offer, "a=group:BUNDLE 0 1") {
		t.Fatal("mixed offer does not contain BUNDLE MIDs 0 1")
	}
	audio := sdpMediaSection(t, offer, "audio")
	video := sdpMediaSection(t, offer, "video")
	if got, want := audio[0], "m=audio 9 UDP/TLS/RTP/SAVPF 111"; got != want {
		t.Fatalf("audio m-line = %q, want %q", got, want)
	}
	if got, want := video[0], "m=video 9 UDP/TLS/RTP/SAVPF 102 112"; got != want {
		t.Fatalf("video m-line = %q, want %q", got, want)
	}
	for _, expectation := range []struct {
		name    string
		section []string
		line    string
	}{
		{name: "audio MID", section: audio, line: "a=mid:0"},
		{name: "video MID", section: video, line: "a=mid:1"},
		{name: "audio direction", section: audio, line: "a=sendrecv"},
		{name: "video direction", section: video, line: "a=sendrecv"},
		{name: "H264 payload 102", section: video, line: "a=rtpmap:102 H264/90000"},
		{name: "H264 profile 102", section: video, line: "a=fmtp:102 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"},
		{name: "H264 payload 112", section: video, line: "a=rtpmap:112 H264/90000"},
		{name: "H264 profile 112", section: video, line: "a=fmtp:112 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f"},
	} {
		if !linesContain(expectation.section, expectation.line) {
			t.Fatalf("%s line %q is missing", expectation.name, expectation.line)
		}
	}

	transceivers := probe.peer.GetTransceivers()
	if len(transceivers) != 2 {
		t.Fatalf("transceiver count = %d, want 2", len(transceivers))
	}
	if transceivers[0].Kind() != webrtc.RTPCodecTypeAudio || transceivers[1].Kind() != webrtc.RTPCodecTypeVideo {
		t.Fatalf("transceiver kinds = %s, %s; want audio, video", transceivers[0].Kind(), transceivers[1].Kind())
	}
	if transceivers[1].Direction() != webrtc.RTPTransceiverDirectionSendrecv {
		t.Fatalf("video direction = %s, want sendrecv", transceivers[1].Direction())
	}
	if transceivers[1].Sender() == nil {
		t.Fatal("video sendrecv transceiver has no sender")
	}
	videoTrack := transceivers[1].Sender().Track()
	if videoTrack == nil {
		t.Fatal("video sender dummy track was detached before PeerConnection cleanup")
	}
	if got, want := videoTrack.Kind(), webrtc.RTPCodecTypeVideo; got != want {
		t.Fatalf("dummy track kind = %s, want %s", got, want)
	}
}

func TestMixedOfferProductionAnswerAppliesToOfferer(t *testing.T) {
	probe, err := newPionProbe(probeOptions{includeVideo: true}, io.Discard)
	if err != nil {
		t.Fatalf("new mixed probe: %v", err)
	}
	defer probe.Close()

	offerContext, cancelOffer := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelOffer()
	offer, err := probe.Offer(offerContext)
	if err != nil {
		t.Fatalf("create mixed offer: %v", err)
	}
	server, err := rtc.NewServer(rtc.Options{
		PublicIP:        net.ParseIP("127.0.0.1"),
		UDPPortMin:      46200,
		UDPPortMax:      46300,
		Recordings:      recording.NewFactory(t.TempDir(), time.Now),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		AnswerTimeout:   5 * time.Second,
		DisconnectGrace: time.Second,
	})
	if err != nil {
		t.Fatalf("new RTC server: %v", err)
	}
	serverSession, answer, err := server.NewSession(context.Background(), "rtc-probe-regression", offer, func() {})
	if err != nil {
		t.Fatalf("create server session: %v", err)
	}
	defer serverSession.Close()

	video := sdpMediaSection(t, answer, "video")
	if got, want := strings.Fields(video[0]), []string{"m=video", "0", "UDP/TLS/RTP/SAVPF", "102"}; !slices.Equal(got, want) {
		t.Fatalf("rejected video m-line fields = %v, want %v", got, want)
	}
	if got := countSDPLine(video, "a=rtpmap:102 H264/90000"); got != 1 {
		t.Fatalf("selected exact H264 mapping count = %d, want 1", got)
	}
	for _, forbiddenPrefix := range []string{"a=rtpmap:112 ", "a=fmtp:", "a=rtcp-fb:"} {
		for _, line := range video {
			if strings.HasPrefix(line, forbiddenPrefix) {
				t.Fatalf("rejected video unexpectedly contains %q line: %q", forbiddenPrefix, line)
			}
		}
	}

	err = probe.peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer})
	if err != nil {
		t.Fatalf("SetRemoteDescription: %v", err)
	}
	if got, want := probe.peer.SignalingState(), webrtc.SignalingStateStable; got != want {
		t.Fatalf("offerer signaling state = %s, want %s", got, want)
	}
}

func TestMixedOfferProductionAudioPrimeProducesOpusRTP(t *testing.T) {
	testMixedOfferProductionAudioPrimeProducesOpusRTP(t, "silence", true)
}

func TestMixedOfferProductionAudioPrimeToneProducesOpusRTP(t *testing.T) {
	testMixedOfferProductionAudioPrimeProducesOpusRTP(t, "tone", false)
}

func testMixedOfferProductionAudioPrimeProducesOpusRTP(t *testing.T, mode string, wantSilence bool) {
	t.Helper()
	probe, err := newPionProbe(probeOptions{includeVideo: true}, io.Discard)
	if err != nil {
		t.Fatalf("new mixed probe: %v", err)
	}
	defer probe.Close()

	packetResults := make(chan audioPrimePacketResult, 2)
	probe.peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		go func() {
			for range 2 {
				packet, _, readErr := track.ReadRTP()
				packetResults <- audioPrimePacketResult{packet: packet, err: readErr}
				if readErr != nil {
					return
				}
			}
		}()
	})

	offerContext, cancelOffer := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelOffer()
	offer, err := probe.Offer(offerContext)
	if err != nil {
		t.Fatalf("create mixed offer: %v", err)
	}
	if got := countAllSDPMediaSections(offer); got != 2 {
		t.Fatalf("mixed offer media section count = %d, want exactly 2", got)
	}

	server, err := rtc.NewServer(rtc.Options{
		PublicIP:           net.ParseIP("127.0.0.1"),
		UDPPortMin:         46310,
		UDPPortMax:         46410,
		AudioPrimeEnabled:  true,
		AudioPrimeDuration: 40 * time.Millisecond,
		AudioPrimeMode:     mode,
		Recordings:         recording.NewFactory(t.TempDir(), time.Now),
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		AnswerTimeout:      5 * time.Second,
		DisconnectGrace:    time.Second,
	})
	if err != nil {
		t.Fatalf("new RTC server: %v", err)
	}
	serverSession, answer, err := server.NewSession(context.Background(), "rtc-audio-prime-regression", offer, func() {})
	if err != nil {
		t.Fatalf("create server session: %v", err)
	}
	defer serverSession.Close()

	if got := countSDPMediaSections(answer, "audio"); got != 1 {
		t.Fatalf("answer audio m-line count = %d, want 1", got)
	}
	if got := countAllSDPMediaSections(answer); got != 2 {
		t.Fatalf("answer media section count = %d, want exactly 2", got)
	}
	audioFields := strings.Fields(sdpMediaSection(t, answer, "audio")[0])
	if len(audioFields) != 4 || audioFields[1] == "0" || audioFields[3] != "111" {
		t.Fatalf("accepted audio m-line fields = %v, want one active Opus payload 111", audioFields)
	}
	videoFields := strings.Fields(sdpMediaSection(t, answer, "video")[0])
	if len(videoFields) < 2 || videoFields[1] != "0" {
		t.Fatalf("rejected video m-line fields = %v, want port 0", videoFields)
	}

	if err := probe.peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("SetRemoteDescription: %v", err)
	}

	packets := make([]*rtp.Packet, 0, 2)
	deadline := time.After(5 * time.Second)
	for len(packets) < 2 {
		select {
		case result := <-packetResults:
			if result.err != nil {
				t.Fatalf("read primed RTP: %v", result.err)
			}
			packets = append(packets, result.packet)
		case <-deadline:
			t.Fatalf("received %d primed RTP packets, want 2", len(packets))
		}
	}
	for packetIndex, packet := range packets {
		if packet.PayloadType != 111 {
			t.Fatalf("packet %d payload type = %d, want 111", packetIndex, packet.PayloadType)
		}
		isSilence := bytes.Equal(packet.Payload, []byte{0xf8, 0xff, 0xfe})
		if isSilence != wantSilence {
			t.Fatalf("packet %d silence=%t, want %t (payload length=%d)", packetIndex, isSilence, wantSilence, len(packet.Payload))
		}
	}
	if got := packets[1].Timestamp - packets[0].Timestamp; got != 960 {
		t.Fatalf("RTP timestamp delta = %d, want 960", got)
	}
}

type audioPrimePacketResult struct {
	packet *rtp.Packet
	err    error
}

func countSDPMediaSections(raw, media string) int {
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "m="+media+" ") {
			count++
		}
	}
	return count
}

func countAllSDPMediaSections(raw string) int {
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "m=") {
			count++
		}
	}
	return count
}

func countSDPLine(lines []string, want string) int {
	count := 0
	for _, line := range lines {
		if line == want {
			count++
		}
	}
	return count
}

func sdpMediaSection(t *testing.T, raw, media string) []string {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "m="+media+" ") {
			start = i
			break
		}
	}
	if start == -1 {
		t.Fatalf("SDP has no %s media section", media)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "m=") {
			end = i
			break
		}
	}
	return lines[start:end]
}

func sdpHasLine(raw, want string) bool {
	return linesContain(strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n"), want)
}

func linesContain(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func TestRunRejectsMissingConfigurationWithoutLeakingValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "missing URL", env: map[string]string{"RTC_SERVER_TOKEN": "token-secret"}},
		{name: "missing token", env: map[string]string{"RTC_SERVER_URL": "https://server-secret.example", "RTC_PROBE_STUN_URL": "stun:stun-secret.example:3478"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			code := run(context.Background(), mapGetenv(tt.env), &output)
			if code != exitProbeFailure {
				t.Fatalf("exit code = %d, want %d", code, exitProbeFailure)
			}
			got := output.String()
			for _, secret := range []string{"token-secret", "server-secret", "stun-secret", "Bearer"} {
				if strings.Contains(got, secret) {
					t.Fatalf("output contains secret %q: %q", secret, got)
				}
			}
		})
	}
}

func TestParseIncludeVideoDefaultsAndValidates(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "unset defaults false", value: "", want: false},
		{name: "explicit false", value: "false", want: false},
		{name: "explicit true", value: "true", want: true},
		{name: "invalid", value: "video-secret-value", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIncludeVideo(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error present = %v, want %v", err != nil, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("include video = %v, want %v", got, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), tt.value) {
				t.Fatalf("error leaks invalid configuration value: %q", err)
			}
		})
	}
}

func TestRunMixedVideoLoggingDoesNotLeakRequestResponseOrAuthorizationData(t *testing.T) {
	const (
		tokenSecret  = "token-secret-value"
		answerSecret = "answer-secret-value"
	)
	var capturedOffer string
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body struct {
				OfferSDP string `json:"offerSdp"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode POST: %v", err)
			}
			capturedOffer = body.OfferSDP
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"answerSdp": "v=0\r\na=ice-ufrag:" + answerSecret + "\r\na=fingerprint:sha-256 " + answerSecret + "\r\n",
			})
		case http.MethodDelete:
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	code := run(context.Background(), mapGetenv(map[string]string{
		"RTC_SERVER_URL":          server.URL,
		"RTC_SERVER_TOKEN":        tokenSecret,
		"RTC_PROBE_INCLUDE_VIDEO": "true",
	}), &output)
	if code != exitProbeFailure {
		t.Fatalf("exit code = %d, want %d", code, exitProbeFailure)
	}
	if capturedOffer == "" {
		t.Fatal("server did not receive an offer")
	}
	if !sdpHasLine(capturedOffer, "a=group:BUNDLE 0 1") || !strings.Contains(capturedOffer, "m=video 9 UDP/TLS/RTP/SAVPF 102 112") {
		t.Fatal("POSTed offer is not mixed audio/video")
	}
	if deleteCalls != 1 {
		t.Fatalf("DELETE calls = %d, want 1", deleteCalls)
	}

	got := output.String()
	wantStageOutput := "offer_created\nlocal_description_set\nice_gathering_completed\nanswer_received\nprobe_failed category=remote_description\n"
	if got != wantStageOutput {
		t.Fatalf("stage output = %q, want %q", got, wantStageOutput)
	}
	for _, forbidden := range []string{
		tokenSecret,
		answerSecret,
		server.URL,
		capturedOffer,
		"Authorization",
		"Bearer",
		"v=0",
		"a=ice-ufrag:",
		"a=candidate:",
		"a=fingerprint:",
	} {
		if forbidden != "" && strings.Contains(got, forbidden) {
			t.Fatalf("output contains forbidden value %q: %q", forbidden, got)
		}
	}
}

func mapGetenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
