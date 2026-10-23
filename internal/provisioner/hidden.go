package provisioner

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

// hiddenText is how a [Hidden] prints, logs and marshals.
const hiddenText = "[hidden]"

// Hidden has the bytes of a secret, such as the run code of a claim's answer, and keeps
// them out of every log, error and printout: it prints as [hidden] whatever the verb,
// and logs and marshals as [hidden] too. It keeps the bytes behind two pointers, so a
// Hidden inside another value, even in a field fmt cannot call its methods on, prints
// as an address, never as the bytes: fmt follows one pointer to a slice when it reports
// a verb the field does not take, and no pointer to a pointer. A printer that follows
// pointers by reflection would show them. Copies of a Hidden share the bytes, and
// [Hidden.Clear] clears them for every copy. The zero Hidden has no bytes.
type Hidden struct{ p **[]byte }

// Hide is a Hidden of b, which it takes: the caller keeps no other copy and clears the
// bytes with [Hidden.Clear] once they are used.
func Hide(b []byte) Hidden {
	p := &b
	return Hidden{&p}
}

// Len is the number of bytes kept, 0 once cleared.
func (h Hidden) Len() int {
	if h.p == nil {
		return 0
	}
	return len(**h.p)
}

// WriteTo writes the bytes kept to w, the one way they leave the Hidden: to the file or
// the pipe a run reads its secret from.
func (h Hidden) WriteTo(w io.Writer) (int64, error) {
	if h.p == nil {
		return 0, nil
	}
	n, err := w.Write(**h.p)
	return int64(n), err
}

// Clear overwrites the bytes kept with zeros and drops them, for this Hidden and every
// copy of it.
func (h Hidden) Clear() {
	if h.p == nil {
		return
	}
	clear(**h.p)
	**h.p = nil
}

// String is [hidden].
func (h Hidden) String() string { return hiddenText }

// GoString is [hidden].
func (h Hidden) GoString() string { return hiddenText }

// Format writes [hidden] for every verb.
func (h Hidden) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, hiddenText) }

// LogValue is [hidden].
func (h Hidden) LogValue() slog.Value { return slog.StringValue(hiddenText) }

// MarshalJSON is [hidden], as a JSON string.
func (h Hidden) MarshalJSON() ([]byte, error) { return json.Marshal(hiddenText) }

// MarshalText is [hidden].
func (h Hidden) MarshalText() ([]byte, error) { return []byte(hiddenText), nil }
