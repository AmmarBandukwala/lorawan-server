package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fieldnode/lorawan-server/api/internal/llm"
)

// The context budget. These are the tests for the two guards that make a small local
// model usable at all: a pinned sampling configuration, and a ceiling on how much tool
// output gets replayed into a window that is roughly a third spent before the model has
// read anything.

// toolMessages returns the tool results replayed to the model in one request.
func toolMessages(req llm.Request) []llm.Message {
	var out []llm.Message
	for _, m := range req.Messages {
		if m.Role == llm.RoleTool {
			out = append(out, m)
		}
	}
	return out
}

// cutMarkerProbe is the marker fitResult appends. Naming it here lets a test assert that
// the replayed length is the cap plus the explanation and nothing else.
const cutMarkerProbe = "\n… the rest of this result was cut to fit the model's context, so " +
	"what follows is a prefix and not the whole answer. Ask again with a dev_eui " +
	"filter or a smaller limit for the part you actually need."

// bigResult builds a plausible JSON array of n records, each roughly the size of a real
// uplink row, so the caps are exercised against shapes the listener actually returns
// rather than against an undifferentiated blob.
//
// The payload length varies with the index on purpose. Uniform records happen to divide
// the cap evenly, so a naive byte cut lands on a record boundary by coincidence and a
// test asserting "the cut did not split a record" passes whether or not the code tries.
func bigResult(records int) string {
	var b strings.Builder
	b.WriteString(`{"uplinks":[`)
	for i := range records {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"uplink_id":%d,"dev_eui":"0011223344556677","f_port":10,`+
			`"payload":"%s","rssi":-97.5,"snr":-4.0,"decoded":{"temperature":21.5}}`,
			i, strings.Repeat("ab", 70+i%23))
	}
	b.WriteString("]}")
	return b.String()
}

func TestSamplingIsPinnedOnEveryRound(t *testing.T) {
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{"get_device": `{"dev_eui":"A"}`}}
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("1", "get_device", `{"dev_eui":"A"}`),
		toolCall("2", "get_device", `{"dev_eui":"A"}`),
		textResponse("Both are provisioned."),
	}}
	svc := newTestService(t, net, model)

	if _, err := collect(t, svc, []llm.Message{{Role: llm.RoleUser, Content: "two devices?"}}); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(model.requests) != 3 {
		t.Fatalf("model saw %d requests, want 3", len(model.requests))
	}

	for i, req := range model.requests {
		if req.Temperature == nil {
			t.Fatalf("request %d: temperature left to the runtime's default", i)
		}
		if *req.Temperature != answerTemperature {
			t.Errorf("request %d: temperature = %v, want %v", i, *req.Temperature, answerTemperature)
		}
		if req.MaxTokens != maxAnswerTokens {
			t.Errorf("request %d: max_tokens = %d, want %d", i, req.MaxTokens, maxAnswerTokens)
		}
	}
}

// An oversized result must be cut, and the model must be told it was cut. The failure
// being prevented is subtle and bad: a model handed the first 6 KB of a 200-device list
// reports "there are 14 devices" with total confidence, because nothing said otherwise.
func TestOversizedToolResultIsCutAndSaysSo(t *testing.T) {
	huge := bigResult(400)
	if len(huge) <= maxToolResultBytes {
		t.Fatalf("fixture is %d bytes; it must exceed the cap to test it", len(huge))
	}

	net := &fakeNetwork{tools: standardTools(), results: map[string]string{"list_uplinks": huge}}
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("1", "list_uplinks", `{}`),
		textResponse("Three uplinks."),
	}}
	svc := newTestService(t, net, model)

	events, err := collect(t, svc, []llm.Message{{Role: llm.RoleUser, Content: "recent uplinks?"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	replayed := toolMessages(model.requests[1])
	if len(replayed) != 1 {
		t.Fatalf("second request carried %d tool results, want 1", len(replayed))
	}
	content := replayed[0].Content
	if len(content) >= len(huge) {
		t.Errorf("result was replayed whole: %d bytes of a %d byte document", len(content), len(huge))
	}
	if len(content) > maxToolResultBytes+len(cutMarkerProbe) {
		t.Errorf("replayed content is %d bytes, well past the cap", len(content))
	}
	for _, phrase := range []string{"was cut", "smaller limit"} {
		if !strings.Contains(content, phrase) {
			t.Errorf("the cut is not explained to the model; %q is missing from:\n%.200s", phrase, content)
		}
	}

	// The operator's trail must still show a successful lookup. The truncation is a
	// decision made here about the model's window, not a failure the network reported,
	// and labelling it as one would send someone to check the listener.
	var sawResult bool
	for _, e := range events {
		if e.Type == EventToolResult {
			sawResult = true
			if !e.OK {
				t.Error("a truncated result was reported to the operator as a failed lookup")
			}
		}
	}
	if !sawResult {
		t.Error("no tool_result event was emitted")
	}
}

// Cutting on a byte rather than on a record boundary shows the model a half-written
// field, which reads as corruption on the network rather than as a decision made here —
// and sends it to debug the wrong thing.
func TestCutLandsOnARecordBoundary(t *testing.T) {
	result := bigResult(400)
	cut := fitResult(result, 0)
	body := strings.TrimSuffix(cut, cutMarkerProbe)
	if body == cut {
		t.Fatal("the marker was not appended, so the cut cannot be located")
	}
	if !strings.HasSuffix(body, "}") {
		t.Errorf("the cut ended mid-record, at: …%.80s", body[max(0, len(body)-80):])
	}
	if !strings.HasPrefix(result, body) {
		t.Error("the replayed prefix is not a prefix of the original result")
	}
	// Every record kept must be complete: closing the array has to be enough to make
	// the prefix parse, which is only true if no record was sliced open.
	if !json.Valid([]byte(body + "]}")) {
		t.Errorf("the kept records do not close cleanly; ending was: …%.60s", body[max(0, len(body)-60):])
	}
}

// The per-result cap alone is not enough: maxCalls is 12, so twelve results each under
// 6 KB is still more than a small window holds.
func TestTotalToolResultBudgetIsBounded(t *testing.T) {
	huge := bigResult(400)
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{"list_devices": huge}}

	var script []*llm.Response
	for round := range 3 {
		var calls []llm.ToolCall
		for range 4 {
			calls = append(calls, llm.ToolCall{
				ID: fmt.Sprintf("r%dc%d", round, len(calls)), Type: "function",
				Function: llm.FunctionCall{Name: "list_devices", Arguments: `{}`},
			})
		}
		script = append(script, &llm.Response{FinishReason: "tool_calls", ToolCalls: calls})
	}
	script = append(script, textResponse("Done."))

	model := &scriptedModel{responses: script}
	svc := newTestService(t, net, model)

	if _, err := collect(t, svc, []llm.Message{{Role: llm.RoleUser, Content: "the whole fleet?"}}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	last := model.requests[len(model.requests)-1]
	var total, count int
	var exhausted bool
	for _, m := range toolMessages(last) {
		count++
		total += len(m.Content)
		if strings.Contains(m.Content, "budget for this question is spent") {
			exhausted = true
		}
	}
	if count != 12 {
		t.Fatalf("final request carried %d tool results, want 12", count)
	}
	if total > maxToolResultsTotalBytes+count*len(cutMarkerProbe) {
		t.Errorf("tool results in the final request total %d bytes, past the %d byte budget",
			total, maxToolResultsTotalBytes)
	}
	if !exhausted {
		t.Error("twelve oversized results were accepted without ever spending the budget")
	}
}

// The guard must be invisible when it is not needed. Appending a warning to every result
// would train the model to expect truncation and make it hedge about complete data.
func TestSmallResultIsReplayedVerbatim(t *testing.T) {
	const small = `{"dev_eui":"0011223344556677","name":"Tank 3","enabled":true}`
	net := &fakeNetwork{tools: standardTools(), results: map[string]string{"get_device": small}}
	model := &scriptedModel{responses: []*llm.Response{
		toolCall("1", "get_device", `{"dev_eui":"0011223344556677"}`),
		textResponse("It is enabled."),
	}}
	svc := newTestService(t, net, model)

	if _, err := collect(t, svc, []llm.Message{{Role: llm.RoleUser, Content: "is tank 3 on?"}}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	replayed := toolMessages(model.requests[1])
	if len(replayed) != 1 {
		t.Fatalf("got %d tool results, want 1", len(replayed))
	}
	if replayed[0].Content != small {
		t.Errorf("a result within budget was altered:\n got: %s\nwant: %s", replayed[0].Content, small)
	}
}

// A failure message is short and must survive intact. It is the only thing that lets the
// model correct a mistyped DevEUI, so cutting it would remove the loop's ability to
// recover while leaving the failure on screen.
func TestFailureTextIsNeverCut(t *testing.T) {
	const failure = "This lookup failed: bad DevEUI"
	if got := fitResult(failure, 0); got != failure {
		t.Errorf("a failure message was altered: %q", got)
	}
	if got := fitResult("", maxToolResultsTotalBytes); !strings.Contains(got, "budget") {
		t.Errorf("with the budget spent, an empty result became %q", got)
	}
}

// The fallback path: a result with no closing brace anywhere near the limit.
//
// This is not hypothetical. A model's formatter script, a gateway's log tail and a
// plain-text failure note are all one long run with no '}' in them, and they are exactly
// the results a diagnosis needs. The record-boundary rule cannot help there, so this
// path decides what the model sees — and the only acceptable answer is a cut that still
// says it was cut. A silent cut here is the original failure this whole function exists
// to prevent, arriving through a branch nothing tested.
func TestCutWithoutARecordBoundaryStillSaysSo(t *testing.T) {
	// One long object with a single closing brace at the very end: LastIndexByte over
	// the first `budget` bytes finds nothing, so the naive cut is what runs.
	plain := `{"log":"` + strings.Repeat("gateway TOO_LATE, tx rejected ", 600) + `"}`
	if strings.Contains(plain[:maxToolResultBytes], "}") {
		t.Fatal("fixture has a closing brace inside the budget, so it does not reach the fallback path")
	}

	got := fitResult(plain, 0)
	if !strings.HasSuffix(got, cutMarkerProbe) {
		t.Errorf("a byte-forced cut was replayed with no explanation; it ends: …%.120s",
			got[max(0, len(got)-120):])
	}
	if len(got) != maxToolResultBytes+len(cutMarkerProbe) {
		t.Errorf("cut content is %d bytes, want the budget (%d) plus the marker (%d)",
			len(got), maxToolResultBytes, len(cutMarkerProbe))
	}
	if !strings.HasPrefix(plain, got[:maxToolResultBytes]) {
		t.Error("the fallback did not return a prefix of the original")
	}
}

// The other side of the same rule: a closing brace is only worth honouring if it does
// not throw away most of the allowance.
//
// Without the halfway floor, a result that opens with one small object and then a long
// array would be cut at the first '}' — perhaps at byte 40 of 6,000 — and the model
// would be told a careful, tidy prefix of a document it cannot answer from. The guard
// trades tidiness for information, and only a test that measures the kept length can
// tell that from a bug.
func TestAnEarlyBoundaryIsNotWorthMostOfTheBudget(t *testing.T) {
	result := `{"ok":true}` + `,"blob":"` + strings.Repeat("x", maxToolResultBytes*2) + `"}`
	first := strings.IndexByte(result, '}')
	if first >= maxToolResultBytes/2 {
		t.Fatalf("fixture's first closing brace is at %d; it must be well before the halfway "+
			"point (%d) to test the floor", first, maxToolResultBytes/2)
	}

	got := fitResult(result, 0)
	kept := strings.TrimSuffix(got, cutMarkerProbe)
	if kept == got {
		t.Fatal("no marker was appended, so the cut cannot be located")
	}
	if len(kept) < maxToolResultBytes/2 {
		t.Errorf("the cut kept %d of %d bytes by honouring a boundary at byte %d; "+
			"throwing away most of the allowance to avoid one split field is the wrong trade",
			len(kept), maxToolResultBytes, first)
	}
}
