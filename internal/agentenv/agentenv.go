// Package agentenv names the environment variables that change what the agent does
// beyond what its run sets: its runtime's and qory's settings, its telemetry, its tool
// servers, and what a program or a shell loads before it starts. qory run prebuild
// refuses an image or a development container configuration that sets one, and a
// dispatched walled run of Claude Code drops the image's values of them, but for the
// gateway's placeholders. Both guard against the values an image or a configuration
// leaves in place, not against a program of the image or a command of the repository,
// which runs as the agent's user in the agent's container; against those, the boundary
// is the wall. This is the one list of them; whatever else reads them reads it here.
package agentenv

import "strings"

// Prefixes start the names of the variables of the agent's runtime, Anthropic's and
// Claude Code's, qory's, the tool servers' and OpenTelemetry's.
var Prefixes = []string{"ANTHROPIC_", "CLAUDE_", "QORY_", "MCP_", "OTEL_"}

// Names are the variables that load code into a program or a shell before it starts:
// node's options and module path, the dynamic linker's preload and audit libraries,
// and the files bash and sh read first.
var Names = []string{"NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "LD_AUDIT", "BASH_ENV", "ENV"}

// Widens reports whether name is one of [Names] or starts with one of [Prefixes],
// compared exactly, case included, as the environment compares names.
func Widens(name string) bool {
	for _, p := range Prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, n := range Names {
		if name == n {
			return true
		}
	}
	return false
}

// Widening is the names among names that [Widens] reports, in their order, each once.
func Widening(names []string) []string {
	var out []string
	for _, n := range names {
		if Widens(n) && !contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// contains reports whether names holds name.
func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
