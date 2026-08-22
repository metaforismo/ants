package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/metaforismo/ants/internal/app"
	"github.com/metaforismo/ants/internal/config"
	"github.com/metaforismo/ants/internal/domain"
	"github.com/metaforismo/ants/internal/fixtures"
	"github.com/metaforismo/ants/internal/orchestration"
)

// demoWorld is the seeded environment one demo execution runs against.
type demoWorld struct {
	application *app.App
	tenant      *domain.Tenant
	project     *domain.Project
	thread      *domain.Thread
}

const (
	demoPrincipal = domain.PrincipalID("prn_demoprincipal00000000")
	demoSlug      = "demo"
	demoProject   = "calc"
	demoKey       = "cli-demo-run"
)

func appBuildForDemo(cfg config.Config, stderr io.Writer) (*demoWorld, error) {
	cfg.Store.Mode = config.StoreModeMemory
	application, err := app.Build(cfg, stderr)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()

	idStr, err := domain.NewID(domain.PrefixTenant)
	if err != nil {
		return nil, err
	}
	tenant, terr := domain.NewTenant(domain.TenantID(idStr), demoSlug, "Demo Tenant", domain.PlanFree, "local", application.Clock.Now())
	if terr != nil {
		return nil, terr
	}
	if cerr := application.Repos.Tenants.Create(ctx, tenant); cerr != nil {
		return nil, cerr
	}

	prjID, perr := domain.NewID(domain.PrefixProject)
	if perr != nil {
		return nil, perr
	}
	project, pperr := domain.NewProject(domain.ProjectID(prjID), tenant.ID, demoProject, "Calculator", "main", fixtures.DemoName, application.Clock.Now())
	if pperr != nil {
		return nil, pperr
	}
	if cerr := application.Repos.Projects.Create(ctx, project); cerr != nil {
		return nil, cerr
	}

	thrID, thErr := domain.NewID(domain.PrefixThread)
	if thErr != nil {
		return nil, thErr
	}
	thread, therr := domain.NewThread(domain.ThreadID(thrID), tenant.ID, project.ID,
		"implement arithmetic helpers", demoPrincipal, application.Clock.Now())
	if therr != nil {
		return nil, therr
	}
	if cerr := application.Repos.Threads.Create(ctx, thread); cerr != nil {
		return nil, cerr
	}
	msgID, mErr := domain.NewID(domain.PrefixMessage)
	if mErr != nil {
		return nil, mErr
	}
	message := &domain.Message{
		ID:           domain.MessageID(msgID),
		TenantID:     tenant.ID,
		ThreadID:     thread.ID,
		Role:         domain.RoleUser,
		DeliveryMode: domain.DeliveryImmediate,
		Content:      "implement add and multiply for calc.sh",
	}
	if aerr := application.Repos.Threads.AppendMessage(ctx, message); aerr != nil {
		return nil, aerr
	}

	return &demoWorld{application: application, tenant: tenant, project: project, thread: thread}, nil
}

// execute runs the pipeline while streaming events to notify as they land.
func (d *demoWorld) execute(ctx context.Context, notify func(string)) (domain.RunID, error) {
	result, err := d.application.Engine.StartRun(ctx, orchestrationStartInput(d))
	if err != nil {
		return "", err
	}

	streamStop := make(chan struct{})
	var streamDone chan struct{}
	cursor := int64(0)
	go func() {
		ticker := time.NewTicker(15 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-streamStop:
				return
			case <-ticker.C:
				events, listErr := d.application.Repos.Events.ListByRun(ctx, d.tenant.ID, result.Run.ID, cursor, 0)
				if listErr != nil {
					continue
				}
				for _, evt := range events {
					notify(renderEvent(evt))
					cursor = evt.Seq
				}
			}
		}
	}()

	execErr := d.application.Engine.Execute(ctx, d.tenant.ID, result.Run.ID)
	close(streamStop)
	if streamDone != nil {
		<-streamDone
	}
	// Drain any events that landed after the last tick.
	events, _ := d.application.Repos.Events.ListByRun(ctx, d.tenant.ID, result.Run.ID, cursor, 0)
	for _, evt := range events {
		notify(renderEvent(evt))
	}
	return result.Run.ID, execErr
}

func orchestrationStartInput(d *demoWorld) orchestration.StartInput {
	return orchestration.StartInput{
		TenantID:       d.tenant.ID,
		ThreadID:       d.thread.ID,
		Principal:      demoPrincipal,
		Actor:          domain.Actor{Type: domain.PrincipalHuman, ID: string(demoPrincipal)},
		IdempotencyKey: demoKey,
	}
}

func renderEvent(evt *domain.Event) string {
	line := fmt.Sprintf("[%s] %s %s", evt.OccurredAt.Format("15:04:05.000"), evt.Type, evt.AggregateType)
	if to, ok := evt.Data["to"].(string); ok {
		line += " -> " + to
	}
	if branch, ok := evt.Data["branch"].(string); ok && branch != "" {
		line += " (" + branch
		if sha, ok := evt.Data["sha"].(string); ok && sha != "" {
			line += " @ " + short(sha)
		}
		line += ")"
	}
	return line
}
