//go:build !linux && !darwin

package foragerdir

// platformIdentity is none on a system other than Linux and macOS: the host name stands
// in for it.
func platformIdentity() []byte { return nil }
