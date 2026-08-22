package memory

import (
	"context"

	"github.com/metaforismo/ants/internal/ports"
)

// transactor implements ports.Transactor over the deterministic store.
//
// Memory-mode units are real: fn runs against a snapshot of the whole state
// that is discarded on error or panic, so partial multi-record transitions
// can never be observed. Units serialize on a dedicated mutex; individual
// operations inside fn still take the shared state lock as usual.
type transactor struct{ st *storeState }

var _ ports.Transactor = (*transactor)(nil)

// unitKey marks contexts already inside a unit so nested Do calls join the
// outer unit instead of deadlocking on the non-reentrant unit mutex.
type unitKey struct{}

func (t *transactor) Do(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if ctx.Value(unitKey{}) != nil {
		return fn(ctx)
	}
	t.st.unitMu.Lock()
	defer t.st.unitMu.Unlock()

	backup := t.st.backup()
	defer func() {
		if r := recover(); r != nil {
			t.st.restore(backup)
			panic(r)
		}
	}()
	unitCtx := context.WithValue(ctx, unitKey{}, true)
	if err = fn(unitCtx); err != nil {
		t.st.restore(backup)
		return err
	}
	return nil
}
