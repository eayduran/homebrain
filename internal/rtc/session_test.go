package rtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"home-brain-rtc/internal/recording"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type offerFixture struct {
	peer  *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticRTP
	offer string
}

type synchronizedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type terminalDuringCreateFactory struct {
	logger *slog.Logger
}

func (f terminalDuringCreateFactory) NewSession(_ context.Context, id, _ string, onTerminal func()) (*Session, string, error) {
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{id: id, peer: peer, ctx: ctx, cancel: cancel, logger: f.logger}
	onTerminal()
	return session, "answer", nil
}

type blockingCreateFactory struct {
	logger  *slog.Logger
	started chan struct{}
	release chan struct{}
}

func (f blockingCreateFactory) NewSession(_ context.Context, id, _ string, onTerminal func()) (*Session, string, error) {
	close(f.started)
	<-f.release
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{id: id, peer: peer, ctx: ctx, cancel: cancel, logger: f.logger, onTerminal: onTerminal}, "answer", nil
}

func newOffer(t *testing.T, opus, video bool) offerFixture {
	t.Helper()
	mediaEngine := &webrtc.MediaEngine{}
	audioCodec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000},
		PayloadType:        0,
	}
	if opus {
		audioCodec = webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
				SDPFmtpLine: "minptime=10;useinbandfec=1",
			},
			PayloadType: 111,
		}
	}
	if err := mediaEngine.RegisterCodec(audioCodec, webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	if video {
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000},
			PayloadType:        102,
		}, webrtc.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
				SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
			},
			PayloadType: 112,
		}, webrtc.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
	}
	settings := webrtc.SettingEngine{}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settings))
	peer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(audioCodec.RTPCodecCapability, "microphone", "echo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.AddTrack(audioTrack); err != nil {
		t.Fatal(err)
	}
	if video {
		videoTrack, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000},
			"camera", "echo",
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.AddTrack(videoTrack); err != nil {
			t.Fatal(err)
		}
	}
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(3 * time.Second):
		t.Fatal("offer ICE gathering timed out")
	}
	return offerFixture{peer: peer, track: audioTrack, offer: peer.LocalDescription().SDP}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return newServerFor(t, "8.8.8.8", t.TempDir(), 50*time.Millisecond)
}

func newServerFor(t *testing.T, publicIP, recordingsDir string, disconnectGrace time.Duration) *Server {
	return newServerWithICELiteFor(t, publicIP, recordingsDir, disconnectGrace, false)
}

func newServerWithICELiteFor(t *testing.T, publicIP, recordingsDir string, disconnectGrace time.Duration, iceLite bool) *Server {
	t.Helper()
	server, err := NewServer(Options{
		PublicIP:        net.ParseIP(publicIP),
		UDPPortMin:      50000,
		UDPPortMax:      50100,
		ICELite:         iceLite,
		Recordings:      recording.NewFactory(recordingsDir, time.Now),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		AnswerTimeout:   2 * time.Second,
		DisconnectGrace: disconnectGrace,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestNewSessionICELiteModes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		iceLite bool
	}{
		{name: "normal", iceLite: false},
		{name: "Alexa ICE-Lite experiment", iceLite: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newOffer(t, true, true)
			server := newServerWithICELiteFor(t, "8.8.8.8", t.TempDir(), 50*time.Millisecond, tt.iceLite)
			session, answer, err := server.NewSession(context.Background(), "ice-mode", fixture.offer, func() {})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })

			lower := strings.ToLower(answer)
			if got := strings.Contains(lower, "a=ice-lite"); got != tt.iceLite {
				t.Fatalf("a=ice-lite present = %t, want %t\n%s", got, tt.iceLite, answer)
			}
			if strings.Contains(lower, "a=ice-options:trickle") {
				t.Fatalf("answer advertises trickle ICE:\n%s", answer)
			}
			if !tt.iceLite {
				return
			}
			for _, want := range []string{"m=audio", "opus/48000", "a=candidate:", "8.8.8.8"} {
				if !strings.Contains(lower, want) {
					t.Fatalf("ICE-Lite answer missing %q:\n%s", want, answer)
				}
			}
			videoSection := mediaSection(t, answer, "video")
			videoFields := strings.Fields(strings.SplitN(videoSection, "\r\n", 2)[0])
			if len(videoFields) != 4 || videoFields[1] != "0" || videoFields[3] != "102" {
				t.Fatalf("ICE-Lite rejected video m-line = %q, want port 0 and payload 102", videoFields)
			}
			if !strings.Contains(videoSection, "\r\na=inactive\r\n") {
				t.Fatalf("ICE-Lite rejected video section is not inactive:\n%s", videoSection)
			}
		})
	}
}

