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
// Conversational reports whether a turn contributes an assistant message to
// the next provider request.
//
// A turn that produced no visible text and no tool call is a run observation:
// the transcript may still show its reasoning and completion still evaluates
// that the model answered nothing, but the assistant message has no sendable
// form. Hosts rebuild their next request from turns, so replaying it would put
// an assistant message with nothing to send on the wire.
func (t Turn) Conversational() bool {
	return ai.HasSendableContent(t.Assistant)
}

// ConversationMessages projects completed turns into the messages a provider
// may receive next.
//
// The projection is the one place the rule lives: a turn whose assistant
// message has no sendable content is a run observation, not conversation, and
// is never replayed. Tool results are built from the canonical text-block
// helper so an empty output cannot become a text part without text.
func ConversationMessages(turns []Turn) []Message {
	var out []Message
	for _, rawTurn := range turns {
		turn := TranscriptTurn(rawTurn)
		if !turn.Conversational() {
			continue
		}
		out = append(out, AssistantMessage{Message: turn.Assistant})
		for _, outcome := range turn.Results {
			out = append(out, ToolResultMessage{
				ToolCallID: outcome.CallID,
				ToolName:   outcome.ToolName,
				Content:    ai.TextBlocks(outcome.Result.Text),
				IsError:    outcome.Result.IsError,
			})
		}
	}
	return out
}

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
