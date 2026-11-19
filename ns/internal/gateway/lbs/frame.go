package lbs

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// arrivalTime picks the best available reception timestamp.
//
// Preference order matters for the dashboard's "last seen": an explicit UTC time
// from a GPS-locked station beats a station-computed UNIX time, which beats the
// server's clock. Falling back to the server clock is still better than a zero
// time, which renders as "never".
func arrivalTime(msg uplink, now func() time.Time) time.Time {
	if msg.Radio.Time != "" {
		if t, err := time.Parse(time.RFC3339Nano, msg.Radio.Time); err == nil {
			return t
		}
	}
	if msg.UpInfo.RxTime != 0 {
		seconds := int64(msg.UpInfo.RxTime)
		nanos := int64((msg.UpInfo.RxTime - float64(seconds)) * 1e9)
		return time.Unix(seconds, nanos).UTC()
	}
	return now()
}

// uplinkToken packs an xtime into the 8-byte transport token the engine echoes
// back on a downlink.
//
// The port's token is exactly 64 bits, which is exactly the width of an xtime, so
// the whole value — session identifier included — travels through unchanged and
// the downlink can be built without the server remembering anything about the
// uplink. That is a real advantage over the UDP transport, where the token has to
// be a compressed summary.
func uplinkToken(x xtime) [8]byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], uint64(x))
	return out
}

// xTimeFromToken reverses uplinkToken.
func xTimeFromToken(token [8]byte) xtime {
	return xtime(binary.BigEndian.Uint64(token[:]))
}

// decodeDrawx reads the base64url, unpadded raw frame.
func decodeDrawx(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("lbs: no raw frame")
	}
	raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(s)
	if err != nil {
		// Some builds pad the value. Retrying with padding accepts both without
		// loosening the encoding in any other way.
		padded, padErr := base64.URLEncoding.DecodeString(s)
		if padErr != nil {
			return nil, fmt.Errorf("lbs: drawx is not base64url: %w", err)
		}
		return padded, nil
	}
	return raw, nil
}

// encodeDrawx is the inverse, used by tests and by any future upstream
// simulation.
func encodeDrawx(raw []byte) string {
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(raw)
}

// hexBytes decodes an optional hex field, returning nil for an absent one.
func hexBytes(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return out, nil
}
