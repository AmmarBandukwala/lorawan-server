package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fieldnode/lorawan-server/api/internal/llm"
	"github.com/fieldnode/lorawan-server/api/internal/mcpclient"
)

// Ask answers one question, emitting events as it goes.
//
// transcript is the conversation so far, oldest first, and must not include a system
// turn: this builds that itself, every time, because the tool list and the retrieved
// context change between questions and a cached system turn would quietly answer from
// stale facts.
//
// emit is called for every event, and its error aborts the answer. That is how a
// browser closing its connection stops the generation rather than merely going
// unnoticed while the model burns a core on an answer nobody will read.
func (s *Service) Ask(ctx context.Context, transcript []llm.Message, emit func(Event) error) error {
	fail := func(err error) error {
		// The error goes to the operator as an event and is also returned, so the
		// handler can log it and the page can show it. Swallowing either one leaves
		// someone debugging with only half the story.
		_ = emit(Event{Type: EventError, Text: err.Error()})
		return err
	}

	// The session is per question, so a listener that restarted between two
	// questions is picked up by the next one rather than staying broken until the
	// console restarts. Releasing it on return is what stops a browser that asks
	// forty questions from leaving forty connections on the listener.
	network, release, err := s.session(ctx)
	if err != nil {
		return fail(fmt.Errorf("the assistant could not reach the MCP listener: %w", err))
	}
	defer release()

	tools, err := network.Tools(ctx)
	if err != nil {
		return fail(err)
	}
	// This filter is the whole access-control story, so it runs before anything is
	// offered to the model and before anything is said to the operator.
	offered := readOnlyTools(tools)
	if len(offered) == 0 {
		return fail(fmt.Errorf("the MCP listener offered no read-only tools, so there is " +
			"nothing the assistant is allowed to look at"))
	}

	history, trimmed := trimTranscript(transcript)
	messages := append([]llm.Message{
		{Role: llm.RoleSystem, Content: s.systemPrompt(s.kb.ContextFor(latestQuestion(history)), trimmed)},
	}, history...)

	calls := 0
	// Bytes of tool result already replayed into this question. Bounded separately
	// from the per-result cap because maxCalls is 12: twelve results each under the
	// per-result ceiling is still more than a small context model has.
	spent := 0
	for round := 0; round < maxRounds; round++ {
		if err := emit(Event{Type: EventStatus, Text: statusLine(round, calls)}); err != nil {
			return err
		}

		temperature := answerTemperature
		response, err := s.model.Stream(ctx, llm.Request{
			Model:       s.modelID,
			Messages:    messages,
			Tools:       toolSpecs(offered),
			Temperature: &temperature,
			MaxTokens:   maxAnswerTokens,
		}, func(fragment string) error {
			return emit(Event{Type: EventToken, Text: fragment})
		})
		if err != nil {
			return fail(err)
		}
		if len(response.ToolCalls) == 0 {
			return emit(doneEvent(response, s.modelID, s.endpoint))
		}

		if calls+len(response.ToolCalls) > maxCalls {
			// Stated rather than silently truncated. A model that wanted more and got
			// less will say it could not finish if it is told; a client handed a
			// partial answer with no marker will take it for the whole one.
			return fail(fmt.Errorf("the assistant stopped after %d lookups without "+
				"finishing; try asking about one device or one gateway at a time", calls))
		}

		// The assistant message carrying the calls must be replayed before the tool
		// results, or the model has no memory of asking and asks again. This is the
		// single most common way to build a tool loop that never terminates.
		messages = append(messages, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		for _, call := range response.ToolCalls {
			if err := emit(Event{
				Type:      EventToolCall,
				Tool:      call.Function.Name,
				Arguments: json.RawMessage(call.Function.Arguments),
			}); err != nil {
				return err
			}

			calls++
			result, callErr := execute(ctx, network, offered, call)
			if err := emit(Event{
				Type: EventToolResult,
				Tool: call.Function.Name,
				OK:   callErr == nil,
			}); err != nil {
				return err
			}
			if callErr != nil {
				// The failure text goes back to the model as the tool result, which is
				// what lets it correct a mistyped DevEUI or pick a different tool.
				// Reporting it to the operator instead would end the answer at the
				// first mistake, and one wrong guess in four is normal for a small
				// model.
				result = "This lookup failed: " + callErr.Error()
			}
			fitted := fitResult(result, spent)
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    fitted,
			})
			// The budget tracks what actually entered the context, not what was
			// asked for: one oversized lookup should not spend the whole allowance
			// when only the first chunk of it was replayed.
			spent += len(fitted)
		}
	}

	return fail(fmt.Errorf("the assistant ran %d rounds of lookups without reaching an "+
		"answer; try a narrower question", maxRounds))
}

