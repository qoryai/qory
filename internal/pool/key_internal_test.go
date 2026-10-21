package pool

import (
	"bytes"
	"testing"

	"github.com/qoryai/qory/internal/provisioner"
)

// TestTheRunKeyAsAChildReadsIt gives the holder's bytes, and clears them, the copy
// and the holder's, more than once.
func TestTheRunKeyAsAChildReadsIt(t *testing.T) {
	const value = "qrk_a.b"
	held := provisioner.Hide([]byte(value))
	k := &runKey{held: held}
	b := k.Bytes()
	if string(b) != value || cap(b) != len(value) {
		t.Fatalf("the key is %q, of capacity %d", b, cap(b))
	}
	k.Clear()
	if !bytes.Equal(b, make([]byte, len(value))) || held.Len() != 0 || len(k.Bytes()) != 0 {
		t.Errorf("the key was not cleared: %q, %d", b, held.Len())
	}
	k.Clear()
}
