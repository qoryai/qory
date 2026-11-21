//go:build !darwin

package codex

// onDiskCase is p: off macOS, the volumes that qory runs on keep the case of a name as
// it is written.
func onDiskCase(p string) string {
	return p
}
