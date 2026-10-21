package pool

import (
	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/provisioner"
)

// runKey is the run key a handover gave, as a child reads it: the bytes of the
// provisioner client's holder, copied once into a buffer of their length when the child
// asks for them. Clear clears the buffer and the holder, and is safe to call more than
// once.
type runKey struct {
	held provisioner.Hidden
	b    []byte
}

var _ child.Secret = (*runKey)(nil)

// into appends what is written to b, which has room for it, so nothing is copied
// elsewhere.
type into struct{ b *[]byte }

func (w into) Write(p []byte) (int, error) {
	*w.b = append(*w.b, p...)
	return len(p), nil
}

// Bytes is the key.
func (k *runKey) Bytes() []byte {
	if k.b == nil && k.held.Len() > 0 {
		k.b = make([]byte, 0, k.held.Len())
		_, _ = k.held.WriteTo(into{&k.b})
	}
	return k.b
}

// Clear clears the key's bytes, here and in the holder.
func (k *runKey) Clear() {
	clear(k.b)
	k.b = nil
	k.held.Clear()
}
