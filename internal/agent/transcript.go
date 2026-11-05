package agent

import "github.com/PizenLabs/ohgo/internal/provider/ai"

// TranscriptTurn projects a completed turn into what a host may show and
// persist as the conversation record.
//
// A call refused with AnswerRequired was an observation proposed after the
// runtime had already closed the action surface and asked for an answer. It
// was not executed and produced no evidence. Keeping it in the transcript
// turns a control decision into a red tool failure, often after a finished
// answer. Removing its outcome and its call block keeps the displayed record
// truthful and keeps a resumed conversation valid, because every remaining
// tool call still has a result.
//
// Policy-sensitive refusals (mutation, execution, delegation, external calls)
// do not carry AnswerRequired even on a closed surface: they are attempted
// actions the user may need to see. Grounding, authorization, and policy
// refusals are preserved for the same reason. The live model conversation is
// not changed by this projection; the loop still keeps the refusal in its own
// context for the rest of the run.
func TranscriptTurn(turn Turn) Turn {
	if turn.Err != nil || len(turn.Results) == 0 {
		return turn
	}
	refused := make(map[string]bool)
	for _, outcome := range turn.Results {
		if outcome.Result.AnswerRequired {
			refused[outcome.CallID] = true
		}
	}
	if len(refused) == 0 {
		return turn
	}

	results := make([]ToolOutcome, 0, len(turn.Results)-len(refused))
	for _, outcome := range turn.Results {
		if refused[outcome.CallID] {
			continue
		}
		results = append(results, outcome)
	}
	turn.Results = results

	content := make([]ai.ContentBlock, 0, len(turn.Assistant.Content))
	calls := 0
	for _, block := range turn.Assistant.Content {
		if call, ok := block.(ai.ToolCall); ok {
			if refused[call.ID] {
				continue
			}
			calls++
		}
		content = append(content, block)
	}
	turn.Assistant.Content = content
	if calls == 0 && turn.Assistant.StopReason == ai.StopToolUse {
		turn.Assistant.StopReason = ai.StopEndTurn
	}
	return turn
}