func TestICELitePeerConnectionConnectsAndClosesCleanly(t *testing.T) {
	fixture := newOffer(t, true, false)
	server := newServerWithICELiteFor(t, localReachableIPv4(t), t.TempDir(), 50*time.Millisecond, true)
	session, answer, err := server.NewSession(context.Background(), "ice-lite-connect", fixture.offer, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	connectAnswer(t, fixture.peer, answer)
	waitForPeerEstablished(t, "full offerer", fixture.peer)
	waitForPeerEstablished(t, "ICE-Lite answerer", session.peer)

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.peer.Close(); err != nil {
		t.Fatal(err)
	}
	waitForPeerClosed(t, "full offerer", fixture.peer)
	waitForPeerClosed(t, "ICE-Lite answerer", session.peer)
}

func localReachableIPv4(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ipv4 := ip.To4(); ipv4 != nil && !ipv4.IsLoopback() && !ipv4.IsUnspecified() && !ipv4.IsLinkLocalUnicast() {
				return ipv4.String()
			}
		}
	}
	t.Fatal("no active non-loopback IPv4 interface is available for the local ICE-Lite integration test")
	return ""
}

func waitForPeerEstablished(t *testing.T, name string, peer *webrtc.PeerConnection) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		peerState := peer.ConnectionState()
		iceState := peer.ICEConnectionState()
		if peerState == webrtc.PeerConnectionStateConnected ||
			iceState == webrtc.ICEConnectionStateConnected ||
			iceState == webrtc.ICEConnectionStateCompleted {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not establish; peer=%s ICE=%s", name, peer.ConnectionState(), peer.ICEConnectionState())
}

func waitForPeerClosed(t *testing.T, name string, peer *webrtc.PeerConnection) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if peer.ConnectionState() == webrtc.PeerConnectionStateClosed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s did not close; peer=%s ICE=%s", name, peer.ConnectionState(), peer.ICEConnectionState())
}

