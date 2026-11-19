package contract_test

import (
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/lorawan"
)

// TestFullFCnt pins the counter reconstruction across rollover and
// re-transmission. The table is deliberately exhaustive rather than illustrative:
// every row is a case where a wrong high-half guess either drops a live device or
// accepts a replay, and implementations disagree here in practice.
func TestFullFCnt(t *testing.T) {
	// nextExpected, truncated, expected
	tests := []struct {
		next, truncated, want uint32
		name                  string
	}{
		{1, 1, 1, "as expected"},
		{1 << 16, 0, 1 << 16, "as expected after rollover"},
		{(1 << 16) + 1, 1, (1 << 16) + 1, "as expected past rollover"},
		{0, 1, 1, "one frame lost"},
		{(1 << 16) + 1, 2, (1 << 16) + 2, "one frame lost past rollover"},
		{2, 1, 1, "re-transmission"},
		{(1 << 16) + 1, 0, 1 << 16, "re-transmission past rollover"},
		{1 << 16, (1 << 16) - 1, (1 << 16) - 1, "re-transmission at rollover"},
		{0xFFFFFFFF, 0, 0, "32-bit wrap"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lorawan.FullFCnt(tc.next, uint16(tc.truncated)); got != tc.want {
				t.Errorf("FullFCnt(%d, %d) = %d, want %d", tc.next, tc.truncated, got, tc.want)
			}
		})
	}
}
