// Package compose turns a stack into one flat tree: every entry once, from one module.
//
// A stack, [stack.Stack], names an ordered list of modules and a target runtime. A compose
// file names a base stack to take both from: [LoadBase] fetches the base and puts its
// modules first, closed, and [ComposeWith] refuses what an appended module ships beyond the
// base's extending block and any entry colliding with the base's. A module is one directory
// of harness material. An entry is one atomic thing a module ships, identified by kind and
// name; the kinds are skills, agents, commands, output-styles, hooks, mcp and files. A module may also ship an AGENTS.md instruction file and settings fragments, and
// those merge across modules instead of colliding. [Compose] returns a [Result]: the modules
// with their pins and variants, one [Entry] per kind and name, every [Exclude] applied, the
// merged settings, the MCP servers and the joined instructions.
//
// # Composition order
//
// Compose walks the stack's modules in order and, for each one:
//
//  1. Resolves the source to a directory and a pin with [source.Resolve]: a path as it
//     stands, or a git ref fetched once into the cache and pinned by its commit.
//  2. Reads the module's qory-module.yaml with [module.ReadManifest], which names the
//     module; a directory without one is not a module. An entry naming the module must
//     name it as the manifest does, and a name composes once.
//  3. Picks the variant with [module.SelectVariant]: the stack's forced variant, else the
//     one named like the target runtime, else the manifest's default, else the module root.
//  4. Reads the entries, the settings fragments, the MCP servers and AGENTS.md with
//     [module.Read]. A variant redirects entry kinds only; settings and AGENTS.md come from
//     the module root.
//  5. Applies the module's excludes. An exclude that names nothing the module ships fails the
//     compose, so a module that stops shipping an entry is noticed.
//  6. Merges the module's settings fragments into the result, appends its AGENTS.md, and
//     adds the variables its manifest exports, each as
//     $QORY_HARNESS_HOME/modules/<name>/<path>. Two modules exporting one name with
//     different values fail the compose, unless [Options.Env] names it; the
//     configuration's variables are written over the modules' at the end.
//
// After the last module, Compose refuses a collision: a kind and name that more than one
// module still provides fails with a [*CollisionError]. There is no last-wins, no rename and
// no precedence by module order. Excludes are applied per module before the check, so an
// exclude is the only way to resolve a collision. Compose then sorts the entries by kind
// and name, and joins the instruction files with a blank line between them and a trailing
// newline.
//
// The first module that fails ends the compose. Compose returns that error and no result.
//
// # Merging settings
//
// A fragment is settings/<runtime>/<file>, where <file> is the target file that runtime
// reads and its extension decides the format. Compose reads .json and .toml and refuses any
// other extension. A TOML document is decoded into the map and list types the JSON decoder
// produces, so both formats merge under the same rules. Fragments for the same runtime and
// the same target file merge in module order:
//
//   - Maps merge by key, at every depth.
//   - A list under the top-level key permissions concatenates and drops duplicates.
//   - A list under the top-level key hooks concatenates.
//   - Every other list, and every scalar, is one value: the first module sets it and a
//     later module may repeat it. A later module setting it to a different value, or to a
//     value of another shape, fails the compose with
//     settings/<runtime>/<file>: <dotted.key.path> is set by modules <a> and <b> with
//     different values. Values are compared with reflect.DeepEqual after decoding.
//   - A key of the top-level env map that [Options.Env] names takes the configuration's
//     value, whatever the fragments say, and never collides. An env key a fragment sets
//     and a module manifest exports with a different value fails the compose after the
//     last module with settings/<runtime>/<file>: env.<NAME> is set by module <a> and
//     exported by module <b> with different values, unless [Options.Env] names it.
//   - Two list elements count as duplicates when they print the same, so the string "1" and
//     the number 1 are one permission.
//
// The list rule follows its top-level key into the whole subtree under it: every list under
// permissions drops duplicates and every list under hooks concatenates, however deep.
//
// # $QORY_HARNESS_HOME
//
// A settings fragment names a hook script as $QORY_HARNESS_HOME/hooks/<file>, and a module's
// other files as $QORY_HARNESS_HOME/modules/<name>/<path>. Merging keeps that text as
// written, which is what the report shows. [Result.SettingsFor], [Result.MCPFor] and
// [Result.EnvFor] replace every occurrence of the literal $QORY_HARNESS_HOME, braced or not, inside a
// string with the home path, at any depth, and return a copy the caller owns.
// [Result.Settings] and [Result.MCP] are shared: a caller reads them and does not write to
// them.
//
// # Using it
//
// A caller loads a stack, composes it, and matches the collision with errors.As:
//
//	p, err := stack.Load(stack.FileName)
//	if err != nil {
//		return err
//	}
//	res, err := compose.Compose(p)
//	var collision *compose.CollisionError
//	if errors.As(err, &collision) {
//		// collision.Collisions names the modules, collision.Suggest the excludes.
//	}
package compose