func TestNewSessionGeneratesCompleteOpusAnswer(t *testing.T) {
	fixture := newOffer(t, true, false)
	server := newTestServer(t)
	session, answer, err := server.NewSession(context.Background(), "session-1", fixture.offer, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	lower := strings.ToLower(answer)
	for _, want := range []string{"m=audio", "opus/48000", "a=candidate:", "a=rtcp-mux", "a=group:bundle", "8.8.8.8"} {
		if !strings.Contains(lower, want) {
			t.Errorf("answer missing %q", want)
		}
	}
	if strings.Contains(lower, " typ tcp ") {
		t.Fatal("answer unexpectedly contains TCP candidate")
	}
}

func TestNewSessionRejectsMalformedAndNonOpusOffers(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.NewSession(context.Background(), "bad", "not-sdp", func() {}); !errors.Is(err, ErrInvalidSDP) {
		t.Fatalf("malformed offer error = %v, want ErrInvalidSDP", err)
	}
	fixture := newOffer(t, false, false)
	if _, _, err := server.NewSession(context.Background(), "pcmu", fixture.offer, func() {}); !errors.Is(err, ErrOpusRequired) {
		t.Fatalf("non-Opus offer error = %v, want ErrOpusRequired", err)
	}
}

func TestNewSessionRejectsUnusedOpusRTPMap(t *testing.T) {
	fixture := newOffer(t, false, false)
	poisoned := strings.Replace(
		fixture.offer,
		"a=rtpmap:0 PCMU/8000\r\n",
		"a=rtpmap:0 PCMU/8000\r\na=rtpmap:111 opus/48000/2\r\n",
		1,
	)
	if poisoned == fixture.offer {
		t.Fatal("test offer did not contain the expected PCMU rtpmap")
	}
	if _, _, err := newTestServer(t).NewSession(context.Background(), "unused-opus", poisoned, func() {}); !errors.Is(err, ErrOpusRequired) {
		t.Fatalf("unused Opus rtpmap error = %v, want ErrOpusRequired", err)
	}
}

func TestNewSessionRejectsDisabledOpusAudioSection(t *testing.T) {
	fixture := newOffer(t, true, false)
	lines := strings.Split(fixture.offer, "\r\n")
	found := false
	for index, line := range lines {
		if !strings.HasPrefix(line, "m=audio ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			t.Fatalf("malformed test audio m-line: %q", line)
		}
		fields[1] = "0"
		lines[index] = strings.Join(fields, " ")
		found = true
		break
	}
	if !found {
		t.Fatal("test offer did not contain an audio m-line")
	}
	disabledAudioOffer := strings.Join(lines, "\r\n")
	if _, _, err := newTestServer(t).NewSession(context.Background(), "disabled-audio", disabledAudioOffer, func() {}); !errors.Is(err, ErrOpusRequired) {
		t.Fatalf("disabled audio error = %v, want ErrOpusRequired", err)
	}
}

func TestNewSessionKeepsAudioWhenOfferContainsVideo(t *testing.T) {
	fixture := newOffer(t, true, true)
	offerVideoLine := mediaLine(t, fixture.offer, "video")
	if formats := strings.Fields(offerVideoLine)[3:]; !equalStrings(formats, []string{"102", "112"}) {
		t.Fatalf("Alexa mixed offer video payloads = %v, want [102 112]", formats)
	}
	session, answer, err := newTestServer(t).NewSession(context.Background(), "mixed", fixture.offer, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	lower := strings.ToLower(answer)
	if !strings.Contains(lower, "m=audio") || !strings.Contains(lower, "opus/48000") {
		t.Fatalf("audio was not accepted:\n%s", answer)
	}
	videoSection := mediaSection(t, answer, "video")
	videoFields := strings.Fields(strings.SplitN(videoSection, "\r\n", 2)[0])
	if len(videoFields) != 4 || videoFields[1] != "0" || videoFields[3] != "102" {
		t.Fatalf("Alexa rejected video m-line = %q, want port 0 and sole offered payload 102", videoFields)
	}
	if !strings.Contains(videoSection, "\r\na=inactive\r\n") {
		t.Fatalf("Alexa rejected video section is not inactive:\n%s", videoSection)
	}
	for _, sender := range session.peer.GetSenders() {
		if track := sender.Track(); track != nil && track.Kind() == webrtc.RTPCodecTypeVideo {
			t.Fatal("Alexa normalization unexpectedly added an active local video track")
		}
	}
}

func TestAlexaRejectedVideoPayloadNormalizationPreservesAudioAndCandidates(t *testing.T) {
	offer := strings.Join([]string{
		"v=0",
		"o=- 1 1 IN IP4 0.0.0.0",
		"s=-",
		"t=0 0",
		"a=group:BUNDLE 0 1",
		"m=audio 9 UDP/TLS/RTP/SAVPF 111",
		"c=IN IP4 0.0.0.0",
		"a=mid:0",
		"a=rtpmap:111 opus/48000/2",
		"m=video 9 UDP/TLS/RTP/SAVPF 102 112",
		"c=IN IP4 0.0.0.0",
		"a=mid:1",
		"a=rtpmap:102 VP8/90000",
		"a=rtpmap:112 H264/90000",
		"",
	}, "\r\n")
	rawAnswer := strings.Join([]string{
		"v=0",
		"o=- 2 2 IN IP4 0.0.0.0",
		"s=-",
		"t=0 0",
		"a=group:BUNDLE 0 1",
		"m=audio 40000 UDP/TLS/RTP/SAVPF 111",
		"c=IN IP4 8.8.8.8",
		"a=mid:0",
		"a=ice-ufrag:answerUfrag",
		"a=ice-pwd:answerPassword",
		"a=fingerprint:sha-256 AA:BB:CC",
		"a=setup:active",
		"a=candidate:1 1 UDP 2130706431 8.8.8.8 40000 typ host",
		"a=rtpmap:111 opus/48000/2",
		"a=sendrecv",
		"m=video  0  UDP/TLS/RTP/SAVPF  0",
		"c=IN IP4 0.0.0.0",
		"a=mid:1",
		"a=ice-ufrag:answerUfrag",
		"a=ice-pwd:answerPassword",
		"a=fingerprint:sha-256 AA:BB:CC",
		"a=setup:active",
		"a=recvonly",
		"",
	}, "\r\n")

	normalized := normalizeAlexaRejectedVideoPayloads(offer, rawAnswer)
	if got := mediaLine(t, normalized, "video"); got != "m=video  0  UDP/TLS/RTP/SAVPF  102" {
		t.Fatalf("normalized video m-line = %q", got)
	}
	if !strings.Contains(mediaSection(t, normalized, "video"), "\r\na=inactive\r\n") {
		t.Fatalf("normalized video section is not inactive:\n%s", normalized)
	}
	if got, want := mediaSection(t, normalized, "audio"), mediaSection(t, rawAnswer, "audio"); got != want {
		t.Fatalf("audio section changed during Alexa normalization\nwant:\n%s\ngot:\n%s", want, got)
	}
	if got, want := linesWithPrefix(normalized, "a=candidate:"), linesWithPrefix(rawAnswer, "a=candidate:"); !equalStrings(got, want) {
		t.Fatalf("candidate lines changed during Alexa normalization: got %v want %v", got, want)
	}
	expected := strings.Replace(rawAnswer, "m=video  0  UDP/TLS/RTP/SAVPF  0", "m=video  0  UDP/TLS/RTP/SAVPF  102", 1)
	expected = strings.Replace(expected, "a=recvonly", "a=inactive", 1)
	if normalized != expected {
		t.Fatalf("Alexa normalization changed unrelated SDP\nwant:\n%s\ngot:\n%s", expected, normalized)
	}

	fallbackOffer := strings.Replace(offer, "m=video 9 UDP/TLS/RTP/SAVPF 102 112", "m=video 9 UDP/TLS/RTP/SAVPF 112", 1)
	emptyPayloadAnswer := strings.Replace(rawAnswer, "m=video  0  UDP/TLS/RTP/SAVPF  0", "m=video  0  UDP/TLS/RTP/SAVPF", 1)
	fallback := normalizeAlexaRejectedVideoPayloads(fallbackOffer, emptyPayloadAnswer)
	if got := mediaLine(t, fallback, "video"); got != "m=video  0  UDP/TLS/RTP/SAVPF 112" {
		t.Fatalf("fallback video m-line = %q, want first offered payload 112", got)
	}
}

func TestAlexaRTCPMuxCandidateNormalizationFromPionAnswer(t *testing.T) {
	rawAnswer := newPionAnswerWithRTCPMuxCandidates(t)
	rawCandidates := candidateLinesInFirstAudioSection(t, rawAnswer)
	component1 := candidateLinesForComponent(rawCandidates, "1")
	component2 := candidateLinesForComponent(rawCandidates, "2")
	if len(component1) != 1 || len(component2) != 1 {
		t.Fatalf("real Pion answer candidates = component-1 %d, component-2 %d; want exactly one each\n%s", len(component1), len(component2), rawAnswer)
	}

	normalized := normalizeAlexaRTCPMuxCandidates(rawAnswer)
	normalizedCandidates := candidateLinesInFirstAudioSection(t, normalized)
	if got := candidateLinesForComponent(normalizedCandidates, "1"); !equalStrings(got, component1) {
		t.Fatalf("component-1 candidate changed: got %q want %q", got, component1)
	}
	if got := candidateLinesForComponent(normalizedCandidates, "2"); len(got) != 0 {
		t.Fatalf("component-2 candidates remain after Alexa normalization: %q", got)
	}
	want := strings.Replace(rawAnswer, component2[0]+"\r\n", "", 1)
	if normalized != want {
		t.Fatalf("Alexa RTCP-mux normalization changed bytes other than the component-2 line\nwant:\n%s\ngot:\n%s", want, normalized)
	}
}

func TestAlexaRTCPMuxCandidateNormalizationPreservesFramingAndOtherSections(t *testing.T) {
	inputLines := []string{
		"v=0",
		"a=group:BUNDLE 0 1 2 3",
		"m=audio 40000 UDP/TLS/RTP/SAVPF 111",
		"a=mid:0",
		"a=rtcp-mux",
		"a=candidate:first\t1 udp 100 203.0.113.1 40000 typ host generation 0",
		"a=candidate:dropone  2 udp 90 203.0.113.1 40001 typ host",
		"a=candidate:second 1 UDP 80 203.0.113.2 40002 typ srflx raddr 10.0.0.1 rport 5000",
		"a=candidate:droptwo\t2\tudp 70 203.0.113.2 40003 typ host",
		"a=candidate:not-two 12 udp 60 203.0.113.3 40004 typ host",
		"a=candidate:also-not-two 20 udp 50 203.0.113.4 40005 typ host",
		"a=candidate:malformed 2",
		"a=candidate:wrongmarker 2 udp 40 203.0.113.5 40006 type host",
		"a=ice-ufrag:unchanged",
		"a=fingerprint:sha-256 AA:BB",
		"m=audio 41000 UDP/TLS/RTP/SAVPF 111",
		"a=mid:1",
		"a=candidate:non-mux 2 udp 40 203.0.113.5 41001 typ host",
		"m=audio 0 UDP/TLS/RTP/SAVPF 111",
		"a=mid:2",
		"a=rtcp-mux",
		"a=candidate:rejected 2 udp 30 203.0.113.6 42001 typ host",
		"m=video 0 UDP/TLS/RTP/SAVPF 102",
		"a=mid:3",
		"a=rtcp-mux",
		"a=candidate:video 2 udp 20 203.0.113.7 43001 typ host",
		"a=inactive",
	}
	wantLines := []string{
		"v=0",
		"a=group:BUNDLE 0 1 2 3",
		"m=audio 40000 UDP/TLS/RTP/SAVPF 111",
		"a=mid:0",
		"a=rtcp-mux",
		"a=candidate:first\t1 udp 100 203.0.113.1 40000 typ host generation 0",
		"a=candidate:second 1 UDP 80 203.0.113.2 40002 typ srflx raddr 10.0.0.1 rport 5000",
		"a=candidate:not-two 12 udp 60 203.0.113.3 40004 typ host",
		"a=candidate:also-not-two 20 udp 50 203.0.113.4 40005 typ host",
		"a=candidate:malformed 2",
		"a=candidate:wrongmarker 2 udp 40 203.0.113.5 40006 type host",
		"a=ice-ufrag:unchanged",
		"a=fingerprint:sha-256 AA:BB",
		"m=audio 41000 UDP/TLS/RTP/SAVPF 111",
		"a=mid:1",
		"a=candidate:non-mux 2 udp 40 203.0.113.5 41001 typ host",
		"m=audio 0 UDP/TLS/RTP/SAVPF 111",
		"a=mid:2",
		"a=rtcp-mux",
		"a=candidate:rejected 2 udp 30 203.0.113.6 42001 typ host",
		"m=video 0 UDP/TLS/RTP/SAVPF 102",
		"a=mid:3",
		"a=rtcp-mux",
		"a=candidate:video 2 udp 20 203.0.113.7 43001 typ host",
		"a=inactive",
	}

	for _, tt := range []struct {
		name         string
		lineEnding   string
		finalNewline bool
	}{
		{name: "CRLF with final newline", lineEnding: "\r\n", finalNewline: true},
		{name: "LF without final newline", lineEnding: "\n", finalNewline: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Join(inputLines, tt.lineEnding)
			want := strings.Join(wantLines, tt.lineEnding)
			if tt.finalNewline {
				input += tt.lineEnding
				want += tt.lineEnding
			}

			got := normalizeAlexaRTCPMuxCandidates(input)
			if got != want {
				t.Fatalf("Alexa RTCP-mux normalization did not preserve framing or unrelated bytes\nwant: %q\ngot:  %q", want, got)
			}
			if strings.HasSuffix(got, tt.lineEnding) != tt.finalNewline {
				t.Fatalf("final newline state changed: got suffix=%t want %t", strings.HasSuffix(got, tt.lineEnding), tt.finalNewline)
			}
			gotComponent1 := candidateLinesForComponent(candidateLinesInFirstAudioSection(t, got), "1")
			wantComponent1 := []string{inputLines[5], inputLines[7]}
			if !equalStrings(gotComponent1, wantComponent1) {
				t.Fatalf("component-1 candidates changed or reordered: got %q want %q", gotComponent1, wantComponent1)
			}
			for _, removedCandidate := range []string{inputLines[6], inputLines[8]} {
				if strings.Contains(got, removedCandidate) {
					t.Fatalf("valid component-2 candidate remains in accepted muxed audio: %q", removedCandidate)
				}
			}
		})
	}
}

func TestExtractICESDPLogMetadata(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want iceSDPLogMetadata
	}{
		{
			name: "CRLF exact session-level attributes",
			raw: strings.Join([]string{
				"v=0",
				"o=- 1 1 IN IP4 0.0.0.0",
				"s=-",
				"t=0 0",
				"a=group:BUNDLE audio video data",
				"a=group:BUNDLE ignored",
				"a=ice-lite",
				"a=ice-options:renomination trickle",
				"m=audio 9 UDP/TLS/RTP/SAVPF 111",
				"a=group:BUNDLE media-level-ignored",
				"a=ice-lite",
				"a=ice-options:trickle",
				"",
			}, "\r\n"),
			want: iceSDPLogMetadata{
				HasIceLite: true,
				HasTrickle: true,
				BundleMIDs: []string{"audio", "video", "data"},
			},
		},
		{
			name: "LF safe defaults and exact tokens",
			raw: strings.Join([]string{
				"v=0",
				"a=ice-lite-extra",
				"a=ice-options:nottrickle trickle2",
				"a=group:BUNDLE",
				"m=audio 9 UDP/TLS/RTP/SAVPF 111",
				"a=group:BUNDLE media-mid",
				"a=ice-lite",
				"a=ice-options:trickle",
			}, "\n"),
			want: iceSDPLogMetadata{BundleMIDs: []string{}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := extractICESDPLogMetadata(tt.raw)
			if got.HasIceLite != tt.want.HasIceLite {
				t.Fatalf("HasIceLite = %t, want %t", got.HasIceLite, tt.want.HasIceLite)
			}
			if got.HasTrickle != tt.want.HasTrickle {
				t.Fatalf("HasTrickle = %t, want %t", got.HasTrickle, tt.want.HasTrickle)
			}
			if got.BundleMIDs == nil {
				t.Fatal("BundleMIDs = nil, want non-nil empty-or-populated list")
			}
			if !equalStrings(got.BundleMIDs, tt.want.BundleMIDs) {
				t.Fatalf("BundleMIDs = %q, want %q", got.BundleMIDs, tt.want.BundleMIDs)
			}
		})
	}
}

