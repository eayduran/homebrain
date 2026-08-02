package rtc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"home-brain-rtc/internal/recording"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func TestAudioPrimeSilenceModePreservesExistingPayload(t *testing.T) {
	frames := audioPrimeFramesForMode(audioPrimeModeSilence)
	if len(frames) != 1 {
		t.Fatalf("silence frame count = %d, want 1", len(frames))
	}
	if got, want := frames[0], []byte{0xf8, 0xff, 0xfe}; !bytes.Equal(got, want) {
		t.Fatalf("silence payload = %x, want %x", got, want)
	}
}

func TestAudioPrimeToneModeUsesPinnedPreEncodedOpusFrames(t *testing.T) {
	frames := audioPrimeFramesForMode(audioPrimeModeTone)
	if len(frames) != 5 {
		t.Fatalf("tone frame count = %d, want 5", len(frames))
	}
	hash := sha256.New()
	for frameIndex, frame := range frames {
		if len(frame) != 160 {
			t.Fatalf("tone frame %d length = %d, want 160", frameIndex, len(frame))
		}
		if frame[0] != 0xfc {
			t.Fatalf("tone frame %d Opus TOC = 0x%02x, want 0xfc (single stereo fullband CELT 20ms frame)", frameIndex, frame[0])
		}
		if bytes.Equal(frame, opusSilenceFrame) {
			t.Fatalf("tone frame %d equals the Opus silence payload", frameIndex)
		}
		if len(frame) > 1200 {
			t.Fatalf("tone frame %d length = %d, exceeds RTP-safe fixture limit", frameIndex, len(frame))
		}
		_, _ = hash.Write(frame)
	}
	if got, want := fmt.Sprintf("%x", hash.Sum(nil)), "a634240687b11343b2b1e60e64cb91005c625a0ba9f3e44e0f948d04b811888c"; got != want {
		t.Fatalf("tone fixture hash = %s, want %s", got, want)
	}
}

func TestRunAudioPrimeCyclesEncodedFrameSequence(t *testing.T) {
	ticker := newManualAudioPrimeTicker(3)
	for range 3 {
		ticker.ticks <- time.Now()
	}
	writer := &recordingAudioPrimeWriter{}
	sequence := [][]byte{{0x01}, {0x02}}

	frames, reason, err := runAudioPrime(context.Background(), writer, ticker, 3, sequence)
	if err != nil {
		t.Fatal(err)
	}
	if frames != 3 || reason != audioPrimeReasonDuration {
		t.Fatalf("result = frames=%d reason=%q, want frames=3 reason=duration", frames, reason)
	}
	want := [][]byte{{0x01}, {0x02}, {0x01}}
	for sampleIndex, sample := range writer.recordedSamples() {
		if !bytes.Equal(sample.Data, want[sampleIndex]) {
			t.Fatalf("sample %d payload = %x, want %x", sampleIndex, sample.Data, want[sampleIndex])
		}
		if sample.Duration != audioPrimeFrameDuration {
			t.Fatalf("sample %d duration = %s, want 20ms", sampleIndex, sample.Duration)
		}
	}
}

func TestRunAudioPrimeRejectsEmptyFrameSequence(t *testing.T) {
	ticker := newManualAudioPrimeTicker(1)
	writer := &recordingAudioPrimeWriter{}

	frames, _, err := runAudioPrime(context.Background(), writer, ticker, 1, nil)
	if !errors.Is(err, errAudioPrimeFramesMissing) {
		t.Fatalf("error = %v, want %v", err, errAudioPrimeFramesMissing)
	}
	if frames != 0 || writer.writeAttempts() != 0 {
		t.Fatalf("empty sequence wrote frames=%d attempts=%d, want zero", frames, writer.writeAttempts())
	}
	if !ticker.wasStopped() {
		t.Fatal("ticker was not stopped after empty frame sequence")
	}
}

func TestNewServerAudioPrimeModeDefaultsAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mode     string
		wantMode string
		wantErr  bool
	}{
		{name: "unset defaults to silence", wantMode: audioPrimeModeSilence},
		{name: "explicit silence", mode: audioPrimeModeSilence, wantMode: audioPrimeModeSilence},
		{name: "tone", mode: audioPrimeModeTone, wantMode: audioPrimeModeTone},
		{name: "unknown", mode: "noise", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, err := NewServer(Options{
				PublicIP:       net.ParseIP("127.0.0.1"),
				UDPPortMin:     46600,
				UDPPortMax:     46620,
				AudioPrimeMode: tt.mode,
				Recordings:     recording.NewFactory(t.TempDir(), time.Now),
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("NewServer succeeded, want audio prime mode error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if server.audioPrimeMode != tt.wantMode {
				t.Fatalf("server audio prime mode = %q, want %q", server.audioPrimeMode, tt.wantMode)
			}
		})
	}
}

