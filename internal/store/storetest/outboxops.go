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

// RunOutboxOperator executes the dead-letter operator contract (ADR-0015)
// against a fresh store per subtest; both adapters must satisfy identical
// assertions. All time-based behavior flows through the world's Advance hook.
func RunOutboxOperator(t *testing.T, f Factory) {
	t.Run("ListReturnsOnlyDeadLettersDeterministically", func(t *testing.T) {
		repos, _, advance := world(f)
		testOperatorListDeterministic(t, repos, advance)
	})
	t.Run("ListPaginationBoundariesAreExact", func(t *testing.T) {
		repos, _, _ := world(f)
		testOperatorPaginationBoundaries(t, repos)
	})
	t.Run("ListRejectsInvalidRequests", func(t *testing.T) {
		repos, _, _ := world(f)
		ctx := context.Background()
		if _, err := repos.Outbox.ListDeadLetters(ctx, ports.ListDeadLettersRequest{
			TenantID: tenantID, Limit: 0,
		}); ErrKind(err) != domain.ErrKindInvalid {
			t.Fatalf("zero limit must be invalid, got %v", err)
		}
		if _, err := repos.Outbox.ListDeadLetters(ctx, ports.ListDeadLettersRequest{
			TenantID: tenantID, Limit: ports.MaxDeadLetterPageSize + 1,
		}); ErrKind(err) != domain.ErrKindInvalid {
			t.Fatalf("oversized page must be invalid, got %v", err)
		}
	})
	t.Run("GetShowsDeadAndDiscardedUniformNotFoundOtherwise", func(t *testing.T) {
		repos, _, _ := world(f)
		testOperatorGet(t, repos)
	})
	t.Run("RequeueRestartsBoundedLifecycleFromDead", func(t *testing.T) {
		repos, _, advance := world(f)
		testOperatorRequeueRestart(t, repos, advance)
	})
	t.Run("RequeueRejectsWrongStateMissingAndForeignUniformly", func(t *testing.T) {
		repos, _, _ := world(f)
		testOperatorRequeueRejections(t, repos)
	})
	t.Run("StaleCredentialCannotMutate", func(t *testing.T) {
		repos, _, _ := world(f)
		testOperatorStaleCredential(t, repos)
	})
	t.Run("DiscardIsTerminalExplicitAndRetained", func(t *testing.T) {
		repos, _, advance := world(f)
		testOperatorDiscardTerminal(t, repos, advance)
	})
	t.Run("StatsPartitionIncludesDiscarded", func(t *testing.T) {
		repos, _, _ := world(f)
		testOperatorStatsPartition(t, repos)
	})
	t.Run("ConcurrentRequeueDiscardAndLeaseHaveOneWinner", func(t *testing.T) {
		repos, _, advance := world(f)
		testOperatorConcurrencyStress(t, repos, advance)
	})
}

// seedDeadLetter drives one message through publish → lease → terminal
// failure (max_attempts must be 1 so a single failure dead-letters it) along
// the same path production takes. Returns the summary carrying the fencing
// credential.
func seedDeadLetter(t *testing.T, ctx context.Context, repos ports.Repositories, id string) ports.DeadLetterSummary {
	t.Helper()
	if err := repos.Outbox.Publish(ctx, outboxMsg(id, "event:"+id, 1)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 1,
	})
	if err != nil || len(batch) != 1 || batch[0].ID != id {
		got := make([]string, len(batch))
		for i := range batch {
			got[i] = batch[i].ID
		}
		t.Fatalf("lease seeded message %s: claimed %v (n=%d, err=%v)", id, got, len(batch), err)
	}
	if err := repos.Outbox.FailWithBackoff(ctx, batch[0].ID, "w1", time.Second, "sink unavailable"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	got, err := repos.Outbox.GetDeadLetter(ctx, tenantID, id)
	if err != nil {
		t.Fatalf("dead letter must be readable after death: %v", err)
	}
	if got.Status != domain.OutboxDead || got.Generation < 1 || got.Cause != "sink unavailable" {
		t.Fatalf("seeded message must be dead with a credential and cause: %+v", got)
	}
	return *got
}

func operatorActor() domain.Actor {
	return domain.Actor{Type: domain.PrincipalHuman, ID: string(principal)}
}

