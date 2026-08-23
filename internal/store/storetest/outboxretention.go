package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// RunOutboxRetention executes the retention/GC contract (ADR-0016) against a
// fresh store per subtest; both adapters must satisfy identical assertions.
//
// The suite is black-box over the port: sweeps report counts, never
// identifiers, so per-row deletion order is pinned through observable
// behavior — class priority under a tight budget (delivered budget first),
// eligibility boundaries, and full drain of eligible rows with ineligible
// rows untouched. DryRun must reproduce a real sweep's numbers exactly,
// including its budget allocation, so previews cannot promise more than one
// bounded round deletes. Adapter-specific tests assert the within-class
// oldest-first victim order against each store's own inspection surface.
func RunOutboxRetention(t *testing.T, f Factory) {
	t.Run("InvalidRequestsAreRejected", func(t *testing.T) {
		repos, _, _ := world(f)
		ctx := context.Background()
		if _, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
			DeliveredOlderThan: -time.Minute, Limit: 10,
		}); ErrKind(err) != domain.ErrKindInvalid {
			t.Fatalf("negative horizon must be invalid, got %v", err)
		}
		if _, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
			Limit: 0,
		}); ErrKind(err) != domain.ErrKindInvalid {
			t.Fatalf("zero limit must be invalid, got %v", err)
		}
		if _, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
			Limit: ports.MaxRetentionSweepBatch + 1,
		}); ErrKind(err) != domain.ErrKindInvalid {
			t.Fatalf("oversized batch must be invalid, got %v", err)
		}
	})
	t.Run("SweepDeletesOnlyEligibleTerminalRowsBeyondHorizon", func(t *testing.T) {
		repos, _, advance := world(f)
		testRetentionSelectiveDeletion(t, repos, advance)
	})
	t.Run("BoundaryAgeAtExactlyHorizonIsEligibleBelowIsRetained", func(t *testing.T) {
		repos, _, advance := world(f)
		testRetentionBoundary(t, repos, advance)
	})
	t.Run("ClassBudgetGoesToDeliveredFirstUnderTightLimit", func(t *testing.T) {
		repos, _, advance := world(f)
		testRetentionClassPriority(t, repos, advance)
	})
	t.Run("RerunsAreIdempotentAndDryRunMatchesReality", func(t *testing.T) {
		repos, _, advance := world(f)
		testRetentionIdempotence(t, repos, advance)
	})
	t.Run("DryRunAppliesTheSameClassPriorityAndBudgetAsSweep", func(t *testing.T) {
		repos, _, advance := world(f)
		testRetentionDryRunBudget(t, repos, advance)
	})
	t.Run("ConcurrentSweepsStayBoundedAndTruthful", func(t *testing.T) {
		repos, _, advance := world(f)
		testRetentionConcurrency(t, repos, advance)
	})
	t.Run("RolledBackUnitRestoresDeletedRows", func(t *testing.T) {
		repos, tx, advance := world(f)
		testRetentionRollbackRestores(t, repos, tx, advance)
	})
	t.Run("GlobalRoundSpansEveryTenant", func(t *testing.T) {
		repos, _, advance := world(f)
		testRetentionGlobalScoping(t, repos, advance)
	})
	t.Run("ZeroHorizonExemptsItsClassEntirely", func(t *testing.T) {
		repos, _, advance := world(f)
		testRetentionZeroExempt(t, repos, advance)
	})
	t.Run("ResultCutoffIsTheStoreClockInstant", func(t *testing.T) {
		w := f()
		advance := w.Advance
		if advance == nil {
			advance = func(time.Duration) {}
		}
		testRetentionCutoffOwnership(t, w.Repos, w.Clock, advance)
	})
}

const retentionHorizon = time.Hour

