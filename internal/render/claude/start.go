package claude

import "strings"

// emptyMCP is the MCP list a dispatched run of Claude Code is given when the harness has
// none: no server.
const emptyMCP = `{"mcpServers":{}}`

// HeadlessArgs are Claude Code's print mode: the session's events as JSON lines on
// stdout, in added to the folders Claude Code reads, and the prompt /<name> <arg>, the
// form of a skill and a command alike.
func (claude) HeadlessArgs(kind, name, arg, in string) ([]string, bool) {
	return []string{"--output-format", "stream-json", "--verbose", "--add-dir", in, "-p", "/" + name + " " + arg}, true
}

// DispatchedArgs are args with --strict-mcp-config, so Claude Code loads no MCP server
// but those of --mcp-config, --mcp-config with the harness's list, or an empty one
// when the harness has none, and --setting-sources user, which replaces any other value
// of it, so no settings of the checkout's .claude are read. The flags go before the
// arguments given after --, and the last is one that takes a single value, so
// --mcp-config, which takes several, never takes a word of those.
func (claude) DispatchedArgs(args []string) []string {
	out := make([]string, 0, len(args)+5)
	hasMCP := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--strict-mcp-config", strings.HasPrefix(a, "--setting-sources="):
			continue
		case a == "--setting-sources":
			i++
			continue
		case a == "--mcp-config", strings.HasPrefix(a, "--mcp-config="):
			hasMCP = true
		}
		out = append(out, a)
	}
	if !hasMCP {
		out = append(out, "--mcp-config", emptyMCP)
	}
	return append(out, "--strict-mcp-config", "--setting-sources", "user")
}

// SettingsFlag is --settings, which takes settings.json's path or its JSON.
func (claude) SettingsFlag() string { return "--settings" }

// AgentHomeVariable is CLAUDE_CONFIG_DIR: Claude Code reads its settings, CLAUDE.md,
// plugins, memory, .claude.json and transcripts from that folder.
func (claude) AgentHomeVariable() string { return "CLAUDE_CONFIG_DIR" }

// Install is Claude Code's npm package at version.
func (claude) Install(version string) string { return "@anthropic-ai/claude-code@" + version }