func testOperatorListDeterministic(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedTenant(ctx, t, repos, otherTenID, "other")

	var ids []string
	for range 3 {
		idStr, _ := domain.NewID(domain.PrefixEvent)
		id := "obx_" + idStr
		ids = append(ids, id)
		seedDeadLetter(t, ctx, repos, id)
		// Strictly increasing created_at makes the ordering assertion
		// independent of how the random ids happen to sort.
		advance(time.Second)
	}

	// A foreign-tenant dead letter must never appear in this tenant's list.
	foreignMsg := outboxMsg("obx_evt_foreignlist", "event:foreignlist", 1)
	foreignMsg.TenantID = otherTenID
	if err := repos.Outbox.Publish(ctx, foreignMsg); err != nil {
		t.Fatal(err)
	}
	fb, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{WorkerID: "w1", LeaseFor: time.Minute, Limit: 5})
	if err != nil || len(fb) != 1 {
		t.Fatalf("lease foreign seed: %d %v", len(fb), err)
	}
	if err := repos.Outbox.FailWithBackoff(ctx, fb[0].ID, "w1", time.Second, "x"); err != nil {
		t.Fatal(err)
	}

	page, err := repos.Outbox.ListDeadLetters(ctx, ports.ListDeadLettersRequest{
		TenantID: tenantID, Limit: ports.MaxDeadLetterPageSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 {
		t.Fatalf("exactly three own dead letters expected, got %d", len(page))
	}
	for i, s := range page {
		if s.ID != ids[i] {
			t.Fatalf("list order must follow (created_at, id): position %d = %s, want %s", i, s.ID, ids[i])
		}
		if s.Status != domain.OutboxDead || s.DeadAt == nil {
			t.Fatalf("unexpected summary: %+v", s)
		}
	}
}

func testOperatorPaginationBoundaries(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	// No clock movement: all rows tie on created_at and the id tiebreak
	// alone defines the deterministic order.
	ids := []string{
		"obx_evt_pagination00",
		"obx_evt_pagination01",
		"obx_evt_pagination02",
		"obx_evt_pagination03",
	}
	for _, id := range ids {
		seedDeadLetter(t, ctx, repos, id)
	}

	firstPage, err := repos.Outbox.ListDeadLetters(ctx, ports.ListDeadLettersRequest{
		TenantID: tenantID, Limit: 2,
	})
	if err != nil || len(firstPage) != 2 {
		t.Fatalf("first page: %d %v", len(firstPage), err)
	}
	if firstPage[0].ID != ids[0] || firstPage[1].ID != ids[1] {
		t.Fatalf("first page contents mismatched: %+v", firstPage)
	}
	last := firstPage[len(firstPage)-1]
	secondPage, err := repos.Outbox.ListDeadLetters(ctx, ports.ListDeadLettersRequest{
		TenantID:       tenantID,
		AfterCreatedAt: last.CreatedAt,
		AfterID:        last.ID,
		Limit:          2,
	})
	if err != nil || len(secondPage) != 2 {
		t.Fatalf("second page: %d %v", len(secondPage), err)
	}
	if secondPage[0].ID != ids[2] || secondPage[1].ID != ids[3] {
		t.Fatalf("keyset cursor must resume exactly after the boundary: %+v", secondPage)
	}

	pastEnd, err := repos.Outbox.ListDeadLetters(ctx, ports.ListDeadLettersRequest{
		TenantID:       tenantID,
		AfterCreatedAt: secondPage[1].CreatedAt,
		AfterID:        secondPage[1].ID,
		Limit:          2,
	})
	if err != nil || len(pastEnd) != 0 {
		t.Fatalf("cursor past the end must be empty: %d %v", len(pastEnd), err)
	}
}

func testOperatorGet(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedTenant(ctx, t, repos, otherTenID, "other")
	dead := seedDeadLetter(t, ctx, repos, "obx_evt_getcheck")

	if _, err := repos.Outbox.DiscardDeadLetter(ctx, ports.OutboxMutationRequest{
		TenantID:           tenantID,
		MessageID:          dead.ID,
		ExpectedGeneration: dead.Generation,
		Actor:              operatorActor(),
	}); err != nil {
		t.Fatalf("discard: %v", err)
	}

	got, err := repos.Outbox.GetDeadLetter(ctx, tenantID, dead.ID)
	if err != nil {
		t.Fatalf("discarded row must stay readable: %v", err)
	}
	if got.Status != domain.OutboxDiscarded || got.DiscardedAt == nil || got.DeadAt == nil {
		t.Fatalf("terminal decision must be explicit on the retained row: %+v", got)
	}

	if _, err := repos.Outbox.GetDeadLetter(ctx, otherTenID, dead.ID); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("cross-tenant read must be uniform not-found, got %v", err)
	}
	if _, err := repos.Outbox.GetDeadLetter(ctx, tenantID, "obx_evt_missing"); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("missing read must be not-found, got %v", err)
	}
}