// seedDelivered drives one message to `delivered` along production's own
// path (publish → lease → ack). Call it only when exactly one due message
// exists; the lease claims precisely that row.
func seedDelivered(t *testing.T, ctx context.Context, repos ports.Repositories, id string) {
	t.Helper()
	if err := repos.Outbox.Publish(ctx, outboxMsg(id, "event:"+id, 3)); err != nil {
		t.Fatalf("publish delivered seed: %v", err)
	}
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{WorkerID: "w1", LeaseFor: time.Hour, Limit: 1})
	if err != nil || len(batch) != 1 || batch[0].ID != id {
		t.Fatalf("lease delivered seed %s: %d %v", id, len(batch), err)
	}
	if err := repos.Outbox.MarkDelivered(ctx, id, "w1"); err != nil {
		t.Fatalf("ack delivered seed %s: %v", id, err)
	}
}

// seedDiscarded drives one message dead and then discards it through the
// operator path (ADR-0015), producing history GC may eventually collect.
func seedDiscarded(t *testing.T, ctx context.Context, repos ports.Repositories, id string) {
	t.Helper()
	if err := repos.Outbox.Publish(ctx, outboxMsg(id, "event:"+id, 1)); err != nil {
		t.Fatalf("publish discarded seed: %v", err)
	}
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{WorkerID: "w1", LeaseFor: time.Hour, Limit: 1})
	if err != nil || len(batch) != 1 || batch[0].ID != id {
		t.Fatalf("lease discarded seed %s: %d %v", id, len(batch), err)
	}
	if err := repos.Outbox.FailWithBackoff(ctx, id, "w1", time.Second, "poison"); err != nil {
		t.Fatalf("fail discarded seed %s: %v", id, err)
	}
	if _, err := repos.Outbox.DiscardDeadLetter(ctx, ports.OutboxMutationRequest{
		TenantID:           tenantID,
		MessageID:          id,
		ExpectedGeneration: 1,
		Actor:              operatorActor(),
	}); err != nil {
		t.Fatalf("discard seed %s: %v", id, err)
	}
}

// seedDead leaves one dead letter in place: open operator work that GC must
// never touch.
func seedDead(t *testing.T, ctx context.Context, repos ports.Repositories, id string) {
	t.Helper()
	if err := repos.Outbox.Publish(ctx, outboxMsg(id, "event:"+id, 1)); err != nil {
		t.Fatal(err)
	}
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{WorkerID: "w1", LeaseFor: time.Hour, Limit: 1})
	if err != nil || len(batch) != 1 || batch[0].ID != id {
		t.Fatalf("lease dead seed %s: %d %v", id, len(batch), err)
	}
	if err := repos.Outbox.FailWithBackoff(ctx, id, "w1", time.Second, "still poisoned"); err != nil {
		t.Fatal(err)
	}
}