func TestRunAudioPrimeCompletesConfiguredFrames(t *testing.T) {
	ticker := newManualAudioPrimeTicker(3)
	for range 3 {
		ticker.ticks <- time.Now()
	}
	writer := &recordingAudioPrimeWriter{}

	frames, reason, err := runAudioPrime(
		context.Background(), writer, ticker, 3, audioPrimeFramesForMode(audioPrimeModeSilence),
	)
	if err != nil {
		t.Fatal(err)
	}
	if frames != 3 {
		t.Fatalf("frames = %d, want 3", frames)
	}
	if reason != audioPrimeReasonDuration {
		t.Fatalf("reason = %q, want %q", reason, audioPrimeReasonDuration)
	}
	if !ticker.wasStopped() {
		t.Fatal("ticker was not stopped")
	}
	for sampleIndex, sample := range writer.recordedSamples() {
		if sample.Duration != 20*time.Millisecond {
			t.Fatalf("sample %d duration = %s, want 20ms", sampleIndex, sample.Duration)
		}
		if got, want := sample.Data, []byte{0xf8, 0xff, 0xfe}; string(got) != string(want) {
			t.Fatalf("sample %d payload = %x, want %x", sampleIndex, got, want)
		}
	}
}

func TestRunAudioPrimeCancellationWinsOverReadyTick(t *testing.T) {
	ticker := newManualAudioPrimeTicker(1)
	ticker.ticks <- time.Now()
	writer := &recordingAudioPrimeWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	frames, reason, err := runAudioPrime(
		ctx, writer, ticker, 1, audioPrimeFramesForMode(audioPrimeModeSilence),
	)
	if err != nil {
		t.Fatal(err)
	}
	if frames != 0 || writer.writeAttempts() != 0 {
		t.Fatalf("cancelled run wrote frames=%d attempts=%d, want zero", frames, writer.writeAttempts())
	}
	if reason != audioPrimeReasonCancelled {
		t.Fatalf("reason = %q, want %q", reason, audioPrimeReasonCancelled)
	}
}

func TestRunAudioPrimeCancellationStopsAfterWrittenFrame(t *testing.T) {
	ticker := newManualAudioPrimeTicker(2)
	writer := &recordingAudioPrimeWriter{wrote: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan audioPrimeResult, 1)
	go func() {
		frames, reason, err := runAudioPrime(
			ctx, writer, ticker, 3, audioPrimeFramesForMode(audioPrimeModeSilence),
		)
		result <- audioPrimeResult{frames: frames, reason: reason, err: err}
	}()

	ticker.ticks <- time.Now()
	select {
	case <-writer.wrote:
	case <-time.After(time.Second):
		t.Fatal("first sample was not written")
	}
	cancel()
	ticker.ticks <- time.Now()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.frames != 1 || writer.writeAttempts() != 1 {
			t.Fatalf("cancelled run wrote frames=%d attempts=%d, want one", got.frames, writer.writeAttempts())
		}
		if got.reason != audioPrimeReasonCancelled {
			t.Fatalf("reason = %q, want %q", got.reason, audioPrimeReasonCancelled)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled runner did not stop")
	}
}

