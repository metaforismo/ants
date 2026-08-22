package postgres

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/metaforismo/ants/internal/domain"
)

type EventRepository struct{ st *Store }

var _ interface {
	Append(context.Context, *domain.Event) error
	ListByTenant(context.Context, domain.TenantID, int64, int) ([]*domain.Event, error)
	ListByRun(context.Context, domain.TenantID, domain.RunID, int64, int) ([]*domain.Event, error)
} = (*EventRepository)(nil)

const eventColumns = `seq, id, type, occurred_at, tenant_id, aggregate_type,
	aggregate_id, aggregate_version, actor_type, actor_id, trace_id, run_id, data`

func scanEvent(rows *sql.Rows) (*domain.Event, error) {
	var (
		e         domain.Event
		actorType string
		traceID   sql.NullString
		runID     sql.NullString
		data      []byte
	)
	err := rows.Scan(&e.Seq, &e.ID, &e.Type, &e.OccurredAt, &e.TenantID,
		&e.AggregateType, &e.AggregateID, &e.AggregateVersion, &actorType,
		&e.Actor.ID, &traceID, &runID, &data)
	if err != nil {
		return nil, wrapScan(err)
	}
	e.Actor.Type = domain.PrincipalKind(actorType)
	if traceID.Valid {
		e.TraceID = traceID.String
	}
	if runID.Valid {
		e.RunID = domain.RunID(runID.String)
	}
	if err := unmarshalJSONColumn(data, &e.Data); err != nil {
		return nil, err
	}
	return &e, nil
}

// Append persists the envelope and adopts the database-assigned monotonic
// cursor. The seq column is an identity: application-supplied sequence
// numbers are never trusted.
func (r *EventRepository) Append(ctx context.Context, evt *domain.Event) error {
	data, err := marshalJSONColumn(nonNilMap(evt.Data))
	if err != nil {
		return err
	}
	var newSeq int64
	insertErr := r.st.q(ctx).QueryRowContext(ctx,
		`INSERT INTO events (id, type, occurred_at, tenant_id, aggregate_type,
		   aggregate_id, aggregate_version, actor_type, actor_id, trace_id, run_id, data)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		 RETURNING seq`,
		string(evt.ID), string(evt.Type), evt.OccurredAt, string(evt.TenantID),
		evt.AggregateType, evt.AggregateID, evt.AggregateVersion,
		string(evt.Actor.Type), evt.Actor.ID, evt.TraceID, string(evt.RunID), data,
	).Scan(&newSeq)
	if insertErr != nil {
		if mapped := mapUniqueViolation(insertErr, "event_exists", "event %s already exists", evt.ID); mapped != insertErr {
			return mapped
		}
		return wrapWrite(insertErr)
	}
	evt.Seq = newSeq
	return nil
}

func (r *EventRepository) ListByTenant(ctx context.Context, tenantID domain.TenantID, afterSeq int64, limit int) ([]*domain.Event, error) {
	return r.list(ctx, `tenant_id = $1 AND seq > $2`, []any{string(tenantID), afterSeq}, limit)
}

func (r *EventRepository) ListByRun(ctx context.Context, tenantID domain.TenantID, runID domain.RunID, afterSeq int64, limit int) ([]*domain.Event, error) {
	return r.list(ctx, `tenant_id = $1 AND seq > $2 AND run_id = $3`,
		[]any{string(tenantID), afterSeq, string(runID)}, limit)
}

func (r *EventRepository) list(ctx context.Context, predicate string, args []any, limit int) ([]*domain.Event, error) {
	query := `SELECT ` + eventColumns + ` FROM events WHERE ` + predicate + ` ORDER BY seq ASC`
	if limit > 0 {
		query += ` LIMIT $` + strconv.Itoa(len(args)+1)
		args = append(args, limit)
	}
	rows, err := r.st.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapScan(err)
	}
	defer rows.Close()
	out := []*domain.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
