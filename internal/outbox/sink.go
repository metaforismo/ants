package outbox

import (
	"context"
	"log/slog"

	"github.com/metaforismo/ants/internal/ports"
)

// LogSink is the production default until external subscribers exist: it
// records delivered envelopes in the structured log, keyed by event ID so
// operators can trace at-least-once redeliveries.
type LogSink struct{ Logger *slog.Logger }

func (s LogSink) Deliver(_ context.Context, msg ports.OutboxMessage) error {
	s.Logger.Info("outbox envelope delivered",
		"message_id", msg.ID,
		"dedup_key", msg.DedupKey,
		"tenant_id", string(msg.TenantID),
		"attempts", msg.Attempts,
	)
	return nil
}
