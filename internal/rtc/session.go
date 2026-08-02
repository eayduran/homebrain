package rtc

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"home-brain-rtc/internal/recording"

	"github.com/pion/webrtc/v4"
)

type Session struct {
	id                  string
	peer                *webrtc.PeerConnection
	ctx                 context.Context
	cancel              context.CancelFunc
	logger              *slog.Logger
	onTerminal          func()
	recordings          recording.Factory
	disconnectGrace     time.Duration
	audioPrimeEnabled   bool
	audioPrimeDuration  time.Duration
	audioPrimeWriter    audioPrimeWriter
	newAudioPrimeTicker func(time.Duration) audioPrimeTicker

	stateMu              sync.Mutex
	connected            bool
	audioClaimed         bool
	recorder             recording.Recorder
	disconnectTimer      *time.Timer
	disconnectGeneration uint64
	peerState            webrtc.PeerConnectionState
	expiryTimer          *time.Timer
	audioPrimeCancel     context.CancelFunc

	audioPrimeOnce sync.Once
	closeOnce      sync.Once
	closeErr       error
}

func (s *Session) configurePeerCallbacks() {
	s.peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		s.logger.Info("ice_state_changed", "sessionId", s.id, "state", state.String())
	})
	s.peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		s.handlePeerConnectionState(state)
	})
	s.peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		s.logger.Info("remote_track_received", "sessionId", s.id, "codec", track.Codec().MimeType)
		if track.Kind() != webrtc.RTPCodecTypeAudio || !strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeOpus) {
			go drainRemoteTrack(track)
			return
		}
		recorder, ok := s.startRecorder()
		if !ok {
			go drainRemoteTrack(track)
			return
		}
		go s.recordTrack(track, recorder)
	})
}

func drainRemoteTrack(track *webrtc.TrackRemote) {
	for {
		if _, _, err := track.ReadRTP(); err != nil {
			return
		}
	}
}

func (s *Session) startRecorder() (recording.Recorder, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.audioClaimed || s.ctx.Err() != nil {
		return nil, false
	}
	s.audioClaimed = true
	recorder, err := s.recordings.New(s.id)
	if err != nil {
		s.logger.Error("session_error", "sessionId", s.id, "category", "recording_create")
		go func() { _ = s.Close() }()
		return nil, false
	}
	s.recorder = recorder
	s.logger.Info("recording_started", "sessionId", s.id, "file", filepath.Base(recorder.Path()))
	return recorder, true
}

func (s *Session) recordTrack(track *webrtc.TrackRemote, recorder recording.Recorder) {
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			if s.ctx.Err() == nil {
				s.logger.Error("session_error", "sessionId", s.id, "category", "rtp_read")
			}
			return
		}
		if err := recorder.WriteRTP(packet); err != nil {
			if !errors.Is(err, recording.ErrClosed) {
				s.logger.Error("session_error", "sessionId", s.id, "category", "recording_write")
				go func() { _ = s.Close() }()
			}
			return
		}
	}
}

func (s *Session) MarkConnected() {
	s.stateMu.Lock()
	s.connected = true
	s.stateMu.Unlock()
}

func (s *Session) Connected() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.connected
}

func (s *Session) setExpiryTimer(timer *time.Timer) {
	s.stateMu.Lock()
	if s.ctx.Err() != nil {
		timer.Stop()
	} else {
		s.expiryTimer = timer
	}
	s.stateMu.Unlock()
}

func (s *Session) handlePeerConnectionState(state webrtc.PeerConnectionState) {
	s.logger.Info("peer_connection_state_changed", "sessionId", s.id, "state", state.String())
	s.stateMu.Lock()
	s.disconnectGeneration++
	generation := s.disconnectGeneration
	s.peerState = state
	if s.disconnectTimer != nil {
		s.disconnectTimer.Stop()
		s.disconnectTimer = nil
	}
	if state == webrtc.PeerConnectionStateDisconnected && s.ctx.Err() == nil {
		s.disconnectTimer = time.AfterFunc(s.disconnectGrace, func() { s.closeIfStillDisconnected(generation) })
	}
	s.stateMu.Unlock()
	if state == webrtc.PeerConnectionStateConnected {
		s.startAudioPrime()
	}
	if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
		s.stopAudioPrime()
		go func() { _ = s.Close() }()
	}
}

func (s *Session) startAudioPrime() {
	if !s.audioPrimeEnabled || s.audioPrimeWriter == nil || s.ctx.Err() != nil {
		return
	}
	s.audioPrimeOnce.Do(func() {
		primeCtx, primeCancel := context.WithCancel(s.ctx)
		s.stateMu.Lock()
		if s.ctx.Err() != nil {
			s.stateMu.Unlock()
			primeCancel()
			return
		}
		s.audioPrimeCancel = primeCancel
		s.stateMu.Unlock()

		ticker := s.newAudioPrimeTicker(audioPrimeFrameDuration)
		s.logger.Info("audio_prime_started", "sessionId", s.id)
		go func() {
			frames, reason, err := runAudioPrime(
				primeCtx,
				s.audioPrimeWriter,
				ticker,
				int(s.audioPrimeDuration/audioPrimeFrameDuration),
			)
			if err != nil {
				s.logger.Error("audio_prime_failed", "sessionId", s.id, "category", "write")
				return
			}
			s.logger.Info(
				"audio_prime_completed",
				"sessionId", s.id,
				"frames", frames,
				"reason", string(reason),
			)
		}()
	})
}

func (s *Session) stopAudioPrime() {
	s.stateMu.Lock()
	cancel := s.audioPrimeCancel
	s.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Session) closeIfStillDisconnected(generation uint64) {
	s.stateMu.Lock()
	shouldClose := s.ctx.Err() == nil &&
		s.peerState == webrtc.PeerConnectionStateDisconnected &&
		s.disconnectGeneration == generation
	if shouldClose {
		s.disconnectTimer = nil
	}
	s.stateMu.Unlock()
	if shouldClose {
		_ = s.Close()
	}
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.stopAudioPrime()
		s.stateMu.Lock()
		if s.disconnectTimer != nil {
			s.disconnectTimer.Stop()
			s.disconnectTimer = nil
		}
		if s.expiryTimer != nil {
			s.expiryTimer.Stop()
			s.expiryTimer = nil
		}
		recorder := s.recorder
		s.recorder = nil
		s.stateMu.Unlock()
		if recorder != nil {
			if err := recorder.Close(); err != nil {
				s.closeErr = err
				s.logger.Error("session_error", "sessionId", s.id, "category", "recording_close")
			}
			s.logger.Info("recording_stopped", "sessionId", s.id, "file", filepath.Base(recorder.Path()))
		}
		if err := s.peer.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
		if s.onTerminal != nil {
			s.onTerminal()
		}
		s.logger.Info("session_closed", "sessionId", s.id)
	})
	return s.closeErr
}