func statsOf(t *testing.T, repos ports.Repositories) ports.OutboxStats {
	t.Helper()
	stats, err := repos.Outbox.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func testRetentionSelectiveDeletion(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	seedDelivered(t, ctx, repos, "obx_evt_retold_d")
	seedDead(t, ctx, repos, "obx_evt_retdead")
	advance(2 * retentionHorizon)
	// Fresh terminal rows created after most of the aging have ages below
	// the horizon; the ancient ones are far beyond it.
	seedDelivered(t, ctx, repos, "obx_evt_retfresh_d")
	seedDiscarded(t, ctx, repos, "obx_evt_retfresh_c")
	// Pending and leased rows close the state coverage.
	pendingID, _ := domain.NewID(domain.PrefixEvent)
	if err := repos.Outbox.Publish(ctx, outboxMsg("obx_"+pendingID, "event:"+pendingID, 3)); err != nil {
		t.Fatal(err)
	}

	result, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
		DeliveredOlderThan: retentionHorizon,
		DiscardedOlderThan: retentionHorizon,
		Limit:              100,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.DeletedDelivered != 1 || result.DeletedDiscarded != 0 {
		t.Fatalf("only the ancient delivered row is eligible: %+v", result)
	}

	stats := statsOf(t, repos)
	// pending + fresh delivered + fresh discarded + dead.
	want := ports.OutboxStats{Pending: 1, Delivered: 1, Discarded: 1, Dead: 1}
	if stats != want {
		t.Fatalf("survivors mismatch: got %+v want %+v", stats, want)
	}
	if _, err := repos.Outbox.GetDeadLetter(ctx, tenantID, "obx_evt_retdead"); err != nil {
		t.Fatalf("dead letter must survive every sweep: %v", err)
	}
}

func testRetentionBoundary(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	seedDelivered(t, ctx, repos, "obx_evt_retbound_a")
	req := ports.RetentionSweepRequest{DeliveredOlderThan: retentionHorizon, Limit: 10}

	advance(retentionHorizon - time.Second)
	res, err := repos.Outbox.SweepRetention(ctx, req)
	if err != nil || res.DeletedDelivered != 0 {
		t.Fatalf("age just below the horizon must be retained: %+v %v", res, err)
	}
	advance(time.Second) // age now exactly one horizon
	res, err = repos.Outbox.SweepRetention(ctx, req)
	if err != nil || res.DeletedDelivered != 1 {
		t.Fatalf("age at exactly the horizon must be eligible (inclusive): %+v %v", res, err)
	}
}

func testRetentionClassPriority(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	seedDelivered(t, ctx, repos, "obx_evt_retprio_d")
	seedDiscarded(t, ctx, repos, "obx_evt_retprio_c")
	advance(2 * retentionHorizon)

	req := ports.RetentionSweepRequest{
		DeliveredOlderThan: retentionHorizon,
		DiscardedOlderThan: retentionHorizon,
		Limit:              1,
	}
	first, err := repos.Outbox.SweepRetention(ctx, req)
	if err != nil || first.DeletedDelivered != 1 || first.DeletedDiscarded != 0 {
		t.Fatalf("the single-victim budget must go to the delivered class first: %+v %v", first, err)
	}
	second, err := repos.Outbox.SweepRetention(ctx, req)
	if err != nil || second.DeletedDelivered != 0 || second.DeletedDiscarded != 1 {
		t.Fatalf("the next round must collect the remaining discarded row: %+v %v", second, err)
	}
}

// testRetentionDryRunBudget pins the preview contract when eligible rows
// exceed the limit and both classes compete for one budget: the dry run must
// report exactly what a real sweep with the same request deletes — delivered
// victims first up to the remaining budget, discarded with the remainder —
// never the raw size of the eligible backlog. It also proves previews delete
// nothing.
func testRetentionDryRunBudget(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	for i := range 3 {
		seedDelivered(t, ctx, repos, fmt.Sprintf("obx_evt_retdry_d%d", i))
		advance(time.Millisecond)
	}
	for i := range 2 {
		seedDiscarded(t, ctx, repos, fmt.Sprintf("obx_evt_retdry_c%d", i))
		advance(time.Millisecond)
	}
	advance(2 * retentionHorizon)

	req := ports.RetentionSweepRequest{
		DeliveredOlderThan: retentionHorizon,
		DiscardedOlderThan: retentionHorizon,
		Limit:              4,
		DryRun:             true,
	}
	dry, err := repos.Outbox.SweepRetention(ctx, req)
	if err != nil || dry.DeletedDelivered != 3 || dry.DeletedDiscarded != 1 {
		t.Fatalf("dry run must apply class priority (3 delivered) then spend the remaining budget on one discarded row: %+v %v", dry, err)
	}
	if stats := statsOf(t, repos); stats.Delivered != 3 || stats.Discarded != 2 {
		t.Fatalf("dry run must not delete anything: %+v", stats)
	}

	real := req
	real.DryRun = false
	gotReal, err := repos.Outbox.SweepRetention(ctx, real)
	if err != nil || gotReal.DeletedDelivered != 3 || gotReal.DeletedDiscarded != 1 {
		t.Fatalf("sweep must match the preview exactly: %+v %v", gotReal, err)
	}

	// Five rows were eligible against a budget of four, so the continuation
	// round must still see the leftover discarded row — previews track
	// reality across rounds.
	exhausted, err := repos.Outbox.SweepRetention(ctx, req)
	if err != nil || exhausted.DeletedDelivered != 0 || exhausted.DeletedDiscarded != 1 {
		t.Fatalf("dry run must preview exactly the row left beyond the earlier bound: %+v %v", exhausted, err)
	}
	drain := req
	drain.DryRun = false
	final, err := repos.Outbox.SweepRetention(ctx, drain)
	if err != nil || final.DeletedDelivered != 0 || final.DeletedDiscarded != 1 {
		t.Fatalf("continuation round collects the leftover: %+v %v", final, err)
	}
	if stats := statsOf(t, repos); stats.Delivered+stats.Discarded != 0 {
		t.Fatalf("the full population must be gone after three bounded rounds: %+v", stats)
	}
}

func testRetentionIdempotence(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	for i := range 3 {
		seedDelivered(t, ctx, repos, fmt.Sprintf("obx_evt_retidem%d", i))
		advance(time.Second)
	}
	advance(2 * retentionHorizon)

	req := ports.RetentionSweepRequest{DeliveredOlderThan: retentionHorizon, Limit: 2}
	first, err := repos.Outbox.SweepRetention(ctx, req)
	if err != nil || first.DeletedDelivered != 2 {
		t.Fatalf("bounded round must stop at the limit: %+v %v", first, err)
	}
	dry, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
		DeliveredOlderThan: retentionHorizon, Limit: 100, DryRun: true,
	})
	if err != nil || dry.DeletedDelivered != 1 {
		t.Fatalf("dry run must preview exactly the remaining eligible row: %+v %v", dry, err)
	}
	if stats := statsOf(t, repos); stats.Delivered != 1 {
		t.Fatalf("dry run must not delete anything beyond the earlier bounded round: %+v", stats)
	}
	second, err := repos.Outbox.SweepRetention(ctx, req)
	if err != nil || second.DeletedDelivered != 1 {
		t.Fatalf("continuation round must collect the last row: %+v %v", second, err)
	}
	third, err := repos.Outbox.SweepRetention(ctx, req)
	if err != nil || third.DeletedDelivered != 0 {
		t.Fatalf("rerun past exhaustion must delete nothing: %+v %v", third, err)
	}
}

