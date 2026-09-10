// Package render writes a composed harness into a checkout the way one runtime reads it.
//
// A [Runtime] renders for one program that runs the harness, such as Claude Code or Codex
// CLI, and its Name is the stack's target.runtime value. Each runtime is a package
// under internal/render that calls [Register] from its init, so the command module decides
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
// runtime shares at its root, AGENTS.md, skills/, hooks/ and one modules/<name> link per
// module to the module's own directory, calls the runtime's Render for a subdirectory named
// after the runtime, then removes the old home and renames the staging directory over it.
// Render is handed both paths because the two differ while it runs: it writes files into
// the staging directory, and a settings file it writes names the home, the path the
// runtime will read the tree from.
//
// # The links
//
// [LinkInto] gives the checkout the paths the program looks at, relative symlinks so a
// checkout that moves keeps them valid. Every link and the .qory directory go into the
// clone-local exclude file, .git/info/exclude, never the repository's own ignore file. A
// [Link] to a file is one symlink; a link to a directory, such as .claude, is a real
// directory holding one symlink per entry of the home's directory, so the checkout's own
// files in it stay, Claude Code's settings.local.json among them. A link is hard or soft:
//
//   - A hard link, such as .claude/settings.json, is the only thing that may stand at its
//     path. A file or a symlink pointing outside .qory there fails the render with a
//     [*ForeignPathError], because qory replaces nothing it did not write.
//   - A soft link, such as AGENTS.md, yields. Anything qory did not write at that path is
//     left as it is and returned as skipped, so a repository's own instructions stay its
//     own and the compose can report the path it did not link.
//
// Under force, a path git could restore, tracked and unmodified, is removed for either
// kind of link and returned as replaced; anything else is still refused. LinkInto also
// takes back what an earlier compose linked and this one no longer asks for.
//
// [Unlink] takes the links back. It removes the runtime's links that qory wrote, which
// are relative symlinks into .qory, and returns their declared paths; a link another
// runtime it is told about also declares is left for that one. It refuses a path
// that is anything else, and for a soft link it passes over such a path in silence, which
// is how a checkout's own AGENTS.md survives a remove. Inside a linked directory only the
// symlinks into .qory go. A parent directory the links left empty, such as .agents, is
// removed too. Unlink leaves two things behind: the home, which the caller removes, and
// the exclude lines, because worktrees of one repository share one exclude file and a
// line without its link is harmless.
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
//   - [PutServers] merges the composed MCP servers into a settings map under the key the
//     runtime reads them from.
//   - [EncodeTOML] encodes a settings map as TOML, for a runtime that reads TOML.
//   - [WriteJSON] and [WriteFile] write one file under a directory and create the parents.
//
// [Skipped] names the composed entries of the kinds the runtime's Skips reports no place
// for, which the compose prints so nothing disappears unannounced.
//
// A runtime must return its full set of links from a nil [compose.Result], because
// [Unlink] runs without a compose.
//
// # Files
//
// A files entry is a file a module ships as it is, named <runtime>/<path> after its place
// in the module, files/<runtime>/<path>. Build links it at <path> under the runtime's
// directory in the home once Render has written the runtime's own files, so a runtime
// whose directory is linked whole reaches it through that link: .claude/rules becomes a
// directory link like .claude/skills, and .claude/rules/nextjs-15.md resolves through it.
// Copilot, whose .github is the repository's, adds one soft link per file to its Links.
// A runtime with no directory in the checkout, goose and any, names the files kind in
// its Skips, and [Skipped] lists a files entry only for the runtime it names.
//
// Every runtime reserves the paths under its directory that a files entry may not take,
// [Runtime.Reserved]: the files Render writes, the files the program reads as its own
// settings, which a settings fragment sets, and the kind directories. [CheckFiles]
// refuses a files entry at a reserved path, or one whose runtime segment names no
// registered runtime, for every runtime in the result and not only the target, and the
// command module runs it right after the compose. [FileFor] is the check a runtime makes
// for an entry of its own.
package render
