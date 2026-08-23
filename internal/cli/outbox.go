package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/ports"
)

// Outbox dead-letter commands (ADR-0015): inspect, requeue, and discard
// poison deliveries with full audit provenance. Every mutation requires an
// explicit tenant and actor so events and audit records say who intervened;
// discard additionally requires --yes because it is terminal.
//
// Output contract: one line per result on stdout, RFC-3339 timestamps,
// bounded cause text, JSON lines under --json. Errors go to stderr as
// "error: <kind>: <code>: <message>" with exit code 1; usage problems exit 2.
func runOutbox(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		outboxUsage(stdout)
		return exitOK
	}
	switch args[0] {
	case "dead-letter":
		return runDeadLetter(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown outbox command %q\n\n", args[0])
		outboxUsage(stderr)
		return exitUsage
	}
}

func outboxUsage(w io.Writer) {
	fmt.Fprint(w, `ants outbox - dead-letter operations

Usage:
  ants outbox dead-letter list  --tenant <id> [--limit N] [--after TOKEN] [--json]
  ants outbox dead-letter show  --tenant <id> <message-id> [--json]
  ants outbox dead-letter requeue --tenant <id> --actor <id> <message-id> [--reason R] [--trace-id T]
  ants outbox dead-letter discard --tenant <id> --actor <id> --yes <message-id> [--reason R] [--trace-id T]

Requeue restarts a fresh bounded delivery lifecycle. Discard is terminal and
retains the row as history; it refuses to run without --yes. Mutations are
compare-and-swap guarded: a conflict names the newer generation to read via
show before acting again.
`)
}

func runDeadLetter(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		outboxUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "list":
		return runDeadLetterList(args[1:], stdout, stderr)
	case "show":
		return runDeadLetterShow(args[1:], stdout, stderr)
	case "requeue":
		return runDeadLetterMutate(args[1:], stdout, stderr, actionRequeue)
	case "discard":
		return runDeadLetterMutate(args[1:], stdout, stderr, actionDiscard)
	default:
		fmt.Fprintf(stderr, "unknown dead-letter command %q\n", args[0])
		outboxUsage(stderr)
		return exitUsage
	}
}

const (
	actionRequeue = "requeue"
	actionDiscard = "discard"
)

// buildWorld loads config and constructs the application for one command
// invocation; the composition root stays single (ADR-0015).
func buildWorld(stderr io.Writer, configPath string) (*app.App, int) {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "configuration invalid:\n%v\n", err)
		return nil, exitFailure
	}
	application, err := app.Build(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "failed to start:\n%v\n", err)
		return nil, exitFailure
	}
	return application, exitOK
}

type commonFlags struct {
	configPath string
	tenant     string
	json       bool
}

func addCommonFlags(fs *flag.FlagSet, cf *commonFlags) {
	fs.StringVar(&cf.configPath, "config", "", "path to a YAML configuration file")
	fs.StringVar(&cf.tenant, "tenant", "", "tenant that owns the messages (required)")
	fs.BoolVar(&cf.json, "json", false, "emit machine-readable JSON output")
}

// reorderPositionalsLast splits raw arguments into flag tokens and positional
// tokens so operators may place flags on either side of the message id
// (`requeue <id> --reason R`). Stdlib flag parsing alone stops at the first
// positional, silently turning trailing flags into extra positionals.
// Value-taking flags are recognized from the already-declared FlagSet; a
// value is consumed only when it does not itself look like a flag. Unknown
// dash-tokens pass through for flag.Parse to diagnose.
func reorderPositionalsLast(fs *flag.FlagSet, args []string) ([]string, []string) {
	takesValue := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if _, isBool := f.Value.(interface{ IsBoolFlag() bool }); !isBool {
			takesValue[f.Name] = true
		}
	})
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			flagArgs = append(flagArgs, args[i:]...)
			break
		}
		if len(tok) < 2 || tok[0] != '-' {
			positional = append(positional, tok)
			continue
		}
		name := strings.TrimLeft(tok, "-")
		if strings.IndexByte(name, '=') >= 0 || !takesValue[name] ||
			i+1 >= len(args) || looksLikeFlag(args[i+1]) {
			flagArgs = append(flagArgs, tok)
			continue
		}
		flagArgs = append(flagArgs, tok, args[i+1])
		i++
	}
	return flagArgs, positional
}

func looksLikeFlag(tok string) bool {
	return len(tok) > 1 && tok[0] == '-'
}

