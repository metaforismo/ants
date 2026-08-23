# Tranche 3 / PR 3.2 — Outbox dead-letter operator tooling: evidence record

Date: 2026-08-23
Branch: `feat/outbox-dead-letter-ops` → PR against `main`
Base: `main` @ 93393f6 ("feat: Prometheus metrics platform (#15)")
Environment: macOS arm64, Go toolchain 1.25.x (staticcheck 2026.2.1
auto-switches to go1.26.7), Docker postgres:16-alpine (disposable),
pnpm/openapi-typescript 7.13.0.

Scope: one Tranche 3 outcome only — dead-letter inspect/requeue/discard per
ADR-0015. Domain state machine, port contract, migration 0007, memory and
PostgreSQL adapters, transactional operator service, metrics amendment,
app wiring, CLI, shared contract suite, runbook. No retention/GC, batch
operations, remote operator APIs, or OpenAPI changes.

## Requirement → code → test matrix

| # | Requirement | Code | Tests |
|---|---|---|---|
| R1 | Delivery lifecycle declared as a domain transition table; operator edges only from `dead` | `internal/domain/outbox.go` table; enumerated in consistency test registry | `TestTransitionTablesAreInternallyConsistent` (outbox_delivery case), `TestOutboxDeliveryStateMachine` |
| R2 | Requeue restarts a bounded lifecycle: attempts→0, availability=store clock present, lease cleared; identity/dedup/envelope/max_attempts/last_error preserved; same max_attempts budget applies again | memory `applyRequeue`; PG guarded UPDATE in `RequeueDeadLetter` | storetest `RequeueRestartsBoundedLifecycleFromDead` (both adapters); `StaleCredentialCannotMutate` proves re-death opens epoch+2 |
| R3 | Discard is terminal, explicit, retained forever by this feature; never claimable again; repeat detected not silent | `applyDiscard` / PG discard UPDATE; no delete path exists | `DiscardIsTerminalExplicitAndRetained`, CLI `TestConfirmedDiscardIsTerminalAndRepeatDetectable` |
| R4 | Monotonic generation CAS: bumps on dead-entry, requeue-from-dead, discard; stale credential = typed `conflict/stale_version`; wrong state = `invalid_transition`; no LWW path | `classifyOperatorMutation` (memory) / `classifyOperatorTarget` (PG); fail-closed re-check after 0-row UPDATE | `StaleCredentialCannotMutate`, `ConcurrentRequeueDiscardAndLeaseHaveOneWinner`, live smoke race (below) |
| R5 | Unknown and foreign-tenant targets are uniform not-found through every operator surface (anti-enumeration, ADR-0004) | ID-index lookups scoped by tenant before status classification | `RequeueRejectsWrongStateMissingAndForeignUniformly`, `GetShowsDeadAndDiscardedUniformNotFoundOtherwise`, live smoke cross-tenant show |
| R6 | Every mutation commits row + versioned event (`outbox.dead_letter.{requeued,discarded}.v1`, aggregate_version=post-gen) + durable delivery + tenant-scoped audit in ONE unit; any failure rolls all back | `internal/outboxops/service.go` `mutate` over `ports.Transactor`; PG repo joins caller tx via `Store.Do` nesting | `TestRequeueComposesEventDeliveryAndAudit`, `TestAuditFailureRollsBackWholeUnit`, `TestEventFailureRollsBackWholeUnit`, PG `TestOutboxOperatorAuditFailureRollsBackOnPostgres` |
| R7 | Listing deterministic `(created_at,id)` keyset pages in [1,200]; summaries envelope-free; cause ≤512 chars | `ListDeadLetters` both adapters; partial index `outbox_dead_letter_idx`; `DeadLetterSummary` has no envelope field | `ListReturnsOnlyDeadLettersDeterministically`, `ListPaginationBoundariesAreExact`, `ListRejectsInvalidRequests`, live leakage sweep |
| R8 | Metrics amendment via consumer-side observer seam; fixed vocabularies; identifiers never labels; discarded gauge state | `metrics.Metrics.ActionRecorded`; `ants_outbox_operator_actions_total{action,outcome}`; ADR-0014 amendment section | `wantNames` pin, label assertions in `TestRegistryExposesPromisedInstruments`, outboxops `TestObserverSeesTypedOutcomeVocabulary`, `TestNilObserverKeepsBehaviorIdentical` |
| R9 | CLI posture: explicit --tenant/--actor, optional --reason/--trace-id, discard gated by --yes with usage exit 2 and no interactive prompt, stable result line, JSON lines, typed error triples, exit 0/1/2 | `internal/cli/outbox.go` | cli tests incl. `TestDeadLetterListHumanOutputAndCursor`, `TestDiscardRefusesWithoutConfirmation`, `TestUnknownMessageRendersTypedErrorTriple`, `TestFlagsAreAcceptedOnBothSidesOfThePositional` |
| R10 | Forward-only migration adds generation/dead_at/discarded_at, extends status check to 'discarded', backfills legacy dead rows to generation 1 | `db/migrations/0007_outbox_operator.sql` | migrate suite + full contract suite against real PG16 (`scripts/test-postgres.sh`) |
| R11 | Memory UoW snapshot keeps ONE canonical outbox row across slice/ID-index/dedup-index during backup AND restore; rollback restores struct+envelope exactly | `internal/store/memory/snapshot.go` single old-pointer→clone map | `TestOutboxRollbackRestoresCanonicalRow` (fails on both prior variants; see audit F1) |
| R12 | Operator actions exist in policy vocabulary but are structurally denied by default rule; agent paths cannot reach them | `domain/policy.go` ActionOutbox{Requeue,Discard}; engine default deny unchanged | pre-existing policy default-deny tests; no wiring added |

