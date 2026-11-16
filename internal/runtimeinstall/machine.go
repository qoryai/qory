package runtimeinstall

import (
	"path/filepath"
	"strings"
)

// Machine is where a run installs its runtime on a machine that a pool runs: the
// instance's registry, and the runtimes folder that the pool's runs share. qory run
// pool writes it after the run code, in the file it gives qory run's --run-code-file,
// as [Machine.Lines]; qory run reads it with [ParseMachine]. A field that is empty is
// left out.
type Machine struct {
	// Registry is the instance's registry, a host and a port or a host, see
	// [RegistryHost].
	Registry string
	// Folder is the runtimes folder, an absolute clean path.
	Folder string
}

// The keys of a Machine's lines.
const (
	machineRegistry = "registry"
	machineFolder   = "runtimes"
)

// Lines is m as lines of the run code's file: "registry <host>" and "runtimes
// <folder>", each with a newline, in that order.
func (m Machine) Lines() string {
	var b strings.Builder
	if m.Registry != "" {
		b.WriteString(machineRegistry + " " + m.Registry + "\n")
	}
	if m.Folder != "" {
		b.WriteString(machineFolder + " " + m.Folder + "\n")
	}
	return b.String()
}

// ParseMachine reads lines, the lines of the run code's file after the code, each
// "registry <host>" or "runtimes <folder>", each key at most once, with no control
// character; ok is false for any other line.
func ParseMachine(lines []string) (m Machine, ok bool) {
	for _, line := range lines {
		if strings.ContainsFunc(line, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return Machine{}, false
		}
		key, value, _ := strings.Cut(line, " ")
		switch {
		case key == machineRegistry && m.Registry == "" && value != "" && RegistryHost(value+"/x") == value:
			m.Registry = value
		case key == machineFolder && m.Folder == "" && filepath.IsAbs(value) && filepath.Clean(value) == value:
			m.Folder = value
		default:
			return Machine{}, false
		}
	}
	return m, true
}

// RegistryHost is the registry of image, an image name such as pool.registry: its first
// part, with its port, when it is a host, one with a dot or a colon, or localhost;
// empty otherwise.
func RegistryHost(image string) string {
	host, rest, ok := strings.Cut(image, "/")
	if !ok || rest == "" || strings.ContainsAny(host, " \t\r\n@") || !strings.ContainsAny(host, ".:") && host != "localhost" {
		return ""
	}
	return host
}
