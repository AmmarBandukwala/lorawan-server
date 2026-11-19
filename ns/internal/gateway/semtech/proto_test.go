package semtech

import (
	"bytes"
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

func mustEUI(t *testing.T, s string) lorawan.EUI64 {
	t.Helper()
	v, err := lorawan.ParseEUI64(s)
	if err != nil {
		t.Fatalf("ParseEUI64(%q): %v", s, err)
	}
	return v
}

func TestHeaderRoundTripV2(t *testing.T) {
	gw := mustEUI(t, "aa55555555555555")
	h := Header{Version: Version2, Type: PushData, Sequence: 0x1234, Gateway: gw}

	pkt := BuildPacket(h, []byte(`{"rxpk":[]}`))
	// version 2 in the high 3 bits, PUSH_DATA (0) in the low 5.
	if pkt[0] != 0x40 {
		t.Errorf("first byte = 0x%02x, want 0x40", pkt[0])
	}
	if pkt[1] != 0x12 || pkt[2] != 0x34 {
		t.Errorf("sequence bytes = %02x %02x, want 12 34 (big endian)", pkt[1], pkt[2])
	}

	back, err := ParseHeader(pkt)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if back.Version != Version2 || back.Type != PushData || back.Sequence != 0x1234 {
		t.Errorf("header = %+v", back)
	}
	if back.Gateway != gw {
		t.Errorf("gateway = %s, want %s", back.Gateway, gw)
	}
	if back.BodyStart != 11 {
		t.Errorf("BodyStart = %d, want 11 (1 + 2 + 8-byte EUI)", back.BodyStart)
	}
	if body := pkt[back.BodyStart:]; !bytes.Equal(body, []byte(`{"rxpk":[]}`)) {
		t.Errorf("body = %q", body)
	}
}

// TestHeaderV1MACIdentifier covers the trap door of this protocol: v1 carries a
// 6-byte MAC, so the JSON body starts two bytes earlier than a v2 parser expects.
func TestHeaderV1MACIdentifier(t *testing.T) {
	h := Header{
		Version:  Version1,
		Type:     PullData,
		Sequence: 1,
		Gateway:  lorawan.EUI64{0, 0, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06},
	}
	pkt := BuildPacket(h, []byte(`{"x":1}`))
	// 9 header bytes (1 + 2 + 6-byte MAC) + the 7-byte body.
	if len(pkt) != 16 {
		t.Fatalf("v1 PULL_DATA + body = %d bytes, want 16", len(pkt))
	}
	// version 1 in the high 3 bits (0x20) OR-ed with PULL_DATA (0x02).
	if pkt[0] != 0x22 {
		t.Errorf("first byte = 0x%02x, want 0x22", pkt[0])
	}

	back, err := ParseHeader(pkt)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if !back.HasMAC {
		t.Error("v1 packet must report a MAC identifier")
	}
	if back.MAC != ([6]byte{1, 2, 3, 4, 5, 6}) {
		t.Errorf("MAC = %x, want 010203040506", back.MAC)
	}
	if back.BodyStart != 9 {
		t.Errorf("BodyStart = %d, want 9 (1 + 2 + 6-byte MAC)", back.BodyStart)
	}
	if body := pkt[back.BodyStart:]; !bytes.Equal(body, []byte(`{"x":1}`)) {
		t.Errorf("body = %q, want the JSON to start right after the MAC", body)
	}
}

func TestAckHeadersHaveNoIdentifier(t *testing.T) {
	for _, tc := range []struct {
		typ   PacketType
		first byte
	}{
		{PushAck, 0x41},
		{PullAck, 0x44},
		{PullResp, 0x43},
	} {
		pkt := MarshalHeader(Header{Version: Version2, Type: tc.typ, Sequence: 7})
		if len(pkt) != 3 {
			t.Errorf("%s header = %d bytes, want 3", tc.typ, len(pkt))
		}
		if pkt[0] != tc.first {
			t.Errorf("%s first byte = 0x%02x, want 0x%02x", tc.typ, pkt[0], tc.first)
		}
		back, err := ParseHeader(pkt)
		if err != nil {
			t.Fatalf("ParseHeader(%s): %v", tc.typ, err)
		}
		if back.Type != tc.typ || back.Sequence != 7 || back.BodyStart != 3 {
			t.Errorf("header = %+v, want BodyStart 3 (header length)", back)
		}
	}
}

// TestPullRespBodyOffset pins the one asymmetry of the header: PULL_RESP carries
// a txpk body but no gateway identifier, so its body starts at 3 while
// PUSH_DATA's starts at 11.
func TestPullRespBodyOffset(t *testing.T) {
	pkt := BuildPacket(Header{Version: Version2, Type: PullResp, Sequence: 9},
		[]byte(`{"txpk":{"imme":true}}`))
	back, err := ParseHeader(pkt)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if back.BodyStart != 3 {
		t.Fatalf("BodyStart = %d, want 3", back.BodyStart)
	}
	if body := pkt[back.BodyStart:]; string(body) != `{"txpk":{"imme":true}}` {
		t.Errorf("body = %q, want the txpk JSON with no header bytes left in it", body)
	}
}

func TestParseHeaderRejectsMalformedPackets(t *testing.T) {
	cases := map[string][]byte{
		"empty":                          {},
		"shorter than the 3-byte header": {0x40, 0x00},
		"version 0":                      {0x00, 0x00, 0x01},
		"version 3":                      {0x60, 0x00, 0x01}, // 3<<5 | PUSH_DATA
		"unknown type":                   {0x5f, 0x00, 0x01}, // v2, type 0x1f
		"v2 PUSH_DATA with a truncated EUI": {
			0x40, 0x00, 0x01, 0xaa, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55,
		},
	}
	for name, pkt := range cases {
		if _, err := ParseHeader(pkt); err == nil {
			t.Errorf("%s: ParseHeader accepted % x", name, pkt)
		}
	}
}

// TestParseHeaderAcceptsValidHeaderWithJunkBody is deliberate: a forwarder whose
// JSON we cannot parse must still be answered, otherwise it keeps retrying and
// the failure is invisible. Body validation belongs to the caller.
func TestParseHeaderAcceptsValidHeaderWithJunkBody(t *testing.T) {
	gw := mustEUI(t, "aa55555555555555")
	pkt := BuildPacket(Header{Version: Version2, Type: PushData, Sequence: 3, Gateway: gw}, []byte("not json"))
	if _, err := ParseHeader(pkt); err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
}
