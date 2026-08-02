package recording

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
)

var ErrClosed = errors.New("recorder is closed")

type Recorder interface {
	WriteRTP(*rtp.Packet) error
	Close() error
	Path() string
}

type Factory interface {
	New(sessionID string) (Recorder, error)
}

type OGGFactory struct {
	dir string
	now func() time.Time
}

func NewFactory(dir string, now func() time.Time) *OGGFactory {
	if now == nil {
		now = time.Now
	}
	return &OGGFactory{dir: dir, now: now}
}

func (f *OGGFactory) New(sessionID string) (Recorder, error) {
	dir, err := filepath.Abs(f.dir)
	if err != nil {
		return nil, fmt.Errorf("resolve recordings directory: %w", err)
	}
	filename := fmt.Sprintf(
		"%s_%s.ogg",
		f.now().UTC().Format("20060102T150405.000000000Z"),
		sanitizeSessionID(sessionID),
	)
	path := filepath.Join(dir, filename)
	if err := ensureContained(dir, path); err != nil {
		return nil, err
	}
	writer, err := oggwriter.New(path, 48000, 2)
	if err != nil {
		return nil, fmt.Errorf("create OGG writer: %w", err)
	}
	return &oggRecorder{path: path, writer: writer}, nil
}

func ensureContained(dir, path string) error {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return fmt.Errorf("resolve recording path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("recording path escapes recordings directory")
	}
	return nil
}

func sanitizeSessionID(value string) string {
	var builder strings.Builder
	previousUnderscore := false
	for _, r := range value {
		allowed := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.'
		if allowed && r <= unicode.MaxASCII {
			builder.WriteRune(r)
			previousUnderscore = false
			continue
		}
		if !previousUnderscore {
			builder.WriteByte('_')
			previousUnderscore = true
		}
	}
	result := strings.Trim(builder.String(), "._-")
	if result == "" || result == ".." {
		return "session"
	}
	if len(result) > 128 {
		result = result[:128]
	}
	return result
}

type oggRecorder struct {
	mu     sync.Mutex
	path   string
	writer *oggwriter.OggWriter
	closed bool
}

func (r *oggRecorder) Path() string { return r.path }

func (r *oggRecorder) WriteRTP(packet *rtp.Packet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if packet == nil {
		return errors.New("RTP packet is required")
	}
	return r.writer.WriteRTP(packet)
}

func (r *oggRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.writer.Close()
}
