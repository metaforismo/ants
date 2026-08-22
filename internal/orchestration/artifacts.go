package orchestration

import (
	"context"
	"fmt"

	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/sandbox"
)

// storeDiffArtifact persists an integration diff and returns its report ref.
func (e *Engine) storeDiffArtifact(ctx context.Context, st *runState, diff []byte) (domain.ReportArtifactRef, error) {
	content := fmt.Sprintf("diff base=%s head=%s\n", st.baseSHA, st.integrated.SHA) + string(diff)
	return e.storeArtifact(ctx, st, nil, domain.ArtifactDiff, domain.RetentionEphemeral, []byte(content))
}

func (e *Engine) storeLogArtifact(ctx context.Context, st *runState, producer *domain.Task, criterion string, result sandbox.ExecResult) (domain.ReportArtifactRef, error) {
	header := fmt.Sprintf("command: %s\nexit_code=%d duration=%s\n", criterion, result.ExitCode, result.Duration)
	content := append([]byte(header), append(append([]byte{}, result.Stdout...), result.Stderr...)...)
	return e.storeArtifact(ctx, st, producer, domain.ArtifactLog, domain.RetentionEphemeral, content)
}

func (e *Engine) storeReportArtifact(ctx context.Context, st *runState, report *domain.RunReport) (domain.ReportArtifactRef, error) {
	if report == nil {
		report = st.buildReport(false)
	}
	return e.storeArtifact(ctx, st, nil, domain.ArtifactReport, domain.RetentionDurable, jsonBytes(report))
}

// storeArtifact persists content-addressed artifact metadata plus content and
// emits the storage event.
func (e *Engine) storeArtifact(ctx context.Context, st *runState, producer *domain.Task, kind domain.ArtifactKind, retention domain.RetentionClass, content []byte) (domain.ReportArtifactRef, error) {
	idStr, err := e.newID(domain.PrefixArtifact)
	if err != nil {
		return domain.ReportArtifactRef{}, err
	}
	artifact, err := domain.NewArtifact(domain.ArtifactID(idStr), st.run.TenantID, st.run.ID, kind, retention, content, e.deps.Clock.Now())
	if err != nil {
		return domain.ReportArtifactRef{}, err
	}
	if err := e.deps.Artifacts.Create(ctx, artifact); err != nil {
		return domain.ReportArtifactRef{}, err
	}
	ref := domain.ReportArtifactRef{ID: artifact.ID, Kind: artifact.Kind, Digest: artifact.Digest}
	if err := e.emitEvent(ctx, evtFromRun(st.run, domain.EventArtifactStored, map[string]any{
		"artifact_id": string(artifact.ID),
		"kind":        string(kind),
		"digest":      artifact.Digest,
	})); err != nil {
		return ref, err
	}
	return ref, nil
}
