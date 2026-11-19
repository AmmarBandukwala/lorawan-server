package engine_test

import (
	"testing"

	"github.com/fieldnode/lorawan-server/ns/internal/engine"
)

// TestUseRegionDefaultIsTheDocumentedWireValue pins the value the console API
// restates as httpapi.regionDefault.
//
// The two live in separate Go modules and no test can import both, so equality
// cannot be asserted directly. Each module pins the literal instead, and each
// failure message names the other constant, which turns a silent protocol change
// into a red test in whichever module moved.
func TestUseRegionDefaultIsTheDocumentedWireValue(t *testing.T) {
	if engine.UseRegionDefault != -1 {
		t.Errorf("UseRegionDefault = %d, want -1: api/internal/httpapi.regionDefault is -1 "+
			"and the two must stay equal — change both or neither", engine.UseRegionDefault)
	}
}
