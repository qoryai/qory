package cursor

// HeadlessArgs are the Cursor CLI's print mode: the session's events as JSON lines on
// stdout, in added to the workspace's folders, and the prompt /<name> <arg>, which runs a
// skill. Cursor renders no command, so a command has no form.
// Source: cursor-agent --help and https://cursor.com/docs/cli/headless, Cursor CLI 2026.09.10-fd3934a.
func (cursor) HeadlessArgs(kind, name, arg, in string) ([]string, bool) {
	if kind != "skills" {
		return nil, false
	}
	return []string{"--print", "--output-format", "stream-json", "--add-dir", in, "/" + name + " " + arg}, true
}
