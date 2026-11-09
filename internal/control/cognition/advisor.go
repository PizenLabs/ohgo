package cognition

import (
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// ExperienceAdvisor is the optional seam through which promoted execution
// experience may influence a projection.
//
// It is deliberately narrow. An advisor sees the task shape, evidence,
// execution summary, sufficiency verdict, the already-constructed tool surface,
// and the completed turns. It may return tool names to move earlier and bounded
// advisory hints. It cannot add a tool, remove a tool, authorize a call, change
// evidence, alter sufficiency, or decide completion.
type ExperienceAdvisor interface {
	Advise(AdviceRequest) Advice
}

// AdviceRequest is the read-only state an advisor may consult.
type AdviceRequest struct {
	Shape       TaskShape
	Evidence    *EvidenceState
	Execution   ExecutionState
	Sufficiency Sufficiency
	Tools       []agent.Tool
	Turns       []agent.Turn
}

// Advice is an advisory recommendation from promoted experience.
type Advice struct {
	// PreferredTools names tools already in the request's offered set. The
	// projection may only reorder them; a name that is not offered is ignored.
	PreferredTools []string
	// Hints are bounded advisory lines for the runtime state block.
	Hints []string
	// Matched names the promoted records that produced this advice, for audit.
	Matched []string
}

// Empty reports whether the advice has no effect.
func (a Advice) Empty() bool {
	return len(a.PreferredTools) == 0 && len(a.Hints) == 0
}

// adviceFor consults the optional advisor and bounds its output.
func adviceFor(advisor ExperienceAdvisor, shape TaskShape, evidence *EvidenceState, execution ExecutionState, suff Sufficiency, tools []agent.Tool, turns []agent.Turn) (advice Advice) {
	if advisor == nil {
		return Advice{}
	}
	// Experience is an optimization, never a prerequisite for correctness. A
	// faulty advisor degrades to cold start rather than aborting the turn.
	defer func() {
		if recover() != nil {
			advice = Advice{}
		}
	}()
	advice = advisor.Advise(AdviceRequest{
		Shape:       shape,
		Evidence:    evidence,
		Execution:   execution,
		Sufficiency: suff,
		Tools:       tools,
		Turns:       turns,
	})
	return boundAdvice(advice)
}

// boundAdvice keeps an advisor from crowding the state block or repeating a
// preferred tool. It never widens the effect vocabulary.
func boundAdvice(advice Advice) Advice {
	out := Advice{}
	seen := make(map[string]bool, len(advice.PreferredTools))
	for _, tool := range advice.PreferredTools {
		tool = strings.TrimSpace(tool)
		if tool == "" || seen[tool] {
			continue
		}
		seen[tool] = true
		out.PreferredTools = append(out.PreferredTools, tool)
		if len(out.PreferredTools) >= 8 {
			break
		}
	}
	for _, hint := range advice.Hints {
		hint = strings.TrimSpace(hint)
		if hint == "" {
			continue
		}
		out.Hints = append(out.Hints, bound(hint, 240))
		if len(out.Hints) >= 3 {
			break
		}
	}
	for _, id := range advice.Matched {
		id = strings.TrimSpace(id)
		if id != "" {
			out.Matched = append(out.Matched, id)
		}
		if len(out.Matched) >= 8 {
			break
		}
	}
	return out
}

// orderCandidateTools moves preferred names to the front while preserving the
// original order of everything else. It never adds a tool: the input slice is
// the complete, already-authorized surface.
func orderCandidateTools(tools []agent.Tool, preferred []string) []agent.Tool {
	if len(tools) == 0 || len(preferred) == 0 {
		return tools
	}
	rank := make(map[string]int, len(preferred))
	for i, name := range preferred {
		if _, ok := rank[name]; !ok {
			rank[name] = i
		}
	}
	out := append([]agent.Tool(nil), tools...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, iok := rank[out[i].Name()]
		rj, jok := rank[out[j].Name()]
		switch {
		case iok && jok:
			return ri < rj
		case iok:
			return true
		case jok:
			return false
		default:
			return false
		}
	})
	return out
}