func testOperatorRequeueRestart(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	idStr, _ := domain.NewID(domain.PrefixEvent)
	id := "obx_" + idStr
	before := seedDeadLetter(t, ctx, repos, id)

	advance(2 * time.Hour)

	result, err := repos.Outbox.RequeueDeadLetter(ctx, ports.OutboxMutationRequest{
		TenantID:           tenantID,
		MessageID:          id,
		ExpectedGeneration: before.Generation,
		Actor:              operatorActor(),
	})
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if result.Generation != before.Generation+1 || result.AttemptsBefore != before.Attempts || result.Action != "requeue" {
		t.Fatalf("result must carry the post-transition epoch: %+v", result)
	}

	// The fresh lifecycle is immediately claimable without further time
	// movement, restarts its attempt budget, and preserves identity fields.
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w2", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(batch) != 1 || batch[0].ID != id {
		t.Fatalf("requeued message must be due immediately: %d %v", len(batch), err)
	}
	if batch[0].Attempts != 1 {
		t.Fatalf("attempt budget must restart from zero: %+v", batch[0])
	}
	if batch[0].DedupKey != before.DedupKey || batch[0].MaxAttempts != before.MaxAttempts {
		t.Fatalf("identity and delivery bound must be preserved: %+v", batch[0])
	}

	// The restarted lifecycle still dead-letters when its budget exhausts;
	// entering dead opens yet another epoch beyond the stale credential.
	if err := repos.Outbox.FailWithBackoff(ctx, id, "w2", time.Second, "still broken"); err != nil {
		t.Fatal(err)
	}
	stats, _ := repos.Outbox.Stats(ctx)
	if stats.Dead != 1 {
		t.Fatalf("fresh lifecycle must remain bounded by max_attempts: %+v", stats)
	}
	afterDeath, err := repos.Outbox.GetDeadLetter(ctx, tenantID, id)
	if err != nil {
		t.Fatal(err)
	}
	if afterDeath.Generation != before.Generation+2 {
		t.Fatalf("requeue plus second death must move two epochs: %+v", afterDeath)
	}
	if afterDeath.Cause != "still broken" || afterDeath.DeadAt == nil {
		t.Fatalf("second death must restamp cause and dead_at: %+v", afterDeath)
	}
}

func testOperatorRequeueRejections(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	seedTenant(ctx, t, repos, otherTenID, "other")

	idStr, _ := domain.NewID(domain.PrefixEvent)
	id := "obx_" + idStr
	dead := seedDeadLetter(t, ctx, repos, id)

	mut := func(tid domain.TenantID, msgID string, gen int64) ports.OutboxMutationRequest {
		return ports.OutboxMutationRequest{TenantID: tid, MessageID: msgID, ExpectedGeneration: gen, Actor: operatorActor()}
	}

	// Live messages are not operator targets: a pending message rejects
	// requeue as an invalid transition rather than being silently mutated.
	liveStr, _ := domain.NewID(domain.PrefixEvent)
	live := outboxMsg("obx_"+liveStr, "event:"+liveStr, 3)
	if err := repos.Outbox.Publish(ctx, live); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Outbox.RequeueDeadLetter(ctx, mut(tenantID, live.ID, 1)); ErrKind(err) != domain.ErrKindInvalidTransition {
		t.Fatalf("pending message must reject requeue as invalid transition")
	}
	if _, err := repos.Outbox.DiscardDeadLetter(ctx, mut(tenantID, live.ID, 1)); ErrKind(err) != domain.ErrKindInvalidTransition {
		t.Fatalf("pending message must reject discard as invalid transition")
	}

	// Unknown messages are uniformly not-found...
	if _, err := repos.Outbox.RequeueDeadLetter(ctx, mut(tenantID, "obx_evt_missing", 1)); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("unknown message must be not-found")
	}
	// ...indistinguishable from foreign-tenant messages.
	if _, err := repos.Outbox.RequeueDeadLetter(ctx, mut(otherTenID, dead.ID, dead.Generation)); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("foreign-tenant mutation must be uniform not-found")
	}
	if _, err := repos.Outbox.DiscardDeadLetter(ctx, mut(otherTenID, dead.ID, dead.Generation)); ErrKind(err) != domain.ErrKindNotFound {
		t.Fatalf("foreign-tenant discard must be uniform not-found")
	}

	// Structurally unusable requests are rejected before any state read.
	if _, err := repos.Outbox.RequeueDeadLetter(ctx, mut(tenantID, dead.ID, 0)); ErrKind(err) != domain.ErrKindInvalid {
		t.Fatalf("generation 0 cannot match any dead row")
	}
	noActor := ports.OutboxMutationRequest{TenantID: tenantID, MessageID: dead.ID, ExpectedGeneration: dead.Generation}
	if _, err := repos.Outbox.DiscardDeadLetter(ctx, noActor); ErrKind(err) != domain.ErrKindInvalid {
		t.Fatalf("mutation without an actor must be invalid")
	}

	// Nothing above may have consumed or altered the dead letter.
	got, gerr := repos.Outbox.GetDeadLetter(ctx, tenantID, dead.ID)
	if gerr != nil || got.Status != domain.OutboxDead || got.Generation != dead.Generation {
		t.Fatalf("rejected mutations must leave the dead letter intact: %+v %v", got, gerr)
	}
}

