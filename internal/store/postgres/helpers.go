package postgres

import (
	"context"
	"time"

	"github.com/metaforismo/ants/internal/domain"
)

func timeNow() time.Time { return time.Now().UTC() }

// withinAutoTx runs fn inside a transaction when the context does not already
// carry one; otherwise it joins the caller's transaction so both participate
// in the same unit of work.
func withinAutoTx(ctx context.Context, s *Store, fn func(ctx context.Context) error) error {
	if txFrom(ctx) != nil {
		return fn(ctx)
	}
	tx, err := s.pool.BeginTx(ctx, nil)
	if err != nil {
		return domain.Internalf(err, "db_tx", "begin transaction")
	}
	if err := fn(withTx(ctx, tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return domain.Internalf(err, "db_tx", "commit transaction")
	}
	return nil
}

// classifyStaleOrMissing distinguishes an optimistic-concurrency conflict
// from a vanished/foreign row: callers get stale_version or not_found, never
// an ambiguous failure.
func classifyStaleOrMissing(ctx context.Context, entity, id string, exists func() error) error {
	err := exists()
	switch {
	case err == nil:
		// Row is present with a different version than the caller assumed.
		return &domain.Error{
			Kind:    domain.ErrKindConflict,
			Code:    "stale_version",
			Message: entity + " was modified concurrently",
			Details: map[string]any{"id": id},
		}
	case domain.ErrKindOf(err) == domain.ErrKindNotFound:
		return domain.NotFoundf(entity, id)
	default:
		return err
	}
}
