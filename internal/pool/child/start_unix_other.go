//go:build unix && !linux && !darwin

package child

import "errors"

// errNotRead is the error of each call that reads a start or a session's members on a
// system that qory is not built for.
var errNotRead = errors.New("qory reads no start of a process and no session's members on this system")

// startStamp reads no start of a process on a system that qory is not built for.
func startStamp(int) (string, error) { return "", errNotRead }

// SessionMembers lists no process on a system that qory is not built for.
func SessionMembers(int) ([]Member, error) { return nil, errNotRead }