func TestRunAudioPrimeWriteFailureStopsImmediately(t *testing.T) {
	wantErr := errors.New("write failed")
	ticker := newManualAudioPrimeTicker(2)
	ticker.ticks <- time.Now()
	ticker.ticks <- time.Now()
	writer := &recordingAudioPrimeWriter{writeErr: wantErr}

	frames, _, err := runAudioPrime(
		context.Background(), writer, ticker, 2, audioPrimeFramesForMode(audioPrimeModeSilence),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if frames != 0 || writer.writeAttempts() != 1 {
		t.Fatalf("failed run frames=%d attempts=%d, want frames=0 attempts=1", frames, writer.writeAttempts())
	}
	if !ticker.wasStopped() {
		t.Fatal("ticker was not stopped after write failure")
	}
}

func TestSessionAudioPrimeDisabledWritesNothing(t *testing.T) {
	writer := &recordingAudioPrimeWriter{}
	tickerCalls := 0
	logs := &synchronizedBuffer{}
	session := newAudioPrimeTestSession(t, false, 40*time.Millisecond, writer, func(time.Duration) audioPrimeTicker {
		tickerCalls++
		return newManualAudioPrimeTicker(1)
	}, logs)
	session.audioPrimeMode = audioPrimeModeTone

	session.handlePeerConnectionState(webrtc.PeerConnectionStateConnected)
	time.Sleep(10 * time.Millisecond)
	if writer.writeAttempts() != 0 || tickerCalls != 0 {
		t.Fatalf("disabled prime attempts=%d tickerCalls=%d, want zero", writer.writeAttempts(), tickerCalls)
	}
	if strings.Contains(logs.String(), "audio_prime_") {
		t.Fatalf("disabled prime emitted logs: %s", logs.String())
	}
}

func TestSessionAudioPrimeStartsOnlyOnceAfterConnected(t *testing.T) {
	writer := &recordingAudioPrimeWriter{}
	ticker := newManualAudioPrimeTicker(1)
	tickerCalls := 0
	var tickerInterval time.Duration
	logs := &synchronizedBuffer{}
	session := newAudioPrimeTestSession(t, true, 40*time.Millisecond, writer, func(interval time.Duration) audioPrimeTicker {
		tickerCalls++
		tickerInterval = interval
		return ticker
	}, logs)

	session.handlePeerConnectionState(webrtc.PeerConnectionStateConnecting)
	if tickerCalls != 0 || strings.Contains(logs.String(), "audio_prime_started") {
		t.Fatal("audio prime started before connected")
	}
	session.handlePeerConnectionState(webrtc.PeerConnectionStateConnected)
	session.handlePeerConnectionState(webrtc.PeerConnectionStateConnected)
	if tickerCalls != 1 {
		t.Fatalf("ticker factory calls = %d, want 1", tickerCalls)
	}
	if tickerInterval != 20*time.Millisecond {
		t.Fatalf("ticker interval = %s, want 20ms", tickerInterval)
	}
	if got := strings.Count(logs.String(), "audio_prime_started"); got != 1 {
		t.Fatalf("audio_prime_started count = %d, want 1", got)
	}
	if entry := waitForAudioPrimeLog(t, logs, "audio_prime_started"); entry["mode"] != audioPrimeModeSilence {
		t.Fatalf("started log = %#v, want mode=silence", entry)
	}
	session.stopAudioPrime()
}

func TestSessionAudioPrimeToneLogsFixedMode(t *testing.T) {
	writer := &recordingAudioPrimeWriter{}
	ticker := newManualAudioPrimeTicker(1)
	logs := &synchronizedBuffer{}
	session := newAudioPrimeTestSession(t, true, 20*time.Millisecond, writer, func(time.Duration) audioPrimeTicker {
		return ticker
	}, logs)
	session.audioPrimeMode = audioPrimeModeTone

	session.handlePeerConnectionState(webrtc.PeerConnectionStateConnected)
	if entry := waitForAudioPrimeLog(t, logs, "audio_prime_started"); entry["mode"] != audioPrimeModeTone {
		t.Fatalf("started log = %#v, want mode=tone", entry)
	}
	ticker.ticks <- time.Now()
	if entry := waitForAudioPrimeLog(t, logs, "audio_prime_completed"); entry["mode"] != audioPrimeModeTone ||
		entry["frames"] != float64(1) ||
		entry["reason"] != string(audioPrimeReasonDuration) {
		t.Fatalf("completion log = %#v, want mode=tone frames=1 reason=duration", entry)
	}
}

func TestSessionAudioPrimeLogsDurationCompletion(t *testing.T) {
	writer := &recordingAudioPrimeWriter{}
	ticker := newManualAudioPrimeTicker(2)
	logs := &synchronizedBuffer{}
	session := newAudioPrimeTestSession(t, true, 40*time.Millisecond, writer, func(time.Duration) audioPrimeTicker {
		return ticker
	}, logs)

	session.handlePeerConnectionState(webrtc.PeerConnectionStateConnected)
	ticker.ticks <- time.Now()
	ticker.ticks <- time.Now()
	entry := waitForAudioPrimeLog(t, logs, "audio_prime_completed")
	if entry["mode"] != audioPrimeModeSilence || entry["frames"] != float64(2) || entry["reason"] != string(audioPrimeReasonDuration) {
		t.Fatalf("completion log = %#v, want mode=silence frames=2 reason=duration", entry)
	}
}

func TestSessionAudioPrimeTerminalStateLogsCancelledCompletion(t *testing.T) {
	for _, state := range []webrtc.PeerConnectionState{
		webrtc.PeerConnectionStateClosed,
		webrtc.PeerConnectionStateFailed,
	} {
		t.Run(state.String(), func(t *testing.T) {
			writer := &recordingAudioPrimeWriter{}
			ticker := newManualAudioPrimeTicker(1)
			logs := &synchronizedBuffer{}
			session := newAudioPrimeTestSession(t, true, 40*time.Millisecond, writer, func(time.Duration) audioPrimeTicker {
				return ticker
			}, logs)
			session.audioPrimeMode = audioPrimeModeTone

			session.handlePeerConnectionState(webrtc.PeerConnectionStateConnected)
			waitForAudioPrimeLog(t, logs, "audio_prime_started")
			session.handlePeerConnectionState(state)
			entry := waitForAudioPrimeLog(t, logs, "audio_prime_completed")
			if entry["mode"] != audioPrimeModeTone || entry["frames"] != float64(0) || entry["reason"] != string(audioPrimeReasonCancelled) {
				t.Fatalf("completion log = %#v, want mode=tone frames=0 reason=cancelled", entry)
			}
		})
	}
}

func TestSessionAudioPrimeWriteFailureLogsSafeCategoryAndKeepsSessionOpen(t *testing.T) {
	writer := &recordingAudioPrimeWriter{writeErr: errors.New("sentinel write details")}
	ticker := newManualAudioPrimeTicker(1)
	logs := &synchronizedBuffer{}
	session := newAudioPrimeTestSession(t, true, 40*time.Millisecond, writer, func(time.Duration) audioPrimeTicker {
		return ticker
	}, logs)
	session.audioPrimeMode = audioPrimeModeTone

	session.handlePeerConnectionState(webrtc.PeerConnectionStateConnected)
	ticker.ticks <- time.Now()
	entry := waitForAudioPrimeLog(t, logs, "audio_prime_failed")
	if entry["mode"] != audioPrimeModeTone || entry["category"] != "write" {
		t.Fatalf("failure log = %#v, want mode=tone category=write", entry)
	}
	if strings.Contains(logs.String(), "sentinel write details") {
		t.Fatalf("failure log exposed writer error: %s", logs.String())
	}
	if strings.Contains(logs.String(), "audio_prime_completed") {
		t.Fatalf("write failure also logged completion: %s", logs.String())
	}
	if session.ctx.Err() != nil {
		t.Fatalf("write failure closed session: %v", session.ctx.Err())
	}
}

func newAudioPrimeTestSession(
	t *testing.T,
	enabled bool,
	duration time.Duration,
	writer audioPrimeWriter,
	tickerFactory func(time.Duration) audioPrimeTicker,
	logs *synchronizedBuffer,
) *Session {
	t.Helper()
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{
		id:                  "audio-prime-test",
		peer:                peer,
		ctx:                 ctx,
		cancel:              cancel,
		logger:              slog.New(slog.NewJSONHandler(logs, nil)),
		disconnectGrace:     time.Hour,
		audioPrimeEnabled:   enabled,
		audioPrimeDuration:  duration,
		audioPrimeMode:      audioPrimeModeSilence,
		audioPrimeWriter:    writer,
		newAudioPrimeTicker: tickerFactory,
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func waitForAudioPrimeLog(t *testing.T, logs *synchronizedBuffer, message string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var entry map[string]any
			if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == message {
				return entry
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("log %q not found in %s", message, logs.String())
	return nil
}

type audioPrimeResult struct {
	frames int
	reason audioPrimeCompletionReason
	err    error
}

type manualAudioPrimeTicker struct {
	ticks chan time.Time
	mu    sync.Mutex
	stop  bool
}

func newManualAudioPrimeTicker(buffer int) *manualAudioPrimeTicker {
	return &manualAudioPrimeTicker{ticks: make(chan time.Time, buffer)}
}

func (t *manualAudioPrimeTicker) C() <-chan time.Time {
	return t.ticks
}

func (t *manualAudioPrimeTicker) Stop() {
	t.mu.Lock()
	t.stop = true
	t.mu.Unlock()
}

func (t *manualAudioPrimeTicker) wasStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stop
}

type recordingAudioPrimeWriter struct {
	mu       sync.Mutex
	samples  []media.Sample
	attempts int
	writeErr error
	wrote    chan struct{}
}

func (w *recordingAudioPrimeWriter) WriteSample(sample media.Sample) error {
	w.mu.Lock()
	w.attempts++
	if w.writeErr != nil {
		err := w.writeErr
		w.mu.Unlock()
		return err
	}
	w.samples = append(w.samples, media.Sample{
		Data:     append([]byte(nil), sample.Data...),
		Duration: sample.Duration,
	})
	wrote := w.wrote
	w.mu.Unlock()
	if wrote != nil {
		wrote <- struct{}{}
	}
	return nil
}

func (w *recordingAudioPrimeWriter) recordedSamples() []media.Sample {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]media.Sample(nil), w.samples...)
}

func (w *recordingAudioPrimeWriter) writeAttempts() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts
}