// execute performs one tool call, refusing anything that was not offered.
//
// The network is passed in rather than read from the Service because it belongs to
// this question's session, and a call that outlived it would fail confusingly.
//
// The refusal is not paranoia about the model. It guards against a specific and very
// likely failure: a model that has read LoRaWAN documentation elsewhere invents a
// plausible name like "send_downlink", and passing that through would run it, because
// the listener authorises on the token rather than on the list this assistant was
// given.
func execute(ctx context.Context, network Network,
	offered []mcpclient.Tool, call llm.ToolCall) (string, error) {
	name := strings.TrimSpace(call.Function.Name)
	allowed := false
	for _, tool := range offered {
		if tool.Name == name {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("%q is not one of the tools this assistant offers; it can "+
			"only read, and asking again will not change that", name)
	}

	args := json.RawMessage(call.Function.Arguments)
	if strings.TrimSpace(string(args)) == "" {
		args = json.RawMessage(`{}`)
	}
	// A malformed argument blob is reported back to the model rather than rejected
	// outright: small models emit trailing commas and unquoted values, and the same
	// model fixes it on the next attempt far more often than an operator would believe.
	if !json.Valid(args) {
		return "", fmt.Errorf("the arguments were not valid JSON (%s); the tool needs an "+
			"object matching its schema", strings.TrimSpace(string(args)))
	}
	return network.Call(ctx, name, args)
}

// doneEvent reports which model answered and from where. Naming the endpoint is the
// point: the argument for running this locally is that telemetry stays on the network,
// and that claim is only reassuring if the page shows where the traffic went.
func doneEvent(response *llm.Response, fallbackModel, endpoint string) Event {
	model := response.Model
	if model == "" {
		model = fallbackModel
	}
	return Event{
		Type:     EventDone,
		Model:    model,
		Endpoint: endpoint,
		Tokens:   response.Usage.TotalTokens,
	}
}

// statusLine tells the operator what is happening in words they would use.
func statusLine(round, calls int) string {
	switch {
	case round == 0 && calls == 0:
		return "thinking"
	case calls == 0:
		return "still thinking"
	case calls == 1:
		return "looked up 1 thing, thinking again"
	default:
		return fmt.Sprintf("looked up %d things, thinking again", calls)
	}
}

// latestQuestion is the text retrieval runs on: the most recent user turn. A follow-up
// like "and what about the gateway?" is about the gateway, and retrieving on the first
// question of the session would fetch the wrong passage and present it as though it had
// been chosen for this one.
func latestQuestion(transcript []llm.Message) string {
	for i := len(transcript) - 1; i >= 0; i-- {
		if transcript[i].Role == llm.RoleUser && strings.TrimSpace(transcript[i].Content) != "" {
			return transcript[i].Content
		}
	}
	return ""
}

// Sampling is pinned rather than left to the runtime's default, for two reasons that
// are both routine with local runtimes. The default temperature on several is high
// enough that a small model emits tool-call JSON that does not parse, and the loop can
// only hand that failure back and hope. And with no ceiling on the completion, a model
// that begins enumerating a fleet generates until it hits the context window, so the
// answer arrives cut off mid-sentence and looks like a finished one.
//
// 1024 tokens is roughly 700 words, which is far past any answer this assistant gives;
// the point is the ceiling, not the allowance.
const (
	answerTemperature = 0.1
	maxAnswerTokens   = 1024
)

// maxToolResultBytes caps one lookup's result before it is replayed to the model, and
// maxToolResultsTotalBytes caps all of them for one question.
//
// Both are needed. Measured against the real Qwen2.5 tokenizer rather than estimated,
// the first request of a question costs 4,199 tokens before the model reads a single
// result: 1,870 for the twelve tool definitions, 1,684 for the event vocabulary, 645 for
// the guidance and framing. The vocabulary is the item to trim if this ever has to
// shrink — the same ground is covered on demand by the listener's explain_event_reason
// tool — but it stays, because a model that has never seen the reason vocabulary guesses
// at it, and a confident wrong reason is the failure this whole feature is arranged to
// avoid.
//
// Against that 4,199, a `list_devices` on a few hundred nodes is tens of thousands of
// bytes on its own, and the nsclient cap of 4 MB protects this process, not the window.
//
// The two caps together keep the worst case near 6,000 tokens of tool output, which with
// the fixed cost and a bounded completion fits a 16k context — the smallest window worth
// pointing this at.
const (
	maxToolResultBytes       = 6000
	maxToolResultsTotalBytes = 24000
)

// fitResult caps a tool result and says that it did.
//
// The marker is an instruction, not an apology. A model handed a silently shortened
// document concludes the fleet is the size of what it can see and reports that as a
// fact; a model told the rest was cut and how to ask for less narrows the query instead.
// That is the difference between truncation costing accuracy and costing a round trip.
//
// The cut lands on the last closing brace before the limit rather than on the limit
// itself. These results are JSON documents, and slicing one mid-record shows the model a
// half-written field, which reads like corrupt data from the network rather than a
// decision made here.
func fitResult(result string, spent int) string {
	const marker = "\n… the rest of this result was cut to fit the model's context, so " +
		"what follows is a prefix and not the whole answer. Ask again with a dev_eui " +
		"filter or a smaller limit for the part you actually need."

	budget := maxToolResultBytes
	if remaining := maxToolResultsTotalBytes - spent; remaining < budget {
		budget = remaining
	}
	if budget <= 0 {
		return "The lookup budget for this question is spent, so this result was not " +
			"shown. Answer from what you have already read, and say plainly what you " +
			"could not check."
	}
	if len(result) <= budget {
		return result
	}
	if cut := strings.LastIndexByte(result[:budget], '}'); cut >= budget/2 {
		return result[:cut+1] + marker
	}
	return result[:budget] + marker
}

// maxTranscript bounds the history replayed to the model. It is measured in messages
// rather than tokens because token counts are not known until after the call, and on a
// small context model an unbounded history does not degrade gracefully: it either fails
// outright or silently drops the system turn, which is the one turn holding the
// tool-use instructions together.
const maxTranscript = 20

// trimTranscript keeps the most recent turns. The second result says whether anything
// was dropped, which the prompt then states, so a model that has lost the earlier
// context knows it has lost something rather than answering as though the conversation
// began with the last message.
func trimTranscript(transcript []llm.Message) ([]llm.Message, bool) {
	if len(transcript) <= maxTranscript {
		return transcript, false
	}
	return transcript[len(transcript)-maxTranscript:], true
}