func testOperatorStaleCredential(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	dead := seedDeadLetter(t, ctx, repos, "obx_evt_stale")

	mut := func(gen int64) ports.OutboxMutationRequest {
		return ports.OutboxMutationRequest{
			TenantID: tenantID, MessageID: dead.ID, ExpectedGeneration: gen, Actor: operatorActor(),
		}
	}

	// The first operator wins with the current credential...
	if _, err := repos.Outbox.RequeueDeadLetter(ctx, mut(dead.Generation)); err != nil {
		t.Fatalf("first requeue: %v", err)
	}
	// ...a second operator presenting the SAME credential loses to fencing.
	if _, err := repos.Outbox.RequeueDeadLetter(ctx, mut(dead.Generation)); ErrKind(err) != domain.ErrKindConflict {
		t.Fatalf("stale credential must conflict")
	}

	// After the message dies a second time the epoch moved twice: the
	// original credential must STILL be powerless against the newer state.
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w2", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(batch) != 1 {
		t.Fatalf("lease requeued: %d %v", len(batch), err)
	}
	if err := repos.Outbox.FailWithBackoff(ctx, batch[0].ID, "w2", time.Second, "again"); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Outbox.RequeueDeadLetter(ctx, mut(dead.Generation)); ErrKind(err) != domain.ErrKindConflict {
		t.Fatalf("credential from the first epoch must never act on the third")
	}
}

func testOperatorDiscardTerminal(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")
	dead := seedDeadLetter(t, ctx, repos, "obx_evt_discard")

	result, err := repos.Outbox.DiscardDeadLetter(ctx, ports.OutboxMutationRequest{
		TenantID:           tenantID,
		MessageID:          dead.ID,
		ExpectedGeneration: dead.Generation,
		Actor:              operatorActor(),
		Reason:             "poison payload, triaged",
	})
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if result.Generation != dead.Generation+1 || result.Action != "discard" {
		t.Fatalf("discard must bump the epoch: %+v", result)
	}

	// Discarded history is never reclaimable by the dispatcher, no matter
	// how much time passes.
	advance(24 * time.Hour)
	batch, err := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
		WorkerID: "w1", LeaseFor: time.Minute, Limit: 10,
	})
	if err != nil || len(batch) != 0 {
		t.Fatalf("discarded rows must never be leased: %d %v", len(batch), err)
	}

	// History is retained: the row stays queryable with its provenance.
	got, err := repos.Outbox.GetDeadLetter(ctx, tenantID, dead.ID)
	if err != nil {
		t.Fatalf("discard must retain history, got %v", err)
	}
	if got.Status != domain.OutboxDiscarded || got.Cause != dead.Cause || got.DeadAt == nil || got.DiscardedAt == nil {
		t.Fatalf("retained row must keep death and decision timestamps: %+v", got)
	}

	// Repeating the decision with the old credential is detected as a stale
	// replay, not silently idempotent; requeueing discarded history with a
	// FRESH credential is an invalid transition — the row is terminal, not
	// resurrectable.
	repeat := ports.OutboxMutationRequest{
		TenantID: tenantID, MessageID: dead.ID, ExpectedGeneration: dead.Generation, Actor: operatorActor(),
	}
	if _, err := repos.Outbox.DiscardDeadLetter(ctx, repeat); ErrKind(err) != domain.ErrKindConflict {
		t.Fatalf("repeat discard must surface as conflict")
	}
	fresh := repeat
	fresh.ExpectedGeneration = got.Generation
	if _, err := repos.Outbox.RequeueDeadLetter(ctx, fresh); ErrKind(err) != domain.ErrKindInvalidTransition {
		t.Fatalf("requeue of discarded history must be invalid transition")
	}
}

