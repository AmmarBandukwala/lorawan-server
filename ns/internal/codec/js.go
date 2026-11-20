package codec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// Options tunes the execution bounds. Zero values take the package defaults.
type Options struct {
	Timeout      time.Duration
	MaxCallStack int
	MaxScriptLen int
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxCallStack <= 0 {
		o.MaxCallStack = DefaultMaxCallStack
	}
	if o.MaxScriptLen <= 0 {
		o.MaxScriptLen = DefaultMaxScriptLen
	}
	return o
}

// JS runs a vendor payload formatter on a goja runtime.
//
// The script is compiled once, when an operator saves it, so a formatter with a
// syntax error is rejected in the console with the parser's own message instead
// of failing on the first uplink hours later. goja's compiled program is not
// bound to a runtime and may be run in several at once, which is what makes
// compile-once, run-per-uplink practical.
type JS struct {
	prog *goja.Program
	shim *goja.Program
	opts Options
}

// entryFuncs are the formatter entry points, in the order they are tried.
//
// decodeUplink is the TTN v3 and ChirpStack convention and takes one input
// object. Decoder and decode are the TTN v2 convention and take (bytes, port).
// Vendors have shipped both for the same device across firmware revisions, so
// the order is a preference rather than a rule.
var entryFuncs = []string{"decodeUplink", "Decoder", "decode"}

// NewJS compiles a formatter with the default bounds.
func NewJS(script string) (*JS, error) { return NewJSWithOptions(script, Options{}) }

// NewJSWithOptions compiles a formatter, reporting a usable error when the
// source is empty, oversized or does not parse.
func NewJSWithOptions(script string, opts Options) (*JS, error) {
	opts = opts.withDefaults()

	trimmed := strings.TrimSpace(script)
	if trimmed == "" {
		return nil, ErrEmptyScript
	}
	// Bytes, not runes: this is source code, and the limit bounds what the
	// interpreter parses, which is a function of bytes.
	if len(trimmed) > opts.MaxScriptLen {
		return nil, fmt.Errorf("%w: %d bytes, limit %d",
			ErrScriptTooBig, len(trimmed), opts.MaxScriptLen)
	}

	prog, err := goja.Compile("payload-formatter.js", trimmed, false)
	if err != nil {
		return nil, fmt.Errorf("codec: payload formatter does not parse: %w", err)
	}
	shim, err := goja.Compile("shims.js", shimsJS, false)
	if err != nil {
		// A broken shim is a bug in this package rather than operator error, and
		// it would make every codec fail; construction is the only place anyone
		// would ever see it.
		return nil, fmt.Errorf("codec: internal shim does not compile: %w", err)
	}
	return &JS{prog: prog, shim: shim, opts: opts}, nil
}

// Kind reports which codec this is, for the API response.
func (j *JS) Kind() Kind { return KindJS }

// Decode runs the formatter over one payload.
//
// A fresh runtime is created per call. goja runtimes are not safe for
// concurrent use, and reusing one across uplinks would also let a formatter's
// leftover globals leak into the next frame -- a failure that shows up as one
// device's data appearing in another's decode.
func (j *JS) Decode(ctx context.Context, in Input) (map[string]any, error) {
	vm := goja.New()
	vm.SetMaxCallStackSize(j.opts.MaxCallStack)

	if _, err := vm.RunProgram(j.shim); err != nil {
		return nil, fmt.Errorf("codec: shim setup failed: %w", err)
	}
	if _, err := vm.RunProgram(j.prog); err != nil {
		return nil, fmt.Errorf("codec: payload formatter failed to load: %w", err)
	}

	fn, args, err := j.entry(vm, in)
	if err != nil {
		return nil, err
	}
	raw, err := j.call(ctx, vm, fn, args)
	if err != nil {
		return nil, err
	}
	return normalize(vm, raw)
}

// entry finds the formatter's entry point and builds its arguments.
//
// Two arguments are always passed. The v3 convention reads only the first and
// JavaScript ignores the extra, while the v2 convention needs the port as its
// second parameter -- passing one argument would leave every v2 formatter's
// `port` undefined.
func (j *JS) entry(vm *goja.Runtime, in Input) (goja.Callable, []goja.Value, error) {
	for _, name := range entryFuncs {
		v := vm.Get(name)
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			continue
		}
		fn, ok := goja.AssertFunction(v)
		if !ok {
			continue
		}
		return fn, j.args(vm, in), nil
	}
	return nil, nil, ErrNoEntryFunc
}

