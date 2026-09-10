// Package render writes a composed harness into a checkout the way one runtime reads it.
//
// A [Runtime] renders for one program that runs the harness, such as Claude Code or Codex
// CLI, and its Name is the profile's target.runtime value. Each runtime is a package
// under internal/render that calls [Register] from its init, so the command layer decides
// which runtimes a binary knows by importing them for their side effect alone:
//
//	import _ "github.com/qoryai/qory/internal/render/claude"
//
// [Lookup] finds a registered runtime by name and [Names] lists them all, which is what
// an unknown target.runtime reports and what the --runtime flag offers.
//
// # The home
//
// The home is the composed tree at .qory/harness inside the checkout. [Build] writes it
// in one step that either lands whole or leaves the previous home untouched: it stages
// the tree in a sibling directory, home with ".tmp" appended, puts the parts every
// runtime shares at its root, calls the runtime's Render for a subdirectory named after
// the runtime, then removes the old home and renames the staging directory over it.
// Render is handed both paths because the two differ while it runs: it writes files into
// the staging directory, and a settings file it writes names the home, the path the
// runtime will read the tree from.
//
// # The links
//
// [LinkInto] gives the checkout the paths the program looks at, one relative symlink per
// [Link] the runtime asks for, so a checkout that moves keeps them valid. Every link and
// the .qory directory go into the clone-local exclude file, .git/info/exclude, never the
// repository's own ignore file. A link is hard or soft:
//
//   - A hard link, such as .claude, is the only thing that may stand at its path. A real
//     directory or a symlink pointing outside .qory there fails the render, because qory
//     replaces nothing it did not write.
//   - A soft link, such as AGENTS.md, yields. Anything qory did not write at that path is
//     left as it is and returned as skipped, so a repository's own instructions stay its
//     own and the compose can report the path it did not link.
//
// [Unlink] takes the links back. It removes the runtime's link paths that qory wrote,
// which are relative symlinks into .qory, and returns them. It refuses a path that is
// anything else, and for a soft link it passes over such a path in silence, which is how
// a checkout's own AGENTS.md survives a remove. A parent directory the links left empty,
// such as .agents, is removed too. Unlink leaves two things behind: the home, which the
// caller removes, and the exclude lines, because worktrees of one repository share one
// exclude file and a line without its link is harmless.
//
// # Helpers for a runtime package
//
// A Render implementation is assembled from these:
//
//   - [LinkEntries] symlinks the atomic entries of the kinds it names into the runtime's
//     directory, one link per entry.
//   - [WriteSettings] writes the runtime's merged settings files, JSON or TOML by
//     extension, handing each to a patch function, which is where the target model goes.
//   - [WriteAgents] writes one Markdown file per composed agent, keeping the frontmatter
//     keys the runtime reads.
//   - [EncodeTOML] encodes a settings map as TOML, for a runtime that reads TOML.
//   - [WriteFile] writes one file under a directory and creates the parents.
//
// [Skipped] names the composed entries of the kinds the runtime's Skips reports no place
// for, which the compose prints so nothing disappears unannounced.
//
// A runtime must return its full set of links from a nil [compose.Result], because
// [Unlink] runs without a compose.
package render
