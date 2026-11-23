package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// wireResponse is the endpoint's response envelope. It is separate from Response
// because the wire shape is a list of choices and this service always asks for one;
// keeping them apart makes the flattening visible in one place rather than implied by
// a struct tag.
type wireResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

type wireMessage struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

// completionsPath is appended to the base URL. It is a constant rather than a
// configurable path because the OpenAI shape names it and every runtime claiming
// compatibility names it the same; a variable here would just be another way to
// misconfigure the deployment.
const completionsPath = "/chat/completions"

// maxResponseBytes bounds a non-streaming completion. A completion is bounded by
// max_tokens in the request, so reaching this means the endpoint ignored it.
const maxResponseBytes = 2 << 20

// post sends one request and returns the untouched response. The caller decides
// whether to read it whole or stream it, which is why this does not.
//
// The returned cancel belongs to the total budget and must be called once the body
// has been fully consumed — for a stream, after the last event, not after this call
// returns. Cancelling early is how a generation gets cut off at the first chunk while
// the caller believes it timed out.
func (c *Client) post(ctx context.Context, body any, stream bool) (*http.Response, context.CancelFunc, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("llm: encode request: %w", err)
	}

	// The total budget is a context rather than a client timeout so it can exceed
	// the connect timeout without affecting it.
	reqCtx, cancel := context.WithTimeout(ctx, c.total)
	request, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.base+completionsPath,
		bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("llm: build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	// Both accepts, always. A streaming request genuinely wants events; a
	// non-streaming one sends the same header because several runtimes route on it,
	// and answering a plain JSON request with a single-chunk stream is legal.
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		cancel()
		// The address is worth carrying: "cannot reach the model" is the one thing
		// the operator cannot derive from the answer, and whether this points at
		// ollama:11434 or at api.openai.com is the question they are really asking.
		return nil, nil, fmt.Errorf("llm: cannot reach the model endpoint at %s: %w",
			c.base, unwrapURLError(err))
	}
	if response.StatusCode >= 300 {
		defer response.Body.Close()
		cancel()
		return nil, nil, statusError(response)
	}
	return response, cancel, nil
}

// Complete performs one non-streaming completion. The assistant streams, so this
// exists for the calls that should not — and so the streaming path has a
// non-streaming sibling to be checked against when the two disagree.
func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	req.Stream = false

	response, cancel, err := c.post(ctx, req, false)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("llm: read response: %w", err)
	}
	if len(raw) >= maxResponseBytes {
		return nil, errors.New("llm: the endpoint returned a response larger than this " +
			"server will hold; reduce max_tokens or shorten the transcript")
	}

	var wire wireResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("llm: the endpoint returned something that is not a chat "+
			"completion: %w", err)
	}
	if len(wire.Choices) == 0 {
		// Success with no choices is a proxy or a cache answering, not the model.
		// Saying so beats returning an empty completion, which reads as "the model
		// had nothing to say".
		return nil, errors.New("llm: the endpoint reported success with no choices; a " +
			"proxy or cache may be answering instead of the model")
	}
	choice := wire.Choices[0]
	return &Response{
		Model:        wire.Model,
		Content:      choice.Message.Content,
		ToolCalls:    choice.Message.ToolCalls,
		FinishReason: choice.FinishReason,
		Usage:        wire.Usage,
	}, nil
}