// args builds the values handed to the formatter.
//
// The first is a JavaScript array of the payload bytes that also carries the
// named properties, because the two conventions read one argument differently:
//
//	input.bytes / input.fPort   the v3 convention, reading properties
//	bytes.length / bytes[0]     the v2 convention, reading the array
//
// One value satisfies both, which matters because vendors have written v2-style
// bodies under the v3 name decodeUplink. The bytes property holds a separate
// array rather than the hybrid itself: a self-reference would make the value
// impossible to serialise if a formatter returned its input.
func (j *JS) args(vm *goja.Runtime, in Input) []goja.Value {
	items := make([]any, len(in.Bytes))
	for i, b := range in.Bytes {
		items[i] = int64(b)
	}
	hybrid := vm.NewArray(items...)

	vars := vm.NewObject()
	for k, v := range in.Variables {
		_ = vars.Set(k, v)
	}

	_ = hybrid.Set("bytes", vm.NewArray(items...))
	_ = hybrid.Set("fPort", int64(in.FPort))
	_ = hybrid.Set("port", int64(in.FPort))
	_ = hybrid.Set("devEUI", in.DevEUI)
	_ = hybrid.Set("variables", vars)
	if !in.ReceivedAt.IsZero() {
		// An ISO-8601 string rather than a Date: it survives being returned or
		// logged, which a host Date object would not.
		_ = hybrid.Set("recvTime", in.ReceivedAt.UTC().Format(time.RFC3339Nano))
	}
	return []goja.Value{hybrid, vm.ToValue(int64(in.FPort))}
}

// call invokes the formatter with the execution deadline enforced.
//
// goja checks for interruption at instruction boundaries, so a timer calling
// Interrupt stops a loop. There is nothing else to wait on: the runtime exposes
// no I/O, so a formatter cannot block on the host. The context is honoured as
// well, so a shutdown mid-decode does not sit out the full deadline.
func (j *JS) call(ctx context.Context, vm *goja.Runtime, fn goja.Callable, args []goja.Value) (goja.Value, error) {
	type outcome struct {
		val goja.Value
		err error
	}
	// Buffered so the goroutine's send never blocks, even after this function
	// has already returned on a timeout. An unbuffered channel would leak the
	// goroutine on every timed-out decode.
	done := make(chan outcome, 1)

	go func() {
		val, err := fn(goja.Undefined(), args...)
		done <- outcome{val, err}
	}()

	timer := time.AfterFunc(j.opts.Timeout, func() { vm.Interrupt(ErrTimeout) })
	defer timer.Stop()

	select {
	case res := <-done:
		if res.err != nil {
			return nil, j.runtimeError(res.err)
		}
		if res.val == nil || goja.IsUndefined(res.val) || goja.IsNull(res.val) {
			return nil, ErrBadReturn
		}
		return res.val, nil
	case <-ctx.Done():
		// Interrupt so the goroutine unwinds instead of running to completion
		// with nobody listening.
		vm.Interrupt(ctx.Err())
		return nil, ctx.Err()
	}
}

// normalize turns the formatter's return value into the decoded object.
//
// The v3 contract returns { data: {...}, errors: [...] }; the v2 contract
// returns the decoded object directly. Both are accepted, and a non-empty
// errors list is an error even when data is present, because a formatter that
// reports a problem has not decoded the frame.
func normalize(vm *goja.Runtime, val goja.Value) (map[string]any, error) {
	// Exporting to `any` rather than straight to a map is deliberate: goja will
	// happily export a JS array into map[string]any with keys "0", "1", "2", so
	// a formatter that returns an array would otherwise pass as a decoded object
	// full of numeric keys instead of being named as the contract violation it is.
	var exported any
	if err := vm.ExportTo(val, &exported); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadReturn, err)
	}
	raw, ok := exported.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: got %T, want an object", ErrBadReturn, exported)
	}

	if msgs, ok := raw["errors"]; ok && nonEmpty(msgs) {
		return nil, fmt.Errorf("codec: payload formatter reported: %s", joinErrors(msgs))
	}
	if data, ok := raw["data"]; ok {
		if data == nil {
			return map[string]any{}, nil
		}
		m, ok := data.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: 'data' is %T, want an object", ErrBadReturn, data)
		}
		return m, nil
	}
	// v2 shape: the whole return value is the decoded object.
	return raw, nil
}

func nonEmpty(v any) bool {
	switch list := v.(type) {
	case []any:
		return len(list) > 0
	default:
		// errors: null is not a complaint; errors: "bad frame" is.
		return v != nil
	}
}

func joinErrors(v any) string {
	switch list := v.(type) {
	case []any:
		parts := make([]string, 0, len(list))
		for _, e := range list {
			parts = append(parts, fmt.Sprint(e))
		}
		return strings.Join(parts, "; ")
	default:
		return fmt.Sprint(v)
	}
}

// runtimeError turns a goja error into a codec error, preserving ErrTimeout so
// a hung formatter is distinguishable from a broken one.
func (j *JS) runtimeError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), ErrTimeout.Error()) {
		return fmt.Errorf("%w after %s", ErrTimeout, j.opts.Timeout)
	}
	return fmt.Errorf("codec: payload formatter error: %w", err)
}
