package rtc

import (
	"context"
	"time"

	"github.com/pion/webrtc/v4/pkg/media"
)

const audioPrimeFrameDuration = 20 * time.Millisecond

var opusSilenceFrame = []byte{0xf8, 0xff, 0xfe}

type audioPrimeCompletionReason string

const (
	audioPrimeReasonDuration  audioPrimeCompletionReason = "duration"
	audioPrimeReasonCancelled audioPrimeCompletionReason = "cancelled"
)

type audioPrimeWriter interface {
	WriteSample(media.Sample) error
}

type audioPrimeTicker interface {
	C() <-chan time.Time
	Stop()
}

type wallClockAudioPrimeTicker struct {
	ticker *time.Ticker
}

func newWallClockAudioPrimeTicker(interval time.Duration) audioPrimeTicker {
	return &wallClockAudioPrimeTicker{ticker: time.NewTicker(interval)}
}

func (t *wallClockAudioPrimeTicker) C() <-chan time.Time {
	return t.ticker.C
}

func (t *wallClockAudioPrimeTicker) Stop() {
	t.ticker.Stop()
}

func runAudioPrime(
	ctx context.Context,
	writer audioPrimeWriter,
	ticker audioPrimeTicker,
	targetFrames int,
) (int, audioPrimeCompletionReason, error) {
	defer ticker.Stop()
	frames := 0
	for frames < targetFrames {
		if ctx.Err() != nil {
			return frames, audioPrimeReasonCancelled, nil
		}
		select {
		case <-ctx.Done():
			return frames, audioPrimeReasonCancelled, nil
		case <-ticker.C():
			if ctx.Err() != nil {
				return frames, audioPrimeReasonCancelled, nil
			}
			if err := writer.WriteSample(media.Sample{
				Data:     opusSilenceFrame,
				Duration: audioPrimeFrameDuration,
			}); err != nil {
				return frames, "", err
			}
			frames++
		}
	}
	return frames, audioPrimeReasonDuration, nil
}
