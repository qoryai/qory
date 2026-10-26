package codex

// HeadlessArgs are codex exec: the session's events as JSON lines on stdout, in added
// to the folders Codex writes, and the prompt $<name> <arg>, which names a skill. Codex
// renders no command, so a command has no form.
// Source: codex exec --help and https://developers.openai.com/codex/noninteractive, codex-cli 0.162.1.
func (codex) HeadlessArgs(kind, name, arg, in string) ([]string, bool) {
	if kind != "skills" {
		return nil, false
	}
	return []string{"exec", "--json", "--add-dir", in, "$" + name + " " + arg}, true
}
