package foragerdir

import (
	"bytes"
	"os"
)

// platformIdentity is /etc/machine-id.
func platformIdentity() []byte {
	b, err := os.ReadFile("/etc/machine-id")
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return b
}