// testRetentionConcurrency proves concurrent rounds partition work without
// double counting or exceeding bounds: summed truthful counts equal exactly
// the seeded eligible population.
func testRetentionConcurrency(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	const seededPerClass = 6
	for i := range seededPerClass {
		seedDelivered(t, ctx, repos, fmt.Sprintf("obx_evt_retconc_d%d", i))
		advance(time.Millisecond)
		seedDiscarded(t, ctx, repos, fmt.Sprintf("obx_evt_retconc_c%d", i))
		advance(time.Millisecond)
	}
	advance(2 * retentionHorizon)

	var (
		wg             sync.WaitGroup
		mu             sync.Mutex
		sumD, sumC     int64
		sweepErrs      int
		unexpectedKind domain.ErrorKind
	)
	req := ports.RetentionSweepRequest{
		DeliveredOlderThan: retentionHorizon,
		DiscardedOlderThan: retentionHorizon,
		Limit:              4,
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				res, err := repos.Outbox.SweepRetention(ctx, req)
				mu.Lock()
				if err != nil {
					sweepErrs++
					if unexpectedKind == "" {
						unexpectedKind = ErrKind(err)
					}
				} else {
					sumD += res.DeletedDelivered
					sumC += res.DeletedDiscarded
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if sweepErrs > 0 {
		t.Fatalf("concurrent sweeps must never fail: %d errors (kind %s)", sweepErrs, unexpectedKind)
	}
	if sumD != seededPerClass || sumC != seededPerClass {
		t.Fatalf("truthful counts must sum to the seeded population: delivered=%d discarded=%d", sumD, sumC)
	}
	if stats := statsOf(t, repos); stats.Delivered+stats.Discarded != 0 {
		t.Fatalf("every eligible row must be gone: %+v", stats)
	}
}

func testRetentionRollbackRestores(t *testing.T, repos ports.Repositories, tx ports.Transactor, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	seedDelivered(t, ctx, repos, "obx_evt_retrb_d")
	seedDiscarded(t, ctx, repos, "obx_evt_retrb_c")
	advance(2 * retentionHorizon)

	boom := errors.New("rollback the sweep unit")
	err := tx.Do(ctx, func(ctx context.Context) error {
		if _, serr := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
			DeliveredOlderThan: retentionHorizon,
			DiscardedOlderThan: retentionHorizon,
			Limit:              10,
		}); serr != nil {
			return serr
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("unit must fail as scripted, got %v", err)
	}
	if stats := statsOf(t, repos); stats.Delivered != 1 || stats.Discarded != 1 {
		t.Fatalf("a rolled-back unit must restore deleted rows: %+v", stats)
	}

	res, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
		DeliveredOlderThan: retentionHorizon,
		DiscardedOlderThan: retentionHorizon,
		Limit:              10,
	})
	if err != nil || res.DeletedDelivered != 1 || res.DeletedDiscarded != 1 {
		t.Fatalf("the same round outside a failing unit must succeed fully: %+v %v", res, err)
	}
}

