package codec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func decode(t *testing.T, script string, in Input) (map[string]any, error) {
	t.Helper()
	c, err := NewJS(script)
	if err != nil {
		return nil, err
	}
	return c.Decode(context.Background(), in)
}

// num normalises the numeric types goja exports. A JS number can arrive as
// float64, int64 or int depending on how it was produced, and a test that
// compares against one literal fails on the others for no real reason.
func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// wantNum and wantAny assert one decoded field, naming the key on failure.
func wantNum(t *testing.T, got map[string]any, key string, want float64) {
	t.Helper()
	v, ok := num(got[key])
	if !ok {
		t.Errorf("%s = %v (%T), want the number %v", key, got[key], got[key], want)
		return
	}
	if v != want {
		t.Errorf("%s = %v, want %v", key, v, want)
	}
}

func wantAny(t *testing.T, got map[string]any, key string, want any) {
	t.Helper()
	if got[key] != want {
		t.Errorf("%s = %v (%T), want %v (%T)", key, got[key], got[key], want, want)
	}
}

// v3Vendor is a realistic vendor formatter in the TTN v3 / ChirpStack shape:
// ES6 syntax, DataView over a typed array, and the {data, errors} contract.
const v3Vendor = `
const SCALE = 100;
function decodeUplink(input) {
  const b = input.bytes;
  if (b.length < 4) {
    return { data: {}, errors: ['frame too short: ' + b.length] };
  }
  const view = new DataView(Uint8Array.from(b).buffer);
  return {
    data: {
      temperature: view.getInt16(0) / SCALE,
      humidity: b[2],
      battery_ok: (b[3] & 1) === 1,
      port: input.fPort,
    },
    errors: [],
  };
}
`

func TestDecodeV3VendorFormatter(t *testing.T) {
	got, err := decode(t, v3Vendor, Input{Bytes: []byte{0x01, 0x18, 0x3c, 0x01}, FPort: 2})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// 0x0118 = 280, /100 = 2.8.
	wantNum(t, got, "temperature", 2.8)
	wantNum(t, got, "humidity", 60)
	wantAny(t, got, "battery_ok", true)
	wantNum(t, got, "port", 2)
}

// v2Vendor is the TTN v2 shape: Decoder(bytes, port), returning the object
// directly with no data/errors wrapper.
const v2Vendor = `
function Decoder(bytes, port) {
  return {
    temperature: ((bytes[0] << 8) | bytes[1]) / 100,
    humidity: bytes[2],
    port: port,
  };
}
`

func TestDecodeV2VendorFormatter(t *testing.T) {
	got, err := decode(t, v2Vendor, Input{Bytes: []byte{0x01, 0x18, 0x3c}, FPort: 7})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantNum(t, got, "temperature", 2.8)
	wantNum(t, got, "port", 7)
}

// v3NameV2Body is the awkward combination that exists in the field: the v3
// function name with a v2 body that reads its argument as an array. The hybrid
// argument is what makes this work, so it is tested rather than assumed.
const v3NameV2Body = `
function decodeUplink(input) {
  return { temperature: input[0] + input[1] / 100, length: input.length };
}
`

func TestDecodeV3NameWithV2StyleBody(t *testing.T) {
	got, err := decode(t, v3NameV2Body, Input{Bytes: []byte{21, 45}, FPort: 10})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantNum(t, got, "temperature", 21.45)
	wantNum(t, got, "length", 2)
}