func runDeadLetterList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("outbox dead-letter list", flag.ContinueOnError)
	var cf commonFlags
	addCommonFlags(fs, &cf)
	limit := fs.Int("limit", 50, "page size [1,200]")
	after := fs.String("after", "", "cursor token from a previous page")
	flagArgs, _ := reorderPositionalsLast(fs, args)
	fs.SetOutput(stderr)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}
	application, code := buildWorld(stderr, cf.configPath)
	if application == nil {
		return code
	}
	req := ports.ListDeadLettersRequest{
		TenantID: domain.TenantID(cf.tenant),
		Limit:    *limit,
	}
	if *after != "" {
		at, id, err := decodeCursor(*after)
		if err != nil {
			printTypedError(stderr, err)
			return exitFailure
		}
		req.AfterCreatedAt, req.AfterID = at, id
	}
	return deadLetterList(application, req, cf.json, stdout, stderr)
}

func deadLetterList(application *app.App, req ports.ListDeadLettersRequest, asJSON bool, stdout, stderr io.Writer) int {
	page, err := application.OutboxOps.ListDeadLetters(context.Background(), req)
	if err != nil {
		printTypedError(stderr, err)
		return exitFailure
	}
	enc := json.NewEncoder(stdout)
	for i := range page {
		if asJSON {
			if err := enc.Encode(page[i]); err != nil {
				printTypedError(stderr, err)
				return exitFailure
			}
			continue
		}
		printSummaryLine(stdout, &page[i])
	}
	if !asJSON && len(page) > 0 {
		last := page[len(page)-1]
		fmt.Fprintf(stdout, "-- next page: --after %s\n", encodeCursor(last.CreatedAt, last.ID))
	}
	return exitOK
}

func runDeadLetterShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("outbox dead-letter show", flag.ContinueOnError)
	var cf commonFlags
	addCommonFlags(fs, &cf)
	fs.SetOutput(stderr)
	flagArgs, rest := reorderPositionalsLast(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: ants outbox dead-letter show --tenant <id> <message-id>")
		return exitUsage
	}
	application, code := buildWorld(stderr, cf.configPath)
	if application == nil {
		return code
	}
	return deadLetterShow(application, domain.TenantID(cf.tenant), rest[0], cf.json, stdout, stderr)
}

func deadLetterShow(application *app.App, tenantID domain.TenantID, messageID string, asJSON bool, stdout, stderr io.Writer) int {
	summary, err := application.OutboxOps.GetDeadLetter(context.Background(), tenantID, messageID)
	if err != nil {
		printTypedError(stderr, err)
		return exitFailure
	}
	if asJSON {
		if err := json.NewEncoder(stdout).Encode(summary); err != nil {
			printTypedError(stderr, err)
			return exitFailure
		}
		return exitOK
	}
	printDetail(stdout, summary)
	return exitOK
}

type mutateParams struct {
	tenant    domain.TenantID
	message   string
	actor     string
	reason    string
	traceID   string
	asJSON    bool
	confirmed bool
}

func runDeadLetterMutate(args []string, stdout, stderr io.Writer, action string) int {
	name := "outbox dead-letter " + action
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var cf commonFlags
	addCommonFlags(fs, &cf)
	actor := fs.String("actor", "", "operator identity recorded in events and audit (required)")
	reason := fs.String("reason", "", "why this intervention is being made")
	traceID := fs.String("trace-id", "", "optional request/trace correlation id")
	confirmed := fs.Bool("yes", false, "confirm the terminal discard (discard only, required)")
	fs.SetOutput(stderr)
	flagArgs, rest := reorderPositionalsLast(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return exitUsage
	}
	if len(rest) != 1 {
		if action == actionDiscard {
			fmt.Fprintf(stderr, "usage: ants %s --tenant <id> --actor <id> --yes <message-id>\n", name)
		} else {
			fmt.Fprintf(stderr, "usage: ants %s --tenant <id> --actor <id> <message-id>\n", name)
		}
		return exitUsage
	}
	if strings.TrimSpace(cf.tenant) == "" || strings.TrimSpace(*actor) == "" {
		fmt.Fprintln(stderr, "error: usage: --tenant and --actor are required")
		return exitUsage
	}
	application, code := buildWorld(stderr, cf.configPath)
	if application == nil {
		return code
	}
	params := mutateParams{
		tenant:    domain.TenantID(cf.tenant),
		message:   rest[0],
		actor:     *actor,
		reason:    *reason,
		traceID:   *traceID,
		asJSON:    cf.json,
		confirmed: *confirmed,
	}
	return deadLetterMutate(application, action, params, stdout, stderr)
}