func testRetentionGlobalScoping(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedTenant(ctx, t, repos, otherTenID, "other")

	foreignMsg := outboxMsg("obx_evt_retforeign", "event:retforeign", 3)
	foreignMsg.TenantID = otherTenID
	if err := repos.Outbox.Publish(ctx, foreignMsg); err != nil {
		t.Fatal(err)
	}
	fb, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{WorkerID: "w1", LeaseFor: time.Hour, Limit: 1})
	if err != nil || len(fb) != 1 {
		t.Fatalf("lease foreign seed: %d %v", len(fb), err)
	}
	if err := repos.Outbox.MarkDelivered(ctx, fb[0].ID, "w1"); err != nil {
		t.Fatal(err)
	}
	seedDelivered(t, ctx, repos, "obx_evt_retown")
	advance(2 * retentionHorizon)

	// Retention rounds are infrastructure maintenance spanning tenants
	// (ADR-0016): both classes' counts land in one result regardless of
	// which tenant owns them.
	res, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
		DeliveredOlderThan: retentionHorizon, Limit: 10,
	})
	if err != nil || res.DeletedDelivered != 2 {
		t.Fatalf("global round must collect across tenants: %+v %v", res, err)
	}
	if stats := statsOf(t, repos); stats.Delivered != 0 {
		t.Fatalf("no delivered row may survive: %+v", stats)
	}
}

func testRetentionZeroExempt(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	seedDelivered(t, ctx, repos, "obx_evt_retzero_d")
	seedDiscarded(t, ctx, repos, "obx_evt_retzero_c")
	advance(24 * time.Hour)

	// A zero horizon exempts its class even for infinitely old rows: this
	// is the rule that keeps unconfigured deployments inert. The dry run
	// must honor the same exemption — an exempt class is never counted.
	dry, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
		DeliveredOlderThan: 0,
		DiscardedOlderThan: retentionHorizon,
		Limit:              10,
		DryRun:             true,
	})
	if err != nil || dry.DeletedDelivered != 0 || dry.DeletedDiscarded != 1 {
		t.Fatalf("zero horizon must exempt delivered from previews too: %+v %v", dry, err)
	}
	res, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
		DeliveredOlderThan: 0,
		DiscardedOlderThan: retentionHorizon,
		Limit:              10,
	})
	if err != nil || res.DeletedDelivered != 0 || res.DeletedDiscarded != 1 {
		t.Fatalf("zero horizon must exempt delivered while collecting ancient discarded: %+v %v", res, err)
	}
	if stats := statsOf(t, repos); stats.Delivered != 1 || stats.Discarded != 0 {
		t.Fatalf("exempt class must survive untouched: %+v", stats)
	}
}

func testRetentionCutoffOwnership(t *testing.T, repos ports.Repositories, clock ports.Clock, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedDelivered(t, ctx, repos, "obx_evt_retcut_d")
	advance(90 * time.Minute)

	res, err := repos.Outbox.SweepRetention(ctx, ports.RetentionSweepRequest{
		DeliveredOlderThan: retentionHorizon, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clock.Now().IsZero() || !res.Cutoff.Equal(clock.Now()) {
		t.Fatalf("cutoff must be the store clock's own instant: result=%v clock=%v", res.Cutoff, clock.Now())
	}
}
