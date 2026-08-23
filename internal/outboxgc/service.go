// Package outboxgc performs bounded garbage-collection rounds over terminal
// outbox rows (ADR-0016): delivered and discarded rows beyond their
// configured horizons, oldest-terminal-first, at most BatchSize rows per
// round. It never touches pending, leased, or dead rows, domain events, or
// audit history — eligibility is a property of the store's own selection
// logic, not of this service. A configuration whose horizons are both zero
// is inert: nothing anywhere may delete anything until retention is
// intentionally configured.
package outboxgc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/metaforismo/ants/internal/ports"
)

// StateDelivered and StateDiscarded are the fixed label vocabulary of the
// observable deletion classes.
const (
	StateDelivered = "delivered"
	StateDiscarded = "discarded"
)

// Config mirrors config.OutboxRetention for composition-root wiring. All
// values are validated here too so tests and other callers cannot build an
// unbounded service by bypassing configuration validation.
type Config struct {
	DeliveredAfter time.Duration
	DiscardedAfter time.Duration
	BatchSize      int
	Interval       time.Duration
}

func (c Config) Validate() error {
	if c.DeliveredAfter < 0 || c.DiscardedAfter < 0 {
		return fmt.Errorf("outbox.retention horizons must not be negative")
	}
	if c.BatchSize < 1 || c.BatchSize > ports.MaxRetentionSweepBatch {
		return fmt.Errorf("outbox.retention.batch_size must be within [1,%d], got %d", ports.MaxRetentionSweepBatch, c.BatchSize)
	}
	if c.Interval < time.Second {
		return fmt.Errorf("outbox.retention.interval must be at least 1s, got %s", c.Interval)
	}
	return nil
}

// Active reports whether any horizon is configured; an inactive service
// refuses scheduled operation entirely (ADR-0016).
func (c Config) Active() bool {
	return c.DeliveredAfter > 0 || c.DiscardedAfter > 0
}

// Observer receives retention outcomes for instrumentation (ADR-0014 seam
// pattern). Implementations must be cheap and non-blocking; a nil observer
// disables instrumentation only and never changes behavior.
type Observer interface {
	// RoundsCompleted counts one successful sweep (real deletions only;
	// previews never count).
	RoundsCompleted()
	// Deleted reports deleted rows for one fixed-vocabulary class.
	Deleted(state string, n int64)
}

type Service struct {
	store  ports.OutboxStore
	logger *slog.Logger
	cfg    Config
	obs    Observer
}

// New builds the GC service. Store, logger, and a valid config are required;
// construction fails loudly on invalid bounds rather than silently clamping.
func New(store ports.OutboxStore, logger *slog.Logger, cfg Config, obs Observer) (*Service, error) {
	if store == nil || logger == nil {
		return nil, fmt.Errorf("outboxgc: store and logger are required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("outboxgc: %w", err)
	}
	return &Service{store: store, logger: logger, cfg: cfg, obs: obs}, nil
}

// Active reports whether any retention horizon is configured; an inactive
// service refuses scheduled operation entirely (ADR-0016). Callers use it
// to decide whether the scheduled loop should run at all.
func (s *Service) Active() bool { return s.cfg.Active() }

// Preview runs the identical eligibility selection without deleting and
// reports what a round would collect now. Previews never fire observers.
func (s *Service) Preview(ctx context.Context) (ports.RetentionSweepResult, error) {
	return s.store.SweepRetention(ctx, s.request(true))
}

// Round performs one bounded deletion sweep and observes the outcome.
func (s *Service) Round(ctx context.Context) (ports.RetentionSweepResult, error) {
	res, err := s.store.SweepRetention(ctx, s.request(false))
	if err != nil {
		return res, err
	}
	if s.obs != nil {
		if res.DeletedDelivered > 0 {
			s.obs.Deleted(StateDelivered, res.DeletedDelivered)
		}
		if res.DeletedDiscarded > 0 {
			s.obs.Deleted(StateDiscarded, res.DeletedDiscarded)
		}
		// Every successful round counts, including empty ones, mirroring
		// the dispatcher's round counter so operators can rate sweep
		// activity directly.
		s.obs.RoundsCompleted()
	}
	return res, nil
}

// Run executes rounds on the configured interval until ctx is cancelled. It
// refuses inactive configurations instead of spinning a loop that can never
// delete anything — scheduling requires intentional retention settings.
func (s *Service) Run(ctx context.Context) error {
	if !s.cfg.Active() {
		return fmt.Errorf("outboxgc: retention is not configured; refusing to schedule inert collection loops")
	}
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			res, err := s.Round(ctx)
			switch {
			case err != nil && ctx.Err() == nil:
				s.logger.Error("outbox retention round failed", "error", err.Error())
			case err == nil:
				s.logger.Info("outbox retention round completed",
					"deleted_delivered", res.DeletedDelivered,
					"deleted_discarded", res.DeletedDiscarded,
					"cutoff", res.Cutoff.Format(time.RFC3339))
			}
		}
	}
}

func (s *Service) request(dryRun bool) ports.RetentionSweepRequest {
	return ports.RetentionSweepRequest{
		DeliveredOlderThan: s.cfg.DeliveredAfter,
		DiscardedOlderThan: s.cfg.DiscardedAfter,
		Limit:              s.cfg.BatchSize,
		DryRun:             dryRun,
	}
}
