package foragerdir

import (
	"context"
	"os/exec"
	"regexp"
	"time"
)

// platformIdentity is the machine's IOPlatformUUID, as ioreg reports it.
func platformIdentity() []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return nil
	}
	if m := platformUUID.FindSubmatch(out); m != nil {
		return m[1]
	}
	return nil
}

// platformUUID is the line of ioreg's output that holds the identity.
var platformUUID = regexp.MustCompile(`"IOPlatformUUID" = "([^"]+)"`)
