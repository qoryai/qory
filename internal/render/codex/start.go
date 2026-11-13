package codex

import (
	"path"
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
// wall is the limit of what the agent changes. The flag is left out when args have no
// exec, an interactive start, and when any argument before -- sets a sandbox mode, in
// the forms --sandbox, --sandbox=<mode>, -s, -s=<mode> and -s<mode>, bypasses one, or
// asks for --approve-for-me or its alias --not-so-yolo, which Codex refuses beside
// --sandbox (codex-rs/utils/cli/src/shared_options.rs).
//
// The last argument after exec, the prompt, mentions the skill of [HeadlessArgs] by its
// path in the home in the wall when it starts with $<name>: [$<name>](<path>), with
// <path> HomeInWall/skills/<name>/SKILL.md. Codex selects a skill mentioned by path by
// that path alone, and a skill mentioned by $<name> by the first enabled skill of that
// name, which may be one of the repository's (codex-rs/ext/skills/src/selection.rs).
func (codex) WalledArgs(args []string) []string {
	i := slices.Index(args, "exec")
	if i < 0 {
		return args
	}
	args = slices.Clone(args)
	if n := len(args) - 1; n > i {
		args[n] = mentionByPath(args[n])
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

// mentionByPath is prompt with a leading $<name> written as [$<name>](<path>), the path
// of the skill <name> in the home in the wall, and prompt as it is otherwise. A name is
// one or more of the characters Codex reads in a mention, A-Z, a-z, 0-9, _ and -, and
// the colon of a plugin's name, which has no folder of its own in the home, is none.
func mentionByPath(prompt string) string {
	rest, ok := strings.CutPrefix(prompt, "$")
	if !ok {
		return prompt
	}
	end := strings.IndexFunc(rest, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	})
	if end < 0 {
		end = len(rest)
	}
	if end == 0 || end < len(rest) && rest[end] != ' ' {
		return prompt
	}
	name := rest[:end]
	return "[$" + name + "](" + path.Join(HomeInWall, "skills", name, "SKILL.md") + ")" + rest[end:]
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
