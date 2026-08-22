package ports

import (
	"context"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

// SystemClock is the production time source.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// WallSleeper is the production backoff implementation. It honors context
// cancellation instead of sleeping through it.
type WallSleeper struct{}

func (WallSleeper) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RandomIDs generates cryptographically random identifiers via the domain
// constructor.
type RandomIDs struct{}

func (RandomIDs) NewID(prefix string) (string, error) { return domain.NewID(prefix) }
