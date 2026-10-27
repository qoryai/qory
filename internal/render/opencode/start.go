package opencode

// HeadlessArgs are opencode run: the session's events as JSON lines on stdout, and the
// command name with arg as its arguments. OpenCode has no flag that adds a folder, so in
// is not passed, and it runs no skill by its name, so a skill has no form.
// Source: opencode run --help and https://opencode.ai/docs/cli/, OpenCode 1.18.31.
func (opencode) HeadlessArgs(kind, name, arg, in string) ([]string, bool) {
	if kind != "commands" {
		return nil, false
	}
	return []string{"run", "--format", "json", "--command", name, arg}, true
}