// TestBufferShim covers the methods vendor codecs lean on. Without these a
// correct vendor codec fails with "Buffer is not defined" and the operator sees
// a device that never decodes.
func TestBufferShim(t *testing.T) {
	got, err := decode(t, `
function decodeUplink(input) {
  var buf = Buffer.from(input.bytes);
  return {
    data: {
      int16be: buf.readInt16BE(0),
      int16le: buf.readInt16LE(0),
      uint8: buf.readUInt8(0),
      hex: Buffer.from([0xde, 0xad]).toString('hex'),
      utf8: Buffer.from([104, 105]).toString('utf8'),
      b64: Buffer.from([104, 105]).toString('base64'),
      fromHex: Buffer.from('beef', 'hex').readUInt16BE(0),
      sliced: buf.slice(1, 3).readUInt16BE(0),
      len: buf.length,
      indexed: buf[0],
      isBuf: Buffer.isBuffer(buf),
      mapped: Array.from(buf.slice(0, 2)).map(function (x) { return x * 2; }),
    },
    errors: [],
  };
}
`, Input{Bytes: []byte{0x01, 0x02, 0x03, 0x04}})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantNum(t, got, "int16be", 258) // 0x0102
	wantNum(t, got, "int16le", 513) // 0x0201
	wantNum(t, got, "uint8", 1)
	wantAny(t, got, "hex", "dead")
	wantAny(t, got, "utf8", "hi")
	wantAny(t, got, "b64", "aGk=")
	wantNum(t, got, "fromHex", 48879) // 0xbeef
	wantNum(t, got, "sliced", 515)    // 0x02 0x03
	wantNum(t, got, "len", 4)
	wantNum(t, got, "indexed", 1)
	wantAny(t, got, "isBuf", true)

	mapped, ok := got["mapped"].([]any)
	if !ok || len(mapped) != 2 {
		t.Fatalf("mapped = %v, want a two-element array", got["mapped"])
	}
	if n, ok := num(mapped[1]); !ok || n != 4 {
		t.Errorf("mapped[1] = %v, want 4", mapped[1])
	}
}

// TestRequireShim checks both the allowed names and the refusal, because a
// clear refusal is what turns "my codec does not work" into an actionable
// message.
func TestRequireShim(t *testing.T) {
	got, err := decode(t, `
		var bufMod = require('buffer');
		var ieee = require('ieee754');
		function decodeUplink(input) {
		  var b = bufMod.Buffer.from(input.bytes);
		  return { data: {
		    hex: b.toString('hex'),
		    f32: ieee.read(b, 0, false, 23, 4),
		  }, errors: [] };
		}`, Input{Bytes: []byte{0x42, 0x28, 0x00, 0x00}})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantAny(t, got, "hex", "42280000")
	// 0x42280000 as a big-endian float32 is 42.0.
	wantNum(t, got, "f32", 42.0)

	_, err = decode(t, `
		var fs = require('fs');
		function decodeUplink(input) { return { data: {}, errors: [] }; }`,
		Input{Bytes: []byte{1}})
	if err == nil || !strings.Contains(err.Error(), "does not provide") {
		t.Errorf("require('fs') error = %v, want a message naming what is missing", err)
	}
}

func TestFormatterErrorsAreReported(t *testing.T) {
	_, err := decode(t, v3Vendor, Input{Bytes: []byte{0x01}, FPort: 2})
	if err == nil || !strings.Contains(err.Error(), "frame too short") {
		t.Errorf("err = %v, want the formatter's own message", err)
	}
}

// TestErrorsAlongsideDataIsAnError pins the rule that a formatter reporting a
// problem has not decoded the frame, even when it also returned data.
func TestErrorsAlongsideDataIsAnError(t *testing.T) {
	_, err := decode(t, `
		function decodeUplink(input) {
		  return { data: { temperature: 1 }, errors: ['checksum mismatch'] };
		}`, Input{Bytes: []byte{1}})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("err = %v, want the reported checksum problem", err)
	}
}

func TestEmptyErrorsArrayIsNotAProblem(t *testing.T) {
	got, err := decode(t, `
		function decodeUplink(input) { return { data: { ok: 1 }, errors: [] }; }`,
		Input{Bytes: []byte{1}})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantNum(t, got, "ok", 1)
}

// TestSyntaxErrorIsRejectedAtConstruction is the console-facing behaviour: the
// operator sees the parser's message when saving, not a silent failure later.
func TestSyntaxErrorIsRejectedAtConstruction(t *testing.T) {
	_, err := NewJS("function decodeUplink(input) { return {")
	if err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("err = %v, want a parse failure", err)
	}
}

