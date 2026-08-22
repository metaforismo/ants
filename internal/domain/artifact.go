package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// ArtifactKind enumerates what a stored artifact contains. Content is
// addressable by digest so consumers can verify integrity independently of
// the store.
type ArtifactKind string

const (
	ArtifactDiff   ArtifactKind = "diff"
	ArtifactLog    ArtifactKind = "log"
	ArtifactReport ArtifactKind = "report"
)

func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactDiff, ArtifactLog, ArtifactReport:
		return true
	default:
		return false
	}
}

// RetentionClass follows the plan: execution byproducts are ephemeral; audit
// and reports are durable.
type RetentionClass string

const (
	RetentionEphemeral RetentionClass = "ephemeral"
	RetentionDurable   RetentionClass = "durable"
)

const MaxArtifactBytes = 8 << 20

func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

type Artifact struct {
	ID             ArtifactID     `json:"id"`
	TenantID       TenantID       `json:"tenant_id"`
	RunID          RunID          `json:"run_id"`
	TaskID         TaskID         `json:"task_id,omitempty"`
	Kind           ArtifactKind   `json:"kind"`
	Digest         string         `json:"digest"`
	SizeBytes      int            `json:"size_bytes"`
	Retention      RetentionClass `json:"retention"`
	ProducerTaskID TaskID         `json:"producer_task_id,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`

	// Content is populated in memory-mode stores only; the Postgres adapter
	// keeps content in object storage keyed by digest.
	Content []byte `json:"-"`
}

// NewArtifact validates size/digest invariants at creation so an artifact
// record can never exist without matching content.
func NewArtifact(id ArtifactID, tenantID TenantID, runID RunID, kind ArtifactKind, retention RetentionClass, content []byte, now time.Time) (*Artifact, error) {
	if _, err := ParseArtifactID(string(id)); err != nil {
		return nil, err
	}
	if !kind.Valid() {
		return nil, Invalidf("artifact_kind", "artifact kind %q is not supported", kind)
	}
	switch retention {
	case RetentionEphemeral, RetentionDurable:
	default:
		return nil, Invalidf("artifact_retention", "retention class %q is not supported", retention)
	}
	if len(content) == 0 {
		return nil, Invalidf("artifact_content", "artifact content must not be empty")
	}
	if len(content) > MaxArtifactBytes {
		return nil, Invalidf("artifact_size", "artifact exceeds maximum size of %d bytes", MaxArtifactBytes)
	}
	return &Artifact{
		ID:        id,
		TenantID:  tenantID,
		RunID:     runID,
		Kind:      kind,
		Digest:    Digest(content),
		SizeBytes: len(content),
		Retention: retention,
		Content:   content,
		CreatedAt: now.UTC(),
	}, nil
}