// deadLetterMutate is the shared core of requeue and discard. The fencing
// credential is taken from a fresh read of the row so scripts act on what
// they just saw; any concurrent transition still surfaces as a typed
// stale-credential conflict instead of overwriting newer state.
func deadLetterMutate(application *app.App, action string, p mutateParams, stdout, stderr io.Writer) int {
	ctx := context.Background()
	req := ports.OutboxMutationRequest{
		TenantID:  p.tenant,
		MessageID: p.message,
		Actor: domain.Actor{
			Type: domain.PrincipalHuman,
			ID:   p.actor,
		},
		Reason:  p.reason,
		TraceID: p.traceID,
	}

	summary, err := application.OutboxOps.GetDeadLetter(ctx, p.tenant, p.message)
	if err != nil {
		printTypedError(stderr, err)
		return exitFailure
	}

	// Discard is terminal: without the explicit confirmation flag, print
	// exactly what would happen and refuse. No interactive prompt exists,
	// so automation can never hang on this command.
	if action == actionDiscard && !p.confirmed {
		fmt.Fprintf(stderr,
			"refusing to discard %s (generation %d, attempts %d/%d).\n"+
				"This decision is terminal and the row stays as retained history.\n"+
				"Re-run with --yes to confirm.\n",
			summary.ID, summary.Generation, summary.Attempts, summary.MaxAttempts)
		return exitUsage
	}

	// The credential comes from the row just shown; a non-dead row (only
	// discarded rows are reachable here) is rejected by the store's own
	// classification as a typed invalid transition.
	req.ExpectedGeneration = summary.Generation

	var result ports.OutboxMutationResult
	switch action {
	case actionRequeue:
		result, err = application.OutboxOps.Requeue(ctx, req)
	case actionDiscard:
		result, err = application.OutboxOps.Discard(ctx, req)
	default:
		fmt.Fprintf(stderr, "error: usage: unknown action %q\n", action)
		return exitUsage
	}
	if err != nil {
		printTypedError(stderr, err)
		return exitFailure
	}
	if p.asJSON {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			printTypedError(stderr, err)
			return exitFailure
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "%s %s (generation %d, attempts_before %d)\n",
		pastTense(action), result.MessageID, result.Generation, result.AttemptsBefore)
	return exitOK
}

// pastTense renders the stable result-line verb for each action.
func pastTense(action string) string {
	if action == actionDiscard {
		return "discarded"
	}
	return "requeued"
}

func printSummaryLine(stdout io.Writer, s *ports.DeadLetterSummary) {
	deadSince := "-"
	if s.DeadAt != nil {
		deadSince = s.DeadAt.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(stdout, "%s\tgen=%d\tattempts=%d/%d\tdead_since=%s\tcause=%.120s\n",
		s.ID, s.Generation, s.Attempts, s.MaxAttempts, deadSince, s.Cause)
}

func printDetail(stdout io.Writer, s *ports.DeadLetterSummary) {
	fmt.Fprintf(stdout, "id:            %s\n", s.ID)
	fmt.Fprintf(stdout, "dedup_key:     %s\n", s.DedupKey)
	fmt.Fprintf(stdout, "status:        %s\n", s.Status)
	fmt.Fprintf(stdout, "generation:    %d\n", s.Generation)
	fmt.Fprintf(stdout, "attempts:      %d/%d\n", s.Attempts, s.MaxAttempts)
	fmt.Fprintf(stdout, "created_at:    %s\n", s.CreatedAt.UTC().Format(time.RFC3339))
	if s.DeadAt != nil {
		fmt.Fprintf(stdout, "dead_at:       %s\n", s.DeadAt.UTC().Format(time.RFC3339))
	}
	if s.DiscardedAt != nil {
		fmt.Fprintf(stdout, "discarded_at:  %s\n", s.DiscardedAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(stdout, "cause:         %s\n", s.Cause)
}

// printTypedError renders the error taxonomy triple so scripts branch on
// stable tokens instead of prose.
func printTypedError(stderr io.Writer, err error) {
	domErr, ok := err.(*domain.Error)
	if !ok {
		fmt.Fprintf(stderr, "error: internal: internal: %v\n", err)
		return
	}
	fmt.Fprintf(stderr, "error: %s: %s: %s\n", domErr.Kind, domErr.Code, domErr.Message)
}

// cursorToken is the opaque pagination handle handed back by list.
type cursorToken struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func encodeCursor(at time.Time, id string) string {
	raw, _ := json.Marshal(cursorToken{At: at, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(token string) (time.Time, string, error) {
	var c cursorToken
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.ID == "" {
		return time.Time{}, "", domain.Invalidf("outbox_page_cursor", "pagination cursor %q is not valid", token)
	}
	return c.At, c.ID, nil
}
