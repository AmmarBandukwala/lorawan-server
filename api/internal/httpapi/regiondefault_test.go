package httpapi

import "testing"

// TestRegionDefaultMatchesTheNetworkServer pins the wire value that means "no
// device-specific limit".
//
// It looks redundant with the constant's own declaration, and that is the point
// of stating it as a test: the number is a contract with the network server, not
// a local choice. The ns module declares the same value as engine.UseRegionDefault
// and pins it in its own suite; the two modules cannot import each other, so
// changing one now fails its own test with a message naming the other.
func TestRegionDefaultMatchesTheNetworkServer(t *testing.T) {
	if regionDefault != -1 {
		t.Errorf("regionDefault = %d, want -1: ns/internal/engine.UseRegionDefault is -1 "+
			"and the two must stay equal — change both or neither", regionDefault)
	}
}
