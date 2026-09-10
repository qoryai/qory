// Package layer reads one directory of harness material: its optional manifest, the entries
// it ships per kind, its settings fragments and its instruction file.
//
// A layer's tree holds one directory per entry kind, skills, agents, commands,
// output-styles and hooks, the settings/<runtime>/<file> fragments, and AGENTS.md. An entry
// is one atomic thing the layer ships, identified by kind and name: a skill is a directory
// holding SKILL.md, an agent, a command and an output style are Markdown files named after
// the entry, a hook is a regular file under hooks. The manifest, harness.yaml, declares the
// layer's name and its variants, the per-runtime alternatives, each one reading some kinds
// from another directory inside the layer. A directory without a manifest is a layer with
// one variant, read from its root.
//
// A caller reads the manifest, picks the variant for the target runtime, then scans the
// tree:
//
//	m, err := layer.ReadManifest(dir)
//	variant, err := layer.SelectVariant(m, forced, runtime)
//	l, err := layer.Read(name, dir, m, variant)
//
// [Read] takes the variant [SelectVariant] returned for that same manifest. It reads the
// kinds the variant redirects from the directories the variant names and every other kind
// from the directory named like the kind. A kind directory that is not there is not an
// error: the layer ships no entry of that kind. Entries come back sorted by kind, then by
// name.
//
// [ReadDocument] parses one of the Markdown entries into its frontmatter and its body, for
// the renderers that rewrite an entry into a runtime's own format.
//
// This package reads one layer and judges nothing across layers. The excludes and the
// collision check are [github.com/qoryai/qory/internal/compose]'s work.
package layer
