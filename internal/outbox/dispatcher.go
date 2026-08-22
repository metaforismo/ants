// Package outbox provides the in-process dispatcher that drains the
// transactional outbox: claim a bounded batch, hand each message to a sink,
// acknowledge successes, and reschedule failures with classified bounded
// retries toward dead-letter. Delivery is at-least-once; consumers must
// deduplicate on message identity (ADR-0011).
package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/metaforismo/ants/internal/ports"
)

// Sink consumes one claimed message. Returning nil acknowledges delivery;
// any error reschedules the message with backoff.
type Sink interface {
	Deliver(ctx context.Context, msg ports.OutboxMessage) error
}

// Observer receives dispatch outcomes for instrumentation (ADR-0014).
// Implementations must be cheap and non-blocking: they run inline in the
// dispatch loop. A nil observer disables instrumentation only.
type Observer interface {
	RoundLeased(leased int)
	OutboxStates(states ports.OutboxStats)
	Delivered()
	RetryScheduled()
	DeadLettered()
}

// Config bounds dispatcher behavior. All values are validated.
type Config struct {
	BatchSize        int
	Interval         time.Duration
	Lease            time.Duration
	MaxAttempts      int
	RetryBackoffBase time.Duration
}

func (c Config) Validate() error {
	switch {
	case c.BatchSize < 1 || c.BatchSize > 1000:
		return fmt.Errorf("outbox.batch_size must be within [1,1000], got %d", c.BatchSize)
	case c.Interval < 10*time.Millisecond:
		return fmt.Errorf("outbox.interval must be at least 10ms, got %s", c.Interval)
	case c.Lease < time.Second:
		return fmt.Errorf("outbox.lease must be at least 1s, got %s", c.Lease)
	case c.MaxAttempts < 1 || c.MaxAttempts > 100:
		return fmt.Errorf("outbox.max_attempts must be within [1,100], got %d", c.MaxAttempts)
	case c.RetryBackoffBase < 0:
		return fmt.Errorf("outbox.retry_backoff_base must not be negative")
	}
	return nil
}

type Dispatcher struct {
	store    ports.OutboxStore
	sink     Sink
	logger   *slog.Logger
	cfg      Config
	workerID string
	obs      Observer
}

// New builds a dispatcher over one outbox store. Scheduling time is owned by
// that store's clock (ADR-0011), not by the dispatcher. The observer may be
// nil when instrumentation is disabled.
func New(store ports.OutboxStore, sink Sink, logger *slog.Logger, cfg Config, workerID string, obs Observer) (*Dispatcher, error) {
	if store == nil || sink == nil || logger == nil {
		return nil, fmt.Errorf("outbox: store, sink and logger are required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("outbox: %w", err)
	}
	return &Dispatcher{
		store: store, sink: sink, logger: logger,
		cfg: cfg, workerID: workerID, obs: obs,
	}, nil
}

// Run drains the outbox until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := d.DispatchOnce(ctx); err != nil && ctx.Err() == nil {
				d.logger.Error("outbox dispatch round failed", "error", err.Error())
			}
		}
	}
}

// DispatchOnce claims and processes one batch, returning how many messages
// were handled. Exported so tests and operators can drive rounds manually.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	leased, err := d.store.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: d.workerID,
		LeaseFor: d.cfg.Lease,
		Limit:    d.cfg.BatchSize,
	})
	if err != nil {
		return 0, err
	}
	if d.obs != nil {
		d.obs.RoundLeased(len(leased))
		if states, statsErr := d.store.Stats(ctx); statsErr != nil {
			d.logger.Warn("outbox stats refresh failed", "error", safeErr(statsErr))
		} else {
			d.obs.OutboxStates(states)
		}
	}
	for _, msg := range leased {
		deliverCtx, cancel := context.WithTimeout(ctx, d.cfg.Lease)
		deliverErr := d.sink.Deliver(deliverCtx, msg)
		cancel()
		switch {
		case deliverErr == nil:
			if ackErr := d.store.MarkDelivered(ctx, msg.ID, d.workerID); ackErr != nil {
				d.logger.Warn("outbox ack failed; message will redeliver",
					"message_id", msg.ID, "error", safeErr(ackErr))
			} else if d.obs != nil {
				d.obs.Delivered()
			}
		case ctx.Err() != nil:
			// Shutting down: leave the lease to expire so another worker
			// reclaims instead of counting this attempt as a failure.
			return len(leased), ctx.Err()
		default:
			retryIn := backoffFor(d.cfg.RetryBackoffBase, msg.Attempts)
			if failErr := d.store.FailWithBackoff(ctx, msg.ID, d.workerID, retryIn, safeErr(deliverErr)); failErr != nil {
				d.logger.Error("outbox failure recording failed",
					"message_id", msg.ID, "error", safeErr(failErr))
			} else {
				switch {
				case msg.Attempts >= msg.MaxAttempts:
					d.logger.Error("outbox message moved to dead-letter",
						"message_id", msg.ID, "dedup_key", msg.DedupKey,
						"attempts", msg.Attempts, "last_error", safeErr(deliverErr))
					if d.obs != nil {
						d.obs.DeadLettered()
					}
				default:
					d.logger.Warn("outbox delivery failed; scheduled for retry",
						"message_id", msg.ID, "attempt", msg.Attempts,
						"retry_in", retryIn.String(), "error", safeErr(deliverErr))
					if d.obs != nil {
						d.obs.RetryScheduled()
					}
				}
			}
		}
	}
	return len(leased), nil
}

// backoffFor doubles the base per prior attempt, saturating instead of
// overflowing: an unbounded shift of a large configured base could wrap a
// time.Duration negative and turn a retry into a hot loop.
func backoffFor(base time.Duration, attempts int) time.Duration {
	const maxBackoff = 24 * time.Hour
	if base <= 0 {
		return 0
	}
	for range minInt(attempts-1, 16) {
		if base >= maxBackoff {
			return maxBackoff
		}
		base <<= 1
	}
	if base > maxBackoff {
		return maxBackoff
	}
	return base
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func safeErr(err error) string {
	const max = 512
	msg := err.Error()
	if len(msg) > max {
		return msg[:max]
	}
	return msg
}
