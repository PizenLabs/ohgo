package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/gryhook"
)

// attachGRYEvidence installs the opt-in GRY lifecycle adapter on a headless
// run. It returns nil when the adapter is disabled, so the default path keeps
// its exact previous behaviour. The adapter is evidence-only: it cannot admit,
// approve or execute a mutation.
func attachGRYEvidence(ctx context.Context, root, taskID, text string, loop *agent.Loop, settings config.Settings) *gryhook.Session {
	cfg := gryhook.FromEnvAndSettings(root, gryFileConfig(settings))
	if !cfg.Enabled {
		return nil
	}
	runner, err := gryhook.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gry hook disabled: %v\n", err)
		return nil
	}
	session := gryhook.NewSession(runner, taskID, text)
	session.BeforePlan(ctx)
	if blocks := session.PlanEvidence(); len(blocks) > 0 {
		loop.SetSystemContext(blocks)
	}
	gryhook.Install(loop, session)
	return session
}

// finishGRYEvidence runs the final hook and reports one bounded line. It is
// deliberately non-blocking for the run outcome: OHGO's completion policy has
// already been decided from the tool evidence.
func finishGRYEvidence(session *gryhook.Session) {
	if session == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := session.BeforeFinish(ctx)
	if result.Status == "" || result.Status == "disabled" {
		return
	}
	fmt.Fprintf(
		os.Stderr,
		"gry before_finish: status=%s freshness=%s complete_for_scope=%t repository_scan=%t\n",
		result.Status, result.Freshness, result.CompleteForScope, result.RepositoryScan,
	)
	if result.Failure != nil {
		fmt.Fprintf(os.Stderr, "gry before_finish failure: %s: %s\n", result.Failure.Code, result.Failure.Message)
	}
}