func TestICESDPLogMetadataEmptyBundleMIDsSerializeAsArrays(t *testing.T) {
	metadata := extractICESDPLogMetadata("v=0\nm=audio 9 UDP/TLS/RTP/SAVPF 111")
	var logs synchronizedBuffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info(
		"sdp_answer_generated",
		"offerBundleMids", metadata.BundleMIDs,
		"answerBundleMids", metadata.BundleMIDs,
	)
	var record map[string]json.RawMessage
	if err := json.Unmarshal([]byte(logs.String()), &record); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"offerBundleMids", "answerBundleMids"} {
		if got := string(record[key]); got != "[]" {
			t.Fatalf("%s JSON = %s, want []", key, got)
		}
	}
}

func TestNewSessionLogsICESDPMetadata(t *testing.T) {
	fixture := newOffer(t, true, false)
	offer := strings.Replace(
		fixture.offer,
		"\r\nm=audio ",
		"\r\na=ice-options:renomination trickle\r\nm=audio ",
		1,
	)
	if offer == fixture.offer {
		t.Fatal("test offer did not contain the expected audio m-line")
	}

	var logs synchronizedBuffer
	server, err := NewServer(Options{
		PublicIP:        net.ParseIP("8.8.8.8"),
		UDPPortMin:      50000,
		UDPPortMax:      50100,
		ICELite:         true,
		Recordings:      recording.NewFactory(t.TempDir(), time.Now),
		Logger:          slog.New(slog.NewJSONHandler(&logs, nil)),
		AnswerTimeout:   2 * time.Second,
		DisconnectGrace: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, answer, err := server.NewSession(context.Background(), "ice-log-session", offer, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	var record struct {
		Message          string   `json:"msg"`
		SessionID        string   `json:"sessionId"`
		OfferBytes       int      `json:"offerBytes"`
		AnswerBytes      int      `json:"answerBytes"`
		OfferHasIceLite  bool     `json:"offerHasIceLite"`
		AnswerHasIceLite bool     `json:"answerHasIceLite"`
		OfferHasTrickle  bool     `json:"offerHasTrickle"`
		AnswerHasTrickle bool     `json:"answerHasTrickle"`
		OfferBundleMIDs  []string `json:"offerBundleMids"`
		AnswerBundleMIDs []string `json:"answerBundleMids"`
	}
	found := false
	var rawRecord map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var candidateRecord struct {
			Message string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &candidateRecord); err != nil {
			t.Fatal(err)
		}
		if candidateRecord.Message != "sdp_answer_generated" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(line), &rawRecord); err != nil {
			t.Fatal(err)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("sdp_answer_generated log record missing")
	}
	for _, key := range []string{
		"offerHasIceLite",
		"answerHasIceLite",
		"offerHasTrickle",
		"answerHasTrickle",
	} {
		raw, exists := rawRecord[key]
		if !exists {
			t.Fatalf("log metadata key %q missing", key)
		}
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("log metadata key %q is not a boolean: %v", key, err)
		}
	}
	for _, key := range []string{"offerBundleMids", "answerBundleMids"} {
		raw, exists := rawRecord[key]
		if !exists {
			t.Fatalf("log metadata key %q missing", key)
		}
		var value []string
		if string(raw) == "null" {
			t.Fatalf("log metadata key %q is null, want string array", key)
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("log metadata key %q is not a string array: %v", key, err)
		}
	}
	if record.SessionID != "ice-log-session" || record.OfferBytes != len(offer) || record.AnswerBytes != len(answer) {
		t.Fatalf("existing log metadata changed: %#v", record)
	}
	if record.OfferHasIceLite || !record.AnswerHasIceLite {
		t.Fatalf("ICE-Lite metadata = offer %t answer %t, want false/true", record.OfferHasIceLite, record.AnswerHasIceLite)
	}
	if !record.OfferHasTrickle || record.AnswerHasTrickle {
		t.Fatalf("trickle metadata = offer %t answer %t, want true/false", record.OfferHasTrickle, record.AnswerHasTrickle)
	}
	if !equalStrings(record.OfferBundleMIDs, []string{"0"}) || !equalStrings(record.AnswerBundleMIDs, []string{"0"}) {
		t.Fatalf("BUNDLE MIDs = offer %q answer %q, want [0]/[0]", record.OfferBundleMIDs, record.AnswerBundleMIDs)
	}
	serializedLogs := logs.String()
	for _, forbidden := range []string{
		"a=ice-lite",
		"a=ice-options:",
		"a=group:BUNDLE",
		"a=candidate:",
		"a=ice-ufrag:",
		"a=ice-pwd:",
		"a=fingerprint:",
	} {
		if strings.Contains(serializedLogs, forbidden) {
			t.Fatalf("logs contain forbidden SDP content %q", forbidden)
		}
	}
}

func newPionAnswerWithRTCPMuxCandidates(t *testing.T) string {
	t.Helper()
	fixture := newOffer(t, true, false)
	mediaEngine := &webrtc.MediaEngine{}
	opus := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}
	if err := mediaEngine.RegisterCodec(opus, webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	settings := webrtc.SettingEngine{}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.To4() != nil && ip.IsLoopback() })
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settings))
	peer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	track, err := webrtc.NewTrackLocalStaticRTP(opus.RTPCodecCapability, "speaker", "home-brain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = peer.AddTrack(track); err != nil {
		t.Fatal(err)
	}
	if err = peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: fixture.offer}); err != nil {
		t.Fatal(err)
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err = peer.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-time.After(3 * time.Second):
		t.Fatal("answer ICE gathering timed out")
	}
	return peer.LocalDescription().SDP
}

