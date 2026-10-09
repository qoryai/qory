package foragerdir

import (
	"bytes"
	"os"
)

// MachineID returns the machine's identity, which the instance id file keeps a keyed
// hash of: /etc/machine-id on Linux, IOPlatformUUID on macOS, else the host name, as
// machine-id(5) recommends.
func MachineID() []byte {
	if id := bytes.TrimSpace(platformIdentity()); len(id) > 0 {
		return id
	}
	host, _ := os.Hostname()
	return []byte(host)
}
