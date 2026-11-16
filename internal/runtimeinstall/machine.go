package runtimeinstall

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// hostShape is a registry's host in a run code's file: letters, digits, . and -, with
// an optional port after a colon. No character of a URL's other parts is in it.
var hostShape = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)

// validHost reports whether host is a registry's host as a run code's file has it: one
// that [hostShape] matches, with a port from 1 to 65535 when it has one.
func validHost(host string) bool {
	if !hostShape.MatchString(host) {
		return false
	}
	if _, port, err := net.SplitHostPort(host); err == nil {
		n, err := strconv.Atoi(port)
		return err == nil && n >= 1 && n <= 65535
	}
	return !strings.Contains(host, ":")
}

// Machine is where a run installs its runtime on a machine that a pool runs: the
// instance's registry, the address of the Qory Apiary instance whose token route the
// registry's realm must be, and the runtimes folder that the pool's runs share. qory run
// pool writes it after the run code, in the file it gives qory run's --run-code-file,
// as [Machine.Lines]; qory run reads it with [ParseMachine]. A field that is empty is
// left out.
type Machine struct {
	// Registry is the instance's registry, a host and a port or a host, see
	// [RegistryHost].
	Registry string
	// Instance is the address of the Qory Apiary instance, an https URL, or http on
	// loopback, with a host and an optional port alone, see [CheckInstance]. It is
	// written only with a Registry, which it is the token route's address for.
	Instance string
	// Folder is the runtimes folder, an absolute clean path.
	Folder string
}

// The keys of a Machine's lines.
const (
	machineRegistry = "registry"
	machineInstance = "apiary"
	machineFolder   = "runtimes"
)

// Lines is m as lines of the run code's file: "registry <host>", "apiary <url>" with a
// Registry only, and "runtimes <folder>", each with a newline, in that order.
func (m Machine) Lines() string {
	var b strings.Builder
	if m.Registry != "" {
		b.WriteString(machineRegistry + " " + m.Registry + "\n")
		if m.Instance != "" {
			b.WriteString(machineInstance + " " + m.Instance + "\n")
		}
	}
	if m.Folder != "" {
		b.WriteString(machineFolder + " " + m.Folder + "\n")
	}
	return b.String()
}

// ParseMachine reads lines, the lines of the run code's file after the code, each
// "registry <host>", "apiary <url>" or "runtimes <folder>", each key at most once, with
// no control character; ok is false for any other line. The registry's host decides which
// program the run installs and runs, so it is only letters, digits, . and -, with an
// optional port from 1 to 65535: no user, path, query, fragment or escape. The address
// of the instance decides where the run takes the registry's credential from, so
// [CheckInstance] takes it.
func ParseMachine(lines []string) (m Machine, ok bool) {
	for _, line := range lines {
		if strings.ContainsFunc(line, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return Machine{}, false
		}
		key, value, _ := strings.Cut(line, " ")
		switch {
		case key == machineRegistry && m.Registry == "" && value != "" && RegistryHost(value+"/x") == value:
			m.Registry = value
		case key == machineInstance && m.Instance == "" && CheckInstance(value) == nil:
			m.Instance = value
		case key == machineFolder && m.Folder == "" && filepath.IsAbs(value) && filepath.Clean(value) == value:
			m.Folder = value
		default:
			return Machine{}, false
		}
	}
	return m, true
}

// CheckHost returns nil when host is a registry's host, as pool.registry and a run
// code's file have it: letters, digits, . and -, with an optional port from 1 to 65535.
// Its error starts with the fix, and the host is on the second line.
func CheckHost(host string) error {
	if validHost(host) {
		return nil
	}
	return fmt.Errorf("Set pool.registry to an image name on a host of letters, digits, . and -, with an optional port from 1 to 65535\nThe host is %q.", host)
}

// CheckInstance returns nil when address is the address of a Qory Apiary instance as a
// run code's file has it: https, or http when the host is on loopback, then a host that
// [CheckHost] takes, and no user, path, query or fragment. Its error starts with the fix,
// and the address is on the second line.
func CheckInstance(address string) error {
	u, err := url.Parse(address)
	if err == nil && (u.Scheme == "https" || u.Scheme == "http" && loopback(u.Hostname())) && u.Host != "" &&
		CheckHost(u.Host) == nil && u.User == nil && u.Path == "" && u.Opaque == "" && !strings.ContainsAny(address, "?#") {
		return nil
	}
	return fmt.Errorf("Set gateway.server.url in forager.yaml to an https URL of a host and an optional port alone\nThe address is %q.", address)
}

// RegistryOf is the registry of image, an image name such as pool.registry: its first
// part, with its port, when it is a host, one with a dot or a colon, or localhost; empty
// when the first part is no host. A first part that is a host but fails [CheckHost] is an
// error.
func RegistryOf(image string) (string, error) {
	host, rest, ok := strings.Cut(image, "/")
	if !ok || rest == "" || !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "", nil
	}
	if err := CheckHost(host); err != nil {
		return "", err
	}
	return host, nil
}

// RegistryHost is the registry of image as [RegistryOf] has it; empty for an image with
// no host, and for a host that [CheckHost] refuses.
func RegistryHost(image string) string {
	host, _ := RegistryOf(image)
	return host
}
