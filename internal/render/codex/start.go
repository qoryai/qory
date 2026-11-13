package codex

import (
	"slices"
	"strings"
)

// HeadlessArgs are codex exec: the session's events as JSON lines on stdout, in added
// to the folders Codex writes, and the prompt $<name> <arg>, Codex's mention of a
// skill. Codex renders no command, so a command has no form.
// Source: codex exec --help and https://developers.openai.com/codex/noninteractive, codex-cli 0.162.1.
func (codex) HeadlessArgs(kind, name, arg, in string) ([]string, bool) {
	if kind != "skills" {
		return nil, false
	}
	return []string{"exec", "--json", "--add-dir", in, "$" + name + " " + arg}, true
}

// Install is Codex's npm package at version.
func (codex) Install(version string) string { return "@openai/codex@" + version }

// WalledArgs are args with --sandbox danger-full-access after exec, so Codex runs
// commands with no sandbox of its own: inside the wall, every other sandbox mode fails
// before it runs a command, since bubblewrap cannot create a namespace there, and the
// wall is the limit of what the agent changes. args are left as they are when they have
// no exec, an interactive start, and when any of them before -- sets a sandbox mode, in
// the forms --sandbox, --sandbox=<mode>, -s, -s=<mode> and -s<mode>, bypasses one, or
// asks for --approve-for-me or its alias --not-so-yolo, which Codex refuses beside
// --sandbox (codex-rs/utils/cli/src/shared_options.rs).
func (codex) WalledArgs(args []string) []string {
	i := slices.Index(args, "exec")
	if i < 0 {
		return args
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if setsSandbox(a) {
			return args
		}
	}
	return slices.Concat(args[:i+1], []string{"--sandbox", "danger-full-access"}, args[i+1:])
}

// setsSandbox reports whether the argument a sets Codex's sandbox mode, bypasses the
// sandbox, or asks for a mode that conflicts with --sandbox.
func setsSandbox(a string) bool {
	switch a {
	case "--sandbox", "--dangerously-bypass-approvals-and-sandbox", "--yolo", "--approve-for-me", "--not-so-yolo":
		return true
	}
	return strings.HasPrefix(a, "--sandbox=") || strings.HasPrefix(a, "-s") && !strings.HasPrefix(a, "--")
}
