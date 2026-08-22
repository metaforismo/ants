package domain

import (
	"time"
)

// BudgetScope follows the plan hierarchy: tenant -> project -> thread/run ->
// task. Tranche 1 models the run and task levels, which the orchestrator
// enforces directly; tenant/project rollups arrive with metering.
type BudgetScope string

const (
	BudgetScopeRun  BudgetScope = "run"
	BudgetScopeTask BudgetScope = "task"
)

// BudgetLimits are hard caps. Zero means "unset", not "unlimited": unlimited
// must be requested explicitly via the dedicated flag so a missing config
// value can never silently authorize unbounded work.
type BudgetLimits struct {
	MaxTasks        int           `json:"max_tasks"`
	MaxExecOps      int           `json:"max_exec_ops"`
	MaxWallDuration time.Duration `json:"max_wall_duration"`
	Unlimited       bool          `json:"unlimited"`
}

func (l BudgetLimits) Validate() error {
	if l.Unlimited {
		if l.MaxTasks != 0 || l.MaxExecOps != 0 || l.MaxWallDuration != 0 {
			return Invalidf("budget_limits", "unlimited budget must not also declare caps")
		}
		return nil
	}
	if l.MaxTasks < 0 || l.MaxExecOps < 0 || l.MaxWallDuration < 0 {
		return Invalidf("budget_limits", "budget caps must not be negative")
	}
	return nil
}

type Budget struct {
	ID        BudgetID     `json:"id"`
	TenantID  TenantID     `json:"tenant_id"`
	RunID     RunID        `json:"run_id,omitempty"`
	Scope     BudgetScope  `json:"scope"`
	Limits    BudgetLimits `json:"limits"`
	CreatedAt time.Time    `json:"created_at"`
}

func NewBudget(id BudgetID, tenantID TenantID, runID RunID, scope BudgetScope, limits BudgetLimits, now time.Time) (*Budget, error) {
	if _, err := ParseBudgetID(string(id)); err != nil {
		return nil, err
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	switch scope {
	case BudgetScopeRun:
	case BudgetScopeTask:
	default:
		return nil, Invalidf("budget_scope", "budget scope %q is not supported", scope)
	}
	return &Budget{
		ID:        id,
		TenantID:  tenantID,
		RunID:     runID,
		Scope:     scope,
		Limits:    limits,
		CreatedAt: now.UTC(),
	}, nil
}

// BudgetLedger tracks consumption against a Budget and enforces its caps.
// It is a pure value type: the orchestrator persists snapshots, so replay is
// deterministic.
type BudgetLedger struct {
	BudgetID    BudgetID     `json:"budget_id"`
	Limits      BudgetLimits `json:"limits"`
	TasksUsed   int          `json:"tasks_used"`
	ExecOpsUsed int          `json:"exec_ops_used"`
}

func NewLedger(b *Budget) *BudgetLedger {
	return &BudgetLedger{BudgetID: b.ID, Limits: b.Limits}
}

func (l *BudgetLedger) ReserveTask() error {
	if l.Limits.Unlimited {
		l.TasksUsed++
		return nil
	}
	if l.TasksUsed+1 > l.Limits.MaxTasks {
		return &Error{
			Kind:    ErrKindBudgetExhausted,
			Code:    "budget_task_cap",
			Message: "run task budget exhausted",
			Details: map[string]any{"used": l.TasksUsed, "cap": l.Limits.MaxTasks},
		}
	}
	l.TasksUsed++
	return nil
}

func (l *BudgetLedger) RecordExecOp() error {
	if l.Limits.Unlimited {
		l.ExecOpsUsed++
		return nil
	}
	if l.ExecOpsUsed+1 > l.Limits.MaxExecOps {
		return &Error{
			Kind:    ErrKindBudgetExhausted,
			Code:    "budget_exec_cap",
			Message: "run exec-op budget exhausted",
			Details: map[string]any{"used": l.ExecOpsUsed, "cap": l.Limits.MaxExecOps},
		}
	}
	l.ExecOpsUsed++
	return nil
}
