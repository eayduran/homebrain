package recording

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
)

var fixedTime = time.Date(2026, 8, 2, 12, 34, 56, 123456789, time.UTC)

func opusPacket(sequence uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: sequence,
			Timestamp:      uint32(sequence) * 960,
			SSRC:           0x10203040,
		},
		Payload: []byte{0xf8, 0xff, 0xfe},
	}
}

func TestFactorySanitizesSessionIDAndContainsPath(t *testing.T) {
	dir := t.TempDir()
	factory := NewFactory(dir, func() time.Time { return fixedTime })

	recorder, err := factory.New("../../escape/session\x00 name")
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	rel, err := filepath.Rel(dir, recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("recording escaped directory: %q", recorder.Path())
	}
	if strings.ContainsAny(filepath.Base(recorder.Path()), "/\\\x00 ") {
		t.Fatalf("unsafe filename: %q", filepath.Base(recorder.Path()))
	}
	if !strings.HasSuffix(recorder.Path(), ".ogg") {
		t.Fatalf("unexpected extension: %q", recorder.Path())
	}
}

func TestRecorderWritesOggHeaderAndFinalizesIdempotently(t *testing.T) {
	recorder, err := NewFactory(t.TempDir(), func() time.Time { return fixedTime }).New("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.WriteRTP(opusPacket(1)); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("second close must be idempotent: %v", err)
	}
	if err := recorder.WriteRTP(opusPacket(2)); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close error = %v, want ErrClosed", err)
	}

	data, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("OggS")) {
		t.Fatalf("recording does not start with OggS: %x", data[:min(4, len(data))])
	}
	if len(data) <= 64 {
		t.Fatalf("recording was not finalized: %d bytes", len(data))
	}
}

func TestFactoryUsesDistinctNanosecondTimestamps(t *testing.T) {
	now := fixedTime
	factory := NewFactory(t.TempDir(), func() time.Time {
		result := now
		now = now.Add(time.Nanosecond)
		return result
	})
	first, err := factory.New("same")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := factory.New("same")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.Path() == second.Path() {
		t.Fatalf("recording paths collided: %q", first.Path())
	}
}
