package rtc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"home-brain-rtc/internal/recording"

	"github.com/pion/ice/v4"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

var (
	ErrInvalidSDP    = errors.New("invalid SDP offer")
	ErrOpusRequired  = errors.New("offer SDP must contain an Opus audio codec")
	ErrAnswerTimeout = errors.New("SDP answer generation timed out")
)

type Options struct {
	PublicIP        net.IP
	UDPPortMin      uint16
	UDPPortMax      uint16
	Recordings      recording.Factory
	Logger          *slog.Logger
	AnswerTimeout   time.Duration
	DisconnectGrace time.Duration
}

type Server struct {
	api             *webrtc.API
	recordings      recording.Factory
	logger          *slog.Logger
	answerTimeout   time.Duration
	disconnectGrace time.Duration
}

func NewServer(opts Options) (*Server, error) {
	if opts.PublicIP == nil || opts.PublicIP.To4() == nil {
		return nil, errors.New("public IPv4 is required")
	}
	if opts.UDPPortMin == 0 || opts.UDPPortMin > opts.UDPPortMax {
		return nil, errors.New("valid UDP port range is required")
	}
	if opts.Recordings == nil {
		return nil, errors.New("recording factory is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.AnswerTimeout <= 0 {
		opts.AnswerTimeout = 4500 * time.Millisecond
	}
	if opts.DisconnectGrace <= 0 {
		opts.DisconnectGrace = 5 * time.Second
	}

	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register Opus codec: %w", err)
	}

	settings := webrtc.SettingEngine{}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	if err := settings.SetEphemeralUDPPortRange(opts.UDPPortMin, opts.UDPPortMax); err != nil {
		return nil, fmt.Errorf("set UDP port range: %w", err)
	}
	if err := settings.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
		External:        []string{opts.PublicIP.String()},
		AsCandidateType: webrtc.ICECandidateTypeHost,
		Mode:            webrtc.ICEAddressRewriteReplace,
		Networks:        []webrtc.NetworkType{webrtc.NetworkTypeUDP4},
	}); err != nil {
		return nil, fmt.Errorf("configure ICE address rewrite: %w", err)
	}

	return &Server{
		api:             webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settings)),
		recordings:      opts.Recordings,
		logger:          opts.Logger,
		answerTimeout:   opts.AnswerTimeout,
		disconnectGrace: opts.DisconnectGrace,
	}, nil
}

func (s *Server) NewSession(ctx context.Context, id, offer string, onTerminal func()) (*Session, string, error) {
	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, s.answerTimeout)
	defer deadlineCancel()
	if err := deadlineCtx.Err(); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrAnswerTimeout, err)
	}
	if err := validateOffer(offer); err != nil {
		return nil, "", err
	}
	peer, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", fmt.Errorf("create peer connection: %w", err)
	}
	sessionCtx, cancel := context.WithCancel(context.Background())
	session := &Session{
		id: id, peer: peer, cancel: cancel, ctx: sessionCtx, logger: s.logger,
		onTerminal: onTerminal, recordings: s.recordings, disconnectGrace: s.disconnectGrace,
	}
	session.configurePeerCallbacks()

	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		"home-brain-audio", "home-brain",
	)
	if err != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("create local Opus track: %w", err)
	}
	sender, err := peer.AddTrack(localTrack)
	if err != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("add local Opus track: %w", err)
	}
	go consumeRTCP(sender)

	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidSDP, err)
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("create answer: %w", err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(answer); err != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("set local description: %w", err)
	}
	select {
	case <-gathered:
		local := peer.LocalDescription()
		if local == nil {
			_ = session.Close()
			return nil, "", errors.New("local description missing after ICE gathering")
		}
		s.logger.Info("sdp_answer_generated", "sessionId", id, "offerBytes", len(offer), "answerBytes", len(local.SDP))
		s.logger.Info("session_created", "sessionId", id)
		return session, local.SDP, nil
	case <-deadlineCtx.Done():
		_ = session.Close()
		return nil, "", fmt.Errorf("%w: %v", ErrAnswerTimeout, deadlineCtx.Err())
	}
}

func validateOffer(raw string) error {
	var description sdp.SessionDescription
	if err := description.Unmarshal([]byte(raw)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSDP, err)
	}
	foundAudio := false
	for _, media := range description.MediaDescriptions {
		if media.MediaName.Media != "audio" {
			continue
		}
		foundAudio = true
		if media.MediaName.Port.Value == 0 {
			continue
		}
		formats := make(map[string]struct{}, len(media.MediaName.Formats))
		for _, format := range media.MediaName.Formats {
			formats[format] = struct{}{}
		}
		for _, attribute := range media.Attributes {
			if attribute.Key != "rtpmap" {
				continue
			}
			fields := strings.Fields(attribute.Value)
			if len(fields) != 2 {
				continue
			}
			if _, offered := formats[fields[0]]; !offered {
				continue
			}
			codec := strings.Split(strings.ToLower(fields[1]), "/")
			if len(codec) >= 2 && codec[0] == "opus" && codec[1] == "48000" {
				return nil
			}
		}
	}
	if !foundAudio {
		return fmt.Errorf("%w: audio media section is missing", ErrOpusRequired)
	}
	return ErrOpusRequired
}

func consumeRTCP(sender *webrtc.RTPSender) {
	buffer := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buffer); err != nil {
			return
		}
	}
}