func candidateLinesInFirstAudioSection(t *testing.T, raw string) []string {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	inAudio := false
	var candidates []string
	for _, line := range lines {
		if strings.HasPrefix(line, "m=") {
			if inAudio {
				break
			}
			inAudio = strings.HasPrefix(line, "m=audio ")
			continue
		}
		if inAudio && strings.HasPrefix(line, "a=candidate:") {
			candidates = append(candidates, line)
		}
	}
	if !inAudio {
		t.Fatal("SDP missing audio media section")
	}
	return candidates
}

func candidateLinesForComponent(candidates []string, component string) []string {
	var matches []string
	for _, candidate := range candidates {
		fields := strings.Fields(candidate)
		if len(fields) >= 2 && fields[1] == component {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func mediaLine(t *testing.T, raw, kind string) string {
	t.Helper()
	prefix := "m=" + kind + " "
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("SDP missing %s m-line", kind)
	return ""
}

func mediaSection(t *testing.T, raw, kind string) string {
	t.Helper()
	start := strings.Index(raw, "m="+kind+" ")
	if start < 0 {
		t.Fatalf("SDP missing %s media section", kind)
	}
	rest := raw[start:]
	if next := strings.Index(rest[2:], "\r\nm="); next >= 0 {
		return rest[:next+2]
	}
	return rest
}

func linesWithPrefix(raw, prefix string) []string {
	var matches []string
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, prefix) {
			matches = append(matches, line)
		}
	}
	return matches
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestNewSessionHonorsCanceledContext(t *testing.T) {
	fixture := newOffer(t, true, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := newTestServer(t).NewSession(ctx, "canceled", fixture.offer, func() {}); !errors.Is(err, ErrAnswerTimeout) {
		t.Fatalf("error = %v, want ErrAnswerTimeout", err)
	}
}

func connectAnswer(t *testing.T, peer *webrtc.PeerConnection, answer string) {
	t.Helper()
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{})
	var once sync.Once
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			once.Do(func() { close(connected) })
		}
	})
	if peer.ConnectionState() == webrtc.PeerConnectionStateConnected {
		once.Do(func() { close(connected) })
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatalf("peer did not connect; state=%s ICE=%s", peer.ConnectionState(), peer.ICEConnectionState())
	}
}

