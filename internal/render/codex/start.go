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

// WalledArgs are args with --sandbox danger-full-access after exec, so Codex runs
// commands with no sandbox of its own: inside the wall, every other sandbox mode fails
// before it runs a command, since bubblewrap cannot create a namespace there, and the
// wall is the limit of what the agent changes. args without exec, an interactive start,
// and args that set a sandbox mode or bypass one after exec are left as they are.
func (codex) WalledArgs(args []string) []string {
	i := slices.Index(args, "exec")
	if i < 0 {
		return args
	}
	for _, a := range args[i+1:] {
		if a == "--" {
			break
		}
		if a == "-s" || a == "--sandbox" || strings.HasPrefix(a, "--sandbox=") || strings.HasPrefix(a, "-s=") ||
			a == "--dangerously-bypass-approvals-and-sandbox" || a == "--yolo" {
			return args
		}
	}
	return slices.Concat(args[:i+1], []string{"--sandbox", "danger-full-access"}, args[i+1:])
}