func TestConstructionRejectsUnusableScripts(t *testing.T) {
	if _, err := NewJS("   "); !errors.Is(err, ErrEmptyScript) {
		t.Errorf("empty script err = %v, want ErrEmptyScript", err)
	}
	if _, err := NewJSWithOptions("function decodeUplink(i){return{};}",
		Options{MaxScriptLen: 10}); !errors.Is(err, ErrScriptTooBig) {
		t.Errorf("oversized err = %v, want ErrScriptTooBig", err)
	}
	_, err := decode(t, "function notTheEntry(x) { return {}; }", Input{Bytes: []byte{1}})
	if !errors.Is(err, ErrNoEntryFunc) {
		t.Errorf("err = %v, want ErrNoEntryFunc", err)
	}
}

// TestRunawayFormatterIsInterrupted is the guarantee that a formatter with an
// infinite loop costs a bounded amount of time rather than a worker.
func TestRunawayFormatterIsInterrupted(t *testing.T) {
	c, err := NewJSWithOptions("function decodeUplink(i){ while(true){} }",
		Options{Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewJS: %v", err)
	}

	start := time.Now()
	_, err = c.Decode(context.Background(), Input{Bytes: []byte{1}})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
	// Generous ceiling: the point is that it returns, not that it is exact.
	if elapsed > 2*time.Second {
		t.Errorf("timed-out decode took %v; the deadline did not fire", elapsed)
	}
}

// TestUnboundedRecursionIsBounded checks the stack cap turns runaway recursion
// into an error rather than a crashed process.
func TestUnboundedRecursionIsBounded(t *testing.T) {
	c, err := NewJSWithOptions(`
		function decodeUplink(i) { return decodeUplink(i); }`,
		Options{Timeout: time.Second, MaxCallStack: 64})
	if err != nil {
		t.Fatalf("NewJS: %v", err)
	}
	if _, err := c.Decode(context.Background(), Input{Bytes: []byte{1}}); err == nil {
		t.Error("infinite recursion must be reported, not crash the process")
	}
}

func TestBadReturnShapesAreReported(t *testing.T) {
	for name, script := range map[string]string{
		"undefined":   "function decodeUplink(i) { /* returns nothing */ }",
		"null":        "function decodeUplink(i) { return null; }",
		"scalar":      "function decodeUplink(i) { return 42; }",
		"array":       "function decodeUplink(i) { return [1, 2, 3]; }",
		"data scalar": "function decodeUplink(i) { return { data: 5, errors: [] }; }",
	} {
		_, err := decode(t, script, Input{Bytes: []byte{1}})
		if !errors.Is(err, ErrBadReturn) {
			t.Errorf("%s: err = %v, want ErrBadReturn", name, err)
		}
	}
}

// A formatter that throws is a broken codec, and the reason must survive to the
// console rather than arriving as an opaque runtime failure.
func TestThrownErrorIsReported(t *testing.T) {
	_, err := decode(t, `
		function decodeUplink(input) { throw new Error('bad sensor revision'); }`,
		Input{Bytes: []byte{1}})
	if err == nil || !strings.Contains(err.Error(), "bad sensor revision") {
		t.Errorf("err = %v, want the thrown message", err)
	}
}

func TestNoneCodec(t *testing.T) {
	c, err := New(KindNone, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.Decode(context.Background(), Input{Bytes: []byte{1, 2}})
	if err != nil || got != nil {
		t.Errorf("None decode = %v/%v, want nil/nil", got, err)
	}
	// An empty kind is treated as none rather than an error, so a device stored
	// before codecs existed still delivers its raw payload.
	if _, err := New("", ""); err != nil {
		t.Errorf("empty kind: %v", err)
	}
	if _, err := New("CAYENNE_LPP", ""); err == nil {
		t.Error("an unknown kind must be refused rather than silently ignored")
	}
}

// TestES6Features guards the claim that vendor codecs using modern syntax run.
// The template literal is spliced in because its backticks would close the Go
// raw string.
func TestES6Features(t *testing.T) {
	const bt = "`"
	got, err := decode(t, `
const scale = 100;
const double = (x) => x * 2;
function decodeUplink(input) {
  const t = input.bytes[0] / scale;
  return { data: {
    doubled: double(input.bytes[0]),
    label: `+bt+`${t}C`+bt+`,
    spread: Math.max(...input.bytes),
    letBlock: (() => { const z = 3; return z; })(),
    cls: new (class { get v() { return 7; } })().v,
  }, errors: [] };
}
`, Input{Bytes: []byte{21, 30}})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantNum(t, got, "doubled", 42)
	wantAny(t, got, "label", "0.21C")
	wantNum(t, got, "spread", 30)
	wantNum(t, got, "letBlock", 3)
	wantNum(t, got, "cls", 7)
}

// TestConcurrentDecodeUsesOneCompiledProgram verifies the compile-once, run-many
// design instead of assuming it: one *JS shared across goroutines, each getting
// its own correct result. Under -race this also catches accidental sharing of a
// runtime.
func TestConcurrentDecodeUsesOneCompiledProgram(t *testing.T) {
	c, err := NewJS(v3Vendor)
	if err != nil {
		t.Fatalf("NewJS: %v", err)
	}

	const n = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine decodes a different temperature, so a shared or
			// clobbered runtime shows up as a wrong value and not merely a race.
			got, err := c.Decode(context.Background(), Input{
				Bytes: []byte{byte(i), 0x00, 0x10, 0x00}, FPort: 2,
			})
			if err != nil {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("goroutine %d: %v", i, err))
				mu.Unlock()
				return
			}
			if v, ok := num(got["temperature"]); !ok || v != float64(i*256)/100 {
				mu.Lock()
				failures = append(failures,
					fmt.Sprintf("goroutine %d: temperature %v, want %v",
						i, got["temperature"], float64(i*256)/100))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	for _, f := range failures {
		t.Error(f)
	}
}

// TestGlobalsDoNotLeakBetweenDecodes is the isolation guarantee: a formatter
// that writes a global must not have it visible to the next device's decode.
func TestGlobalsDoNotLeakBetweenDecodes(t *testing.T) {
	c, err := NewJS(`
		function decodeUplink(input) {
		  if (typeof globalThis.seenBefore === 'undefined') {
		    globalThis.seenBefore = 1;
		    return { data: { first: true }, errors: [] };
		  }
		  return { data: { first: false }, errors: [] };
		}`)
	if err != nil {
		t.Fatalf("NewJS: %v", err)
	}
	for i := 0; i < 3; i++ {
		got, err := c.Decode(context.Background(), Input{Bytes: []byte{1}})
		if err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		if got["first"] != true {
			t.Fatalf("decode %d saw a global left behind by an earlier decode", i)
		}
	}
}

func TestContextCancellationStopsADecode(t *testing.T) {
	c, err := NewJSWithOptions("function decodeUplink(i){ while(true){} }",
		Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("NewJS: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.Decode(ctx, Input{Bytes: []byte{1}}); err == nil {
		t.Fatal("a cancelled decode must report an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancelled decode took %v; the context was not honoured", elapsed)
	}
}

func TestInputFieldsReachTheFormatter(t *testing.T) {
	got, err := decode(t, `
		function decodeUplink(input) {
		  return { data: {
		    dev: input.devEUI, port: input.fPort, recv: input.recvTime,
		    site: input.variables.site,
		  }, errors: [] };
		}`, Input{
		Bytes:      []byte{1},
		FPort:      19,
		DevEUI:     "0011223344556677",
		ReceivedAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		Variables:  map[string]string{"site": "depot-a"},
	})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantAny(t, got, "dev", "0011223344556677")
	wantNum(t, got, "port", 19)
	wantAny(t, got, "recv", "2026-11-17T10:00:00Z")
	wantAny(t, got, "site", "depot-a")
}

// TestDecodeDoesNotMutateInput matters because the caller keeps the frame after
// decoding it: a formatter that scribbles on its argument must not corrupt the
// stored payload.
func TestDecodeDoesNotMutateInput(t *testing.T) {
	in := Input{Bytes: []byte{1, 2, 3}, FPort: 1}
	_, err := decode(t, `
		function decodeUplink(input) {
		  input.bytes[0] = 99;
		  return { data: { ok: 1 }, errors: [] };
		}`, in)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if in.Bytes[0] != 1 {
		t.Errorf("the formatter mutated the caller's payload: bytes[0] = %d", in.Bytes[0])
	}
}