func testOperatorStatsPartition(t *testing.T, repos ports.Repositories) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	dead := seedDeadLetter(t, ctx, repos, "obx_evt_statsdead")
	liveStr, _ := domain.NewID(domain.PrefixEvent)
	if err := repos.Outbox.Publish(ctx, outboxMsg("obx_"+liveStr, "event:"+liveStr, 3)); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Outbox.DiscardDeadLetter(ctx, ports.OutboxMutationRequest{
		TenantID: tenantID, MessageID: dead.ID, ExpectedGeneration: dead.Generation, Actor: operatorActor(),
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := repos.Outbox.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.Discarded != 1 || stats.Dead != 0 {
		t.Fatalf("stats must partition all messages incl. discarded: %+v", stats)
	}
	if total := stats.Pending + stats.Leased + stats.Delivered + stats.Dead + stats.Discarded; total != 2 {
		t.Fatalf("stats must account for every row: %+v", stats)
	}
}

// testOperatorConcurrencyStress races two operators (requeue vs discard)
// against each other and against dispatcher claim steps over one dead
// message: at most one operator mutation may win per round, every loser
// observes a typed error, and a dead row must never be claimable.
func testOperatorConcurrencyStress(t *testing.T, repos ports.Repositories, advance func(time.Duration)) {
	ctx := context.Background()
	seedTenant(ctx, t, repos, tenantID, "acme")

	const rounds = 6
	winningRounds := 0
	for round := range rounds {
		id := fmt.Sprintf("obx_evt_stress%02d", round)
		dead := seedDeadLetter(t, ctx, repos, id)

		const contenders = 6
		results := make([]error, contenders)
		var wg sync.WaitGroup
		for c := range contenders {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				req := ports.OutboxMutationRequest{
					TenantID:           tenantID,
					MessageID:          id,
					ExpectedGeneration: dead.Generation,
					Actor:              operatorActor(),
				}
				switch n % 3 {
				case 0:
					_, results[n] = repos.Outbox.DiscardDeadLetter(ctx, req)
				case 1:
					_, results[n] = repos.Outbox.RequeueDeadLetter(ctx, req)
				default:
					batch, lerr := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
						WorkerID: fmt.Sprintf("stress-%d-%d", round, n),
						LeaseFor: time.Minute, Limit: 10,
					})
					if lerr != nil {
						results[n] = lerr
						return
					}
					for _, m := range batch {
						if m.ID == id {
							results[n] = errors.New("dispatcher claimed a dead row")
						}
					}
				}
			}(c)
		}
		wg.Wait()

		mutationWins := 0
		for n, err := range results {
			switch {
			case n%3 == 2:
				// Dispatcher contender: nil means it claimed none of ours.
			case err == nil:
				mutationWins++
			case ErrKind(err) == domain.ErrKindConflict ||
				ErrKind(err) == domain.ErrKindInvalidTransition ||
				ErrKind(err) == domain.ErrKindNotFound ||
				ErrKind(err) == domain.ErrKindInvalid:
				// Expected typed losses under contention.
			default:
				t.Fatalf("round %d contender %d: unclassified concurrent error: %v", round, n, err)
			}
		}
		if mutationWins > 1 {
			t.Fatalf("round %d: more than one mutation won (%d)", round, mutationWins)
		}
		winningRounds += mutationWins

		// Settle the round's row back to dead so later rounds start clean.
		// A requeued winner is pending (and thus invisible to Get, which
		// shows only dead/discarded), and a dispatcher contender may hold an
		// unexpired lease on it — expire that lease, then drain whatever
		// claims are available until the row stops appearing. With
		// max_attempts=1 every reclaimed claim dead-letters on its failure
		// recording, so the loop terminates in at most a few passes.
		for pass := 0; pass < 4; pass++ {
			advance(2 * time.Minute)
			batch, lerr := repos.Outbox.Lease(ctx, ports.OutboxLeaseRequest{
				WorkerID: "drain", LeaseFor: time.Minute, Limit: 10,
			})
			if lerr != nil {
				t.Fatal(lerr)
			}
			found := false
			for _, m := range batch {
				if m.ID == id {
					found = true
					if ferr := repos.Outbox.FailWithBackoff(ctx, m.ID, "drain", time.Second, "drain"); ferr != nil {
						t.Fatal(ferr)
					}
				}
			}
			if !found {
				break
			}
		}
		got, gerr := repos.Outbox.GetDeadLetter(ctx, tenantID, id)
		if gerr != nil || (got.Status != domain.OutboxDead && got.Status != domain.OutboxDiscarded) {
			t.Fatalf("round %d: settlement must leave the row terminal: %+v %v", round, got, gerr)
		}
	}
	if winningRounds == 0 {
		t.Fatalf("contention must sometimes let an operator win")
	}
}
