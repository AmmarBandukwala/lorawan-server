// Package codec turns a device's binary application payload into JSON.
//
// Vendors ship these decoders as JavaScript -- a payload formatter that reads
// the bytes and returns an object -- so this package embeds a JavaScript engine
// and runs vendor-supplied source. That is the design constraint everything
// else follows from: the code being executed did not come from this repository,
// it was pasted in by an operator, and it runs on every uplink from that device.
//
// Three consequences are enforced here rather than left to convention:
//
//   - Execution is bounded. A formatter with an accidental infinite loop must
//     not pin a worker, so every run carries a deadline enforced by interrupting
//     the engine, plus a call-stack cap against unbounded recursion.
//   - Failure is not fatal. A frame the formatter cannot decode is still a frame
//     that was received: the caller stores the raw bytes alongside the error.
//     Decode returns an error; it never panics and never loses the uplink.
//   - The engine has no module system and no I/O. goja is a pure-Go interpreter
//     with no filesystem, network or process access, and require() is resolved
//     only against the handful of shims below.
//
// Decoding deliberately lives in the integration layer and not in the engine: a
// broken vendor script must be unable to affect MAC handling, frame counters or
// downlink scheduling.
package codec

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind selects the decoder implementation for a device model.
type Kind string

const (
	// KindNone leaves the payload as raw bytes. This is the default, and it is
	// a legitimate permanent choice, not a placeholder: a consumer that decodes
	// for itself does not want the server guessing.
	KindNone Kind = "NONE"

	// KindJS runs a vendor payload formatter.
	KindJS Kind = "JS"
)

// Defaults for the execution bounds. The deadline matches ChirpStack's
// codec.js.max_execution_time; a formatter that needs longer than ~100ms is
// doing something a payload decoder should not be doing.
const (
	DefaultTimeout      = 100 * time.Millisecond
	DefaultMaxCallStack = 512
	DefaultMaxScriptLen = 64 << 10
)

// Errors reported for a formatter that cannot be used. These are distinct from
// a JavaScript runtime error so the console can say something more useful than
// "the script failed".
var (
	ErrEmptyScript  = errors.New("codec: no script to run")
	ErrScriptTooBig = errors.New("codec: payload formatter is too large")
	ErrNoEntryFunc  = errors.New("codec: script defines neither decodeUplink nor Decoder")
	ErrTimeout      = errors.New("codec: payload formatter exceeded its execution time")
	ErrBadReturn    = errors.New("codec: payload formatter did not return an object")
)

// Input is one application payload presented to a formatter.
type Input struct {
	// Bytes is the decrypted FRMPayload.
	Bytes []byte
	// FPort is the port the frame arrived on. Formatters switch on it when a
	// device multiplexes several payload shapes over one port range.
	FPort int
	// ReceivedAt is when the network heard the frame, for codecs that timestamp
	// their own output.
	ReceivedAt time.Time
	// DevEUI is available to formatters that branch per device revision, which
	// vendors do more often than one would expect.
	DevEUI string
	// Variables are per-device profile values a formatter may need -- device
	// labels, calibration constants. Empty for most codecs.
	Variables map[string]string
}

// Decoder decodes application payloads for one device model.
type Decoder interface {
	Decode(ctx context.Context, in Input) (map[string]any, error)
}

// None returns no decoded data. It exists so the caller has one code path for
// "no codec" instead of a nil check at every call site.
type None struct{}

// Decode implements Decoder.
func (None) Decode(context.Context, Input) (map[string]any, error) { return nil, nil }

// New builds the decoder for a device model's codec configuration. An empty or
// unknown kind is treated as KindNone, because a device left without a formatter
// must still deliver its raw payload rather than fail.
func New(kind Kind, script string) (Decoder, error) {
	switch kind {
	case KindNone, "":
		return None{}, nil
	case KindJS:
		return NewJS(script)
	default:
		return nil, fmt.Errorf("codec: unknown codec kind %q", string(kind))
	}
}