## Audit findings fixed this session

- **F1 — Memory snapshot forked canonical rows (FIXED, mandatory item):**
  `backup()` deep-cloned the three outbox containers independently, so one
  logical row became three distinct clones. After ANY rolled-back unit,
  later in-place mutations through one view (e.g. discard via `outboxByID`)
  were invisible to the others (Stats/List read the slice). Fixed with a
  single old-pointer→clone mapping used for all three containers; restore
  preserves alignment by installing those pointers as-is.
  `TestOutboxRollbackRestoresCanonicalRow` was proven to FAIL on both prior
  variants: HEAD (no clone at all → rollback did not restore) and
  per-container cloning (three distinct pointers asserted).
- **F2 — PG `dead_at` stamped the wrong instant (FIXED):** FailWithBackoff's
  CASE reused the retry-at parameter, stamping `now+retryIn` instead of the
  death instant. Now passes the store clock separately ($5), matching the
  memory adapter.
- **F3 — Legacy dead rows were un-operable (FIXED):** pre-0007 dead rows
  defaulted to generation 0, which `Validate()` rejects as a credential
  forever. Migration now backfills `status='dead'` rows to generation 1
  (they entered dead exactly once). Live rows keep 0 = never an operator
  target.
- **F4 — CLI rejected its own documented argument order (FIXED):** stdlib
  flag parsing stops at the first positional, so
  `requeue <id> --reason R` (the exact shape in the runbook) died with a
  usage error — caught by the live PostgreSQL smoke, not by unit tests.
  Added `reorderPositionalsLast` splitting flags from positionals using the
  declared FlagSet; pinned by `TestFlagsAreAcceptedOnBothSidesOfThePositional`.
  Also replaced a hand-formatted fake error triple with the store's real
  typed classification (single source of truth).
- **F5 — Vocabulary/doc drift and slop (FIXED):** outcome vocabulary gained
  `invalid_request` in code but not in ADR-0015 text; ADR-0014 instrument
  list amended (reviewed intent) and ADR-0015 aligned to the six outcomes;
  `_ = after` test slop removed from service_test.

## CLI concurrency credential (documented behavior)

`ants outbox dead-letter requeue/discard` reads the current generation via
`show` immediately before mutating and presents it as the CAS credential.
This read is a convenience so scripts act on what they just saw — it is NOT
a lock. Under concurrent operators the store-side compare-and-swap still
admits exactly one winner; every loser observes
`error: conflict: stale_version: ...` naming the newer generation, exit 1,
with no partial state. Proven live: six simultaneous CLI processes on one
credential produced exactly one success line, five typed conflicts, and
exactly one audit record. Replaying a lost response surfaces the same typed
conflict rather than pretending idempotent success.

## Gate results (exact commands, final code state)

