package reviewfiles

import (
	"bytes"

	"github.com/BurntSushi/toml"
)

// codexServer is one table of mcp_servers in Codex's config.toml.
type codexServer struct {
	Command      string   `toml:"command"`
	Args         []string `toml:"args"`
	EnabledTools []string `toml:"enabled_tools"`
}

// CodexConfig is the table [mcp_servers.qory_review] of a Codex config.toml that starts the
// server on the copy at root with command, the path of the qory binary in the reviewer's
// container, as qory review-files <root>. Its enabled_tools lists the three tools by their
// bare names, so Codex offers no other tool of the server.
func CodexConfig(command, root string) (string, error) {
	var b bytes.Buffer
	b.WriteString("[mcp_servers." + ServerID + "]\n")
	server := codexServer{
		Command:      command,
		Args:         []string{"review-files", root},
		EnabledTools: append([]string(nil), ToolNames...),
	}
	if err := toml.NewEncoder(&b).Encode(server); err != nil {
		return "", err
	}
	return b.String(), nil
}