func testOpusPacket(sequence uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 111, SequenceNumber: sequence,
			Timestamp: uint32(sequence) * 960, SSRC: 0x99887766,
		},
		Payload: []byte{0xf8, 0xff, 0xfe},
	}
}

func waitForOgg(t *testing.T, dir string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(dir, "*.ogg"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 1 {
			return matches[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recording file was not created")
	return ""
}

func TestRemoteOpusCreatesOggWithoutConnectedDirective(t *testing.T) {
	recordingsDir := t.TempDir()
	fixture := newOffer(t, true, false)
	manager := NewManager(newServerFor(t, "127.0.0.1", recordingsDir, 50*time.Millisecond), time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	answer, err := manager.Create(context.Background(), "no-connected", fixture.offer)
	if err != nil {
		t.Fatal(err)
	}
	connectAnswer(t, fixture.peer, answer)
	for sequence := uint16(1); sequence <= 3; sequence++ {
		if err := fixture.track.WriteRTP(testOpusPacket(sequence)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	path := waitForOgg(t, recordingsDir)
	if err := manager.Close("no-connected"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("OggS")) || len(data) <= 64 {
		t.Fatalf("invalid finalized OGG recording: %d bytes", len(data))
	}
}

func TestManagerRejectsDuplicateAndConnectedIsIdempotent(t *testing.T) {
	fixture := newOffer(t, true, false)
	manager := NewManager(newTestServer(t), time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := manager.Create(context.Background(), "duplicate", fixture.offer); err != nil {
		t.Fatal(err)
	}
	defer manager.CloseAll()
	if _, err := manager.Create(context.Background(), "duplicate", fixture.offer); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate error = %v, want ErrSessionExists", err)
	}
	if err := manager.MarkConnected("duplicate"); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkConnected("duplicate"); err != nil {
		t.Fatalf("second connected marker failed: %v", err)
	}
}

func TestManagerClosesExpiredSession(t *testing.T) {
	fixture := newOffer(t, true, false)
	manager := NewManager(newTestServer(t), 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := manager.Create(context.Background(), "expires", fixture.offer); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for manager.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if manager.Len() != 0 {
		t.Fatalf("TTL did not remove session; len=%d", manager.Len())
	}
	if err := manager.Close("expires"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("close expired error = %v, want ErrSessionNotFound", err)
	}
}

func TestManagerConcurrentAccess(t *testing.T) {
	fixture := newOffer(t, true, false)
	manager := NewManager(newTestServer(t), time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := manager.Create(context.Background(), "race", fixture.offer); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = manager.MarkConnected("race")
			_ = manager.Len()
		}()
	}
	wg.Wait()
	if err := manager.Close("race"); err != nil {
		t.Fatal(err)
	}
	if manager.Len() != 0 {
		t.Fatalf("manager still contains closed session")
	}
}

func TestStaleExpiryDoesNotCloseRecreatedSession(t *testing.T) {
	fixture := newOffer(t, true, false)
	manager := NewManager(newTestServer(t), time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := manager.Create(context.Background(), "reused", fixture.offer); err != nil {
		t.Fatal(err)
	}
	manager.mu.RLock()
	oldSession := manager.sessions["reused"].session
	manager.mu.RUnlock()
	if err := manager.Close("reused"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), "reused", fixture.offer); err != nil {
		t.Fatal(err)
	}
	defer manager.CloseAll()

	manager.expire("reused", oldSession)
	if err := manager.MarkConnected("reused"); err != nil {
		t.Fatalf("stale expiry removed recreated session: %v", err)
	}
}

func TestDisconnectGraceIsCanceledByLaterPeerState(t *testing.T) {
	for _, recoveredState := range []webrtc.PeerConnectionState{
		webrtc.PeerConnectionStateConnecting,
		webrtc.PeerConnectionStateConnected,
	} {
		t.Run(recoveredState.String(), func(t *testing.T) {
			fixture := newOffer(t, true, false)
			manager := NewManager(newServerFor(t, "8.8.8.8", t.TempDir(), 20*time.Millisecond), time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if _, err := manager.Create(context.Background(), "disconnect", fixture.offer); err != nil {
				t.Fatal(err)
			}
			defer manager.CloseAll()
			manager.mu.RLock()
			session := manager.sessions["disconnect"].session
			manager.mu.RUnlock()

			session.handlePeerConnectionState(webrtc.PeerConnectionStateDisconnected)
			session.handlePeerConnectionState(recoveredState)
			time.Sleep(50 * time.Millisecond)
			if err := manager.MarkConnected("disconnect"); err != nil {
				t.Fatalf("recovered peer was closed by stale disconnect timer: %v", err)
			}
		})
	}
}

func TestTerminalCallbackDuringCreationDoesNotPublishClosedSession(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(terminalDuringCreateFactory{logger: logger}, time.Minute, logger)
	if _, err := manager.Create(context.Background(), "terminal", "offer"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("create error = %v, want ErrSessionNotFound", err)
	}
	if manager.Len() != 0 {
		t.Fatalf("terminal session was published; len=%d", manager.Len())
	}
}

func TestMarkConnectedWhileCreateCompletesIsRaceSafe(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	factory := blockingCreateFactory{logger: logger, started: make(chan struct{}), release: make(chan struct{})}
	manager := NewManager(factory, time.Minute, logger)
	createResult := make(chan error, 1)
	go func() {
		_, err := manager.Create(context.Background(), "concurrent-create", "offer")
		createResult <- err
	}()
	<-factory.started
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = manager.MarkConnected("concurrent-create")
			}
		}
	}()
	close(factory.release)
	if err := <-createResult; err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	defer manager.CloseAll()
	if err := manager.MarkConnected("concurrent-create"); err != nil {
		t.Fatal(err)
	}
}
