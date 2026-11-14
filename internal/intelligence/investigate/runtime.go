package investigate

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
	rt "github.com/PizenLabs/ohgo/internal/intelligence/runtime"
)

// RuntimeProvider adapts an already-authorized debug session.
//
// It never launches an adapter: the session was established through the
// runtime manager's authorization seam, and this provider only observes a
// stopped debuggee. Runtime evidence is session-bound, so it is never cached as
// source truth.
type RuntimeProvider struct {
	Session *rt.Session
	Root    string
}

// Name implements Provider.
func (p *RuntimeProvider) Name() planner.Provider { return planner.ProviderRuntime }

// Available implements Provider. A running debuggee cannot answer a stack
// question; planning it would spend a step to learn the session is not stopped.
func (p *RuntimeProvider) Available(context.Context) bool {
	return p != nil && p.Session != nil && p.Session.Alive() && p.Session.State() == rt.StateStopped
}

// Observe implements Provider.
func (p *RuntimeProvider) Observe(ctx context.Context, req Request, _ planner.Step, meter *Meter) (Observation, error) {
	observation := Observation{State: evidence.Observed, Confidence: 0.6, Coverage: 0.4}
	if p.Session == nil {
		return Observation{}, fmt.Errorf("no debug session")
	}
	if state := p.Session.State(); state != rt.StateStopped {
		observation.State = evidence.Unknown
		observation.Confidence = 0
		observation.Coverage = 0
		observation.Issues = append(observation.Issues,
			fmt.Sprintf("runtime session is %s; frame evidence requires STOPPED, and no absence is inferred", state))
		return observation, nil
	}
	if err := meter.ChargeDAP(); err != nil {
		observation.Issues = append(observation.Issues, err.Error())
		return observation, nil
	}
	threads, err := p.Session.Threads(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("runtime threads: %w", err)
	}
	if len(threads) > 4 {
		threads = threads[:4]
	}
	snapshot := "dap:" + p.Session.ID
	frames := 0
	for _, thread := range threads {
		if frames >= 24 {
			break
		}
		if err := meter.ChargeDAP(); err != nil {
			observation.Issues = append(observation.Issues, err.Error())
			break
		}
		stack, err := p.Session.StackTrace(ctx, thread.ID, 20)
		if err != nil {
			observation.Issues = append(observation.Issues,
				fmt.Sprintf("runtime stack for thread %d: %v; no frames were inferred", thread.ID, err))
			continue
		}
		for _, frame := range stack {
			if frames >= 24 {
				break
			}
			frames++
			path := ""
			if frame.Source != nil {
				path = frame.Source.Path
			}
			observation.Records = append(observation.Records, evidence.Record{
				Kind: evidence.KindRuntime, Provider: evidence.ProviderRuntime,
				Source: evidence.SourceDAP, Subject: "frame:" + frame.Name,
				Relation: "observed_at", Object: fmt.Sprintf("%s:%d", path, frame.Line),
				State: evidence.Observed, Confidence: 0.8, Freshness: evidence.FreshnessCurrent,
				Scope:      evidence.Scope{Workspace: p.Root, Path: path},
				Refs:       []evidence.Ref{{Path: path, Line: frame.Line, Column: frame.Column}},
				SnapshotID: snapshot,
				Note:       thread.Name,
			})
			observation.Facts = append(observation.Facts, Fact{Provider: string(p.Name()),
				Subject: frame.Name, Detail: fmt.Sprintf("%s:%d (thread %s)", path, frame.Line, thread.Name)})
			if frames == 1 {
				p.variables(ctx, &observation, frame, meter, snapshot)
			}
		}
	}
	observation.Summary = fmt.Sprintf("%d threads, %d frames observed in session %s", len(threads), frames, p.Session.ID)
	if frames == 0 {
		observation.State = evidence.Unknown
		observation.Confidence = 0.2
		observation.Issues = append(observation.Issues,
			"runtime: no stack frames were returned; this is not proof that nothing executed")
	}
	return observation, nil
}

func (p *RuntimeProvider) variables(ctx context.Context, observation *Observation, frame rt.Frame,
	meter *Meter, snapshot string) {
	if err := meter.ChargeDAP(); err != nil {
		observation.Issues = append(observation.Issues, err.Error())
		return
	}
	scopes, err := p.Session.Scopes(ctx, frame.ID)
	if err != nil {
		observation.Issues = append(observation.Issues, "runtime scopes: "+err.Error())
		return
	}
	for _, scope := range scopes {
		if scope.VariablesReference == 0 || meter.ChargeDAP() != nil {
			continue
		}
		variables, err := p.Session.Variables(ctx, scope.VariablesReference)
		if err != nil {
			observation.Issues = append(observation.Issues, "runtime variables: "+err.Error())
			return
		}
		if len(variables) > 10 {
			variables = variables[:10]
		}
		for _, variable := range variables {
			observation.Records = append(observation.Records, evidence.Record{
				Kind: evidence.KindRuntime, Provider: evidence.ProviderRuntime,
				Source: evidence.SourceDAP, Subject: "variable:" + variable.Name,
				Relation: "value_at_stop", Object: capText(variable.Value, 120),
				State: evidence.Observed, Confidence: 0.75, Freshness: evidence.FreshnessCurrent,
				Scope:      evidence.Scope{Workspace: p.Root, Path: scope.Name},
				SnapshotID: snapshot,
				Note:       strings.TrimSpace(variable.Type),
			})
		}
		return
	}
}
