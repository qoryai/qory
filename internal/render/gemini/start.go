package gemini

// HeadlessArgs are Gemini CLI's prompt mode: the session's events as JSON lines on
// stdout, in added to the folders Gemini CLI reads, and the prompt /<name> <arg>, which
// runs a command. A headless session loads no skill as a command, so a skill has no form.
// Source: gemini --help and https://geminicli.com/docs/cli/headless, Gemini CLI 0.59.0.
func (gemini) HeadlessArgs(kind, name, arg, in string) ([]string, bool) {
	if kind != "commands" {
		return nil, false
	}
	return []string{"--output-format", "stream-json", "--include-directories", in, "-p", "/" + name + " " + arg}, true
}
