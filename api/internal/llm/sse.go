package llm

import (
	"encoding/json"
	"strings"
)

// wireChunk is one streamed delta. It differs from the non-streaming response in
// exactly the way that matters: content arrives as a fragment and tool calls arrive as
// partials keyed by index, so a chunk cannot be treated as a small Response.
type wireChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Role      string          `json:"role"`
			Content   string          `json:"content"`
			ToolCalls []wireToolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	// Usage rides on the final chunk and only if it was asked for. Several runtimes
	// omit it entirely, which is why the caller treats it as advisory.
	Usage Usage `json:"usage"`
	// Done is Ollama's own end-of-stream marker, which it sets alongside the standard
	// [DONE] sentinel rather than instead of it.
	Done bool `json:"done"`
}

// wireToolDelta is a partial tool call. Every field is optional and they arrive across
// chunks: the id, type and name on the first, then argument fragments on the rest.
type wireToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// consumeEvent decodes one SSE event and applies it, reporting whether the event
// carried anything the model actually produced.
//
// That second value is what arms and disarms the first-token budget, and the
// distinction is the reason it exists: a proxy keep-alive or a role-only opening frame
// is traffic, not an answer. Treating any byte as a first token disarms the budget on
// exactly the wedged runtime it is meant to catch.
//
// An unrecognised payload is not automatically an error: the terminal [DONE] sentinel
// is not JSON, and runtimes add fields this package has no field for.
func consumeEvent(raw string, result *Response, assembler *toolAssembler, onContent func(string) error) (bool, error) {
	payload := strings.TrimSpace(raw)
	if payload == "" || payload == "[DONE]" {
		return false, nil
	}

	var chunk wireChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		// Skipping an unparseable event is the right default for a stream: one
		// malformed keep-alive from a proxy should not discard an answer the model has
		// already spent compute producing.
		return false, nil
	}
	if chunk.Model != "" {
		result.Model = chunk.Model
	}
	// Read outside the choice loop: the usage chunk usually carries no choices at all.
	if chunk.Usage.TotalTokens > 0 {
		result.Usage = chunk.Usage
	}
	if len(chunk.Choices) == 0 {
		return false, nil
	}

	choice := chunk.Choices[0]
	if choice.FinishReason != "" {
		result.FinishReason = choice.FinishReason
	}

	produced := false
	if text := choice.Delta.Content; text != "" {
		produced = true
		if onContent != nil {
			// A non-nil error here means the consumer has gone away. Propagating it
			// stops the read, which closes the connection, which tells the runtime to
			// stop generating rather than finish an answer nobody will read.
			if err := onContent(text); err != nil {
				return produced, err
			}
		}
		result.Content += text
	}
	for _, delta := range choice.Delta.ToolCalls {
		// A tool-call delta is the model answering even though no prose has arrived.
		produced = true
		assembler.add(delta)
	}
	return produced, nil
}