| Gate | Command | Result |
|---|---|---|
| Format | `gofmt -l .` | PASS (no output) |
| Vet | `go vet ./...` | PASS |
| Lint | `go run honnef.co/go/tools/cmd/staticcheck@2026.2.1 ./...` | PASS (after fixing SA4006 in the new arg splitter) |
| Tidy idempotence | `go mod tidy && git diff --exit-code -- go.mod go.sum` | PASS (no changes) |
| Manifest | `go run ./scripts/manifestcheck` | PASS (no new dependencies this tranche) |
| Unit | `go test ./...` | PASS (19 packages) |
| Race | `go test -race ./...` | PASS (benign macOS LC_DYSYMTAB linker warnings only) |
| Focused stress | `go test -race -count=60 ./internal/store/storetest/ ./internal/outboxops/ ./internal/store/memory/ ./internal/cli/` | PASS (60 reps of all fencing/concurrency suites) |
| PG16 integration | `./scripts/test-postgres.sh` (disposable postgres:16-alpine; migrate + postgres + storetest incl. full operator contract) | PASS (run against final code state) |
| Full CI | `make ci` | PASS (fmt, vet, lint, tidy, manifest, test, race, build, contracts test + drift) |
| Contracts drift | via `make ci` (`contracts-drift`) | PASS — OpenAPI/schema.d.ts untouched by this tranche |
| Build | `make build` | PASS (bin/ants, bin/ants-api) |
| Demo | `make demo` | PASS |
| Live CLI smoke | disposable ants-smoke-pg, real PG16 through bin/ants (script below) | PASS (all checks) |

## Live PostgreSQL CLI smoke (all PASS)

Provisioned a fresh `ants-smoke-pg` postgres:16-alpine container, ran
`ants migrate up` (0001–0007), seeded one tenant plus two production-shaped
dead letters (status dead, attempts=max_attempts, generation 1, dead_at set,
bounded cause) and one pending live message via SQL fixtures — the
dispatcher's own path to death is already pinned by the PG contract suite;
the smoke exercises the operator surface end to end through the real binary:

- list text + `--json`: deterministic order, page-hint token round-trips,
  live message absent, no envelope bytes anywhere;
- show: status/generation/attempts/cause rendered; uniform not-found for a
  live message, a foreign tenant, and an unknown id;
- requeue: stable result line `requeued <id> (generation 2, attempts_before 5)`;
  event `outbox.dead_letter.requeued.v1` (aggregate_version=2, actor,
  trace-id, reason), its durable outbox delivery (dedup key derived from the
  event id), and exactly one audit record all visible in psql;
- discard without `--yes`: refuses with explanation, exit != 0, row and audit
  untouched;
- confirmed discard: terminal line, retained history readable with
  discarded_at, `.discarded.v1` event committed;
- concurrent race: six simultaneous CLI mutations on one credential —
  exactly one winner, five `error: conflict: stale_version:` losers, final
  row either `2:pending` or `2:discarded`, exactly one audit record;
- leakage sweep: envelope marker string absent from every produced output.

All scratch files used repo-local `.local/.log` names and were deleted; both
smoke containers were removed afterwards (`docker rm -f ants-smoke-pg`).

## Known limitations

- Legacy (pre-migration) dead letters have `dead_at = NULL`; their death
  instant predates the column. Cause and generation are intact and they are
  fully operable.
- Text-mode list always prints the next-page hint after a non-empty page; a
  keyset cursor cannot know whether a following page exists without reading
  ahead. Harmless but worth knowing in scripts.
- The CLI's pre-mutation generation read is per-invocation; between the read
  and the mutation another actor may win — that is the documented typed
  conflict above, not a defect.
- Operator actions are structurally denied by the policy engine's default
  rule until authenticated operator principals exist (ADR-0015); the CLI is
  the trusted local seam, equivalent in trust to `migrate up`.
- Discard retention is forever by design; retention/GC policy remains
  deferred (ADR-0011/0015).
- Metrics sample after dispatcher rounds only; operator actions are counted
  inline at the service seam, so `ants_outbox_messages{state="dead"}` can
  lag CLI-visible reality by up to one poll interval.

## NOT RUN / BLOCKED

None. Every gate listed above was executed to completion in this session
against the final code state. Push/PR steps were performed immediately after
this record was written.
