package llm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stream performs a streaming completion and returns the assembled result.
//
// onContent is called for each text fragment as it arrives, and may return an error
// to abort the generation: that is how a handler tells the model the browser hung up,
// rather than discovering it on the next write after having already spent the compute.
// It may be nil for a caller that only wants the finished answer.
//
// Tool calls are not reported incrementally. A call is only useful once its argument
// JSON is whole, and handing a caller a half-parsed `{"dev_eui":"0011` is a temptation
// to act on it. They come back on the returned Response.
func (c *Client) Stream(ctx context.Context, req Request, onContent func(string) error) (*Response, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	req.Stream = true
	// Asking explicitly is what makes usage arrive on the last chunk. Omitting it and
	// then reporting zero tokens would be a quiet lie about cost.
	req.StreamOptions = &streamOptions{IncludeUsage: true}

	// streamCtx is what the request actually runs on, and the first-token watchdog
	// below cancels it. Deriving the watchdog from a separate context — the obvious
	// mistake here — cancels nothing: the in-flight read is bound to the request's
	// context, so a budget that cannot cancel that read is a budget that reports a
	// failure five seconds late via the total instead of on time.
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()

	response, cancel, err := c.post(streamCtx, req, true)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer response.Body.Close()

	if ctype := response.Header.Get("Content-Type"); ctype != "" &&
		!strings.Contains(ctype, "text/event-stream") {
		// A proxy, or a runtime that ignored "stream": true, answered with a plain
		// body. Reading that as SSE would produce an empty answer and a confusing
		// error, so say what actually happened.
		return nil, fmt.Errorf("llm: the endpoint answered a streaming request with %s "+
			"rather than an event stream; it may not support streaming", ctype)
	}

	// The watchdog runs on its own goroutine because the read blocks and nothing else
	// can notice a stream that opened, said nothing, and is not going to say anything.
	var stalled atomic.Bool
	var once sync.Once
	first := make(chan struct{})
	go func() {
		timer := time.NewTimer(c.first)
		defer timer.Stop()
		select {
		case <-first:
			// Content arrived; the budget has done its job.
		case <-streamCtx.Done():
			// Finished, or the caller went away. Either way there is nothing to cut.
		case <-timer.C:
			// Stated before cancelling, so the reader can tell "the model never
			// answered" from "the caller hung up" once the read fails.
			stalled.Store(true)
			stopStream()
		}
	}()
	signalFirst := func() { once.Do(func() { close(first) }) }

	return c.readStream(streamCtx, response.Body, onContent, signalFirst, &stalled, c.first)
}

// streamOptions is the request field that asks for usage on the final chunk.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// readStream consumes the event stream, assembling content and tool calls.
func (c *Client) readStream(ctx context.Context, body io.Reader, onContent func(string) error,
	signalFirst func(), stalled *atomic.Bool, budget time.Duration) (*Response, error) {

	result := &Response{}
	assembler := newToolAssembler()
	scanner := bufio.NewScanner(body)
	// A single SSE line can hold a whole tool-call argument blob, which for a long
	// downlink payload exceeds the 64 KiB default. This is a generous ceiling rather
	// than a limit on the answer: max_tokens still bounds the generation.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var data strings.Builder
	for scanner.Scan() {
		switch line := scanner.Text(); {
		case line == "":
			// A blank line closes the event. The budget is disarmed only by an event
			// that carried something the model produced, never by a line arriving: a
			// proxy keep-alive must not make a wedged model look healthy.
			produced, err := consumeEvent(data.String(), result, assembler, onContent)
			data.Reset()
			if produced {
				signalFirst()
			}
			if err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, ":"):
			// A comment. Runtimes and proxies emit them as keep-alives; ignoring
			// them is correct and logging them would be noise.
		case strings.HasPrefix(line, "data:"):
			// The spec allows one space after the colon; trimming twice handles both
			// the spaced and unspaced form without a regex.
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// event:, id:, retry: — none of which this protocol uses meaningfully.
			// Ignored rather than treated as an error, because proxies add them.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, streamFailure(ctx, err, stalled, budget)
	}

	// A trailing event with no blank line after it is legal at end of stream, and
	// dropping it would lose the last fragment.
	if data.Len() > 0 {
		if _, err := consumeEvent(data.String(), result, assembler, onContent); err != nil {
			return nil, err
		}
	}

	result.ToolCalls = assembler.ordered()
	return result, nil
}

// streamFailure names the three reasons a stream stops early, which have three
// different remedies.
func streamFailure(ctx context.Context, err error, stalled *atomic.Bool, budget time.Duration) error {
	// Checked before the caller's context on purpose: the watchdog cancels that
	// context too, so ctx.Err() is set in both cases and asking it first would report
	// a silent model as an operator who closed the tab.
	if stalled.Load() {
		return fmt.Errorf("llm: the model accepted the request and then sent nothing "+
			"for %s; it may still be loading into memory, or it may be wedged", budget)
	}
	// "The caller hung up" is routine — an operator closed the tab — and reporting it
	// as a fault would fill the log with things that are not wrong.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("llm: generation cancelled: %w", ctxErr)
	}
	return fmt.Errorf("llm: the stream ended early: %w", err)
}
