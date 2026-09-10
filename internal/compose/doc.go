// Package compose turns a profile into one flat tree: every entry once, from one layer.
//
// A profile, [profile.Profile], names an ordered list of layers and a target runtime, or a
// base profile to take both from: [LoadBase] fetches the base and puts its layers first,
// closed, and [ComposeWith] refuses what an appended layer ships beyond the base's
// extending block and any entry colliding with the base's. A
// layer is one directory of harness material. An entry is one atomic thing a layer ships,
// identified by kind and name; the kinds are skills, agents, commands, output-styles, hooks
// and mcp. A layer may also ship an AGENTS.md instruction file and settings fragments, and
// those merge across layers instead of colliding. [Compose] returns a [Result]: the layers
// with their pins and variants, one [Entry] per kind and name, every [Exclude] applied, the
// merged settings, the MCP servers and the joined instructions.
//
// # Composition order
//
// Compose walks the profile's layers in order and, for each one:
//
//  1. Resolves the source to a directory and a pin with [source.Resolve]: a path as it
//     stands, or a git ref fetched once into the cache and pinned by its commit.
//  2. Reads the layer's harness-layer.yaml with [layer.ReadManifest], which names the
//     layer; a directory without one is not a layer. An entry naming the layer must
//     name it as the manifest does, and a name composes once.
//  3. Picks the variant with [layer.SelectVariant]: the profile's forced variant, else the
//     one named like the target runtime, else the manifest's default, else the layer root.
//  4. Reads the entries, the settings fragments, the MCP servers and AGENTS.md with
//     [layer.Read]. A variant redirects entry kinds only; settings and AGENTS.md come from
//     the layer root.
//  5. Applies the layer's excludes. An exclude that names nothing the layer ships fails the
//     compose, so a layer that stops shipping an entry is noticed.
//  6. Merges the layer's settings fragments into the result, appends its AGENTS.md, and
//     adds the variables its manifest exports, each as
//     $QORY_HARNESS_HOME/layers/<name>/<path>. Two layers exporting one name with
//     different values fail the compose, unless [Options.Env] names it; the
//     configuration's variables are written over the layers' at the end.
//
// After the last layer, Compose refuses a collision: a kind and name that more than one
// layer still provides fails with a [*CollisionError]. There is no last-wins, no rename and
// no precedence by layer order. Excludes are applied per layer before the check, so an
// exclude is the only way to resolve a collision. Compose then sorts the entries by kind
// and name, and joins the instruction files with a blank line between them and a trailing
// newline.
//
// The first layer that fails ends the compose. Compose returns that error and no result.
//
// # Merging settings
//
// A fragment is settings/<runtime>/<file>, where <file> is the target file that runtime
// reads and its extension decides the format. Compose reads .json and .toml and refuses any
// other extension. A TOML document is decoded into the map and list types the JSON decoder
// produces, so both formats merge under the same rules. Fragments for the same runtime and
// the same target file merge in layer order:
//
//   - Maps merge by key, at every depth.
//   - A list under the top-level key permissions concatenates and drops duplicates.
//   - A list under the top-level key hooks concatenates.
//   - Every other list, and every scalar, is one value: the first layer sets it and a
//     later layer may repeat it. A later layer setting it to a different value, or to a
//     value of another shape, fails the compose with
//     settings/<runtime>/<file>: <dotted.key.path> is set by layers <a> and <b> with
//     different values. Values are compared with reflect.DeepEqual after decoding.
//   - A key of the top-level env map that [Options.Env] names takes the configuration's
//     value, whatever the fragments say, and never collides. An env key a fragment sets
//     and a layer manifest exports with a different value fails the compose after the
//     last layer with settings/<runtime>/<file>: env.<NAME> is set by layer <a> and
//     exported by layer <b> with different values, unless [Options.Env] names it.
//   - Two list elements count as duplicates when they print the same, so the string "1" and
//     the number 1 are one permission.
//
// The list rule follows its top-level key into the whole subtree under it: every list under
// permissions drops duplicates and every list under hooks concatenates, however deep.
//
// # $QORY_HARNESS_HOME
//
// A settings fragment names a hook script as $QORY_HARNESS_HOME/hooks/<file>, and a layer's
// other files as $QORY_HARNESS_HOME/layers/<name>/<path>. Merging keeps that text as
// written, which is what the report shows. [Result.SettingsFor], [Result.MCPFor] and
// [Result.EnvFor] replace every occurrence of the literal $QORY_HARNESS_HOME, braced or not, inside a
// string with the home path, at any depth, and return a copy the caller owns.
// [Result.Settings] and [Result.MCP] are shared: a caller reads them and does not write to
// them.
//
// # Using it
//
// A caller loads a profile, composes it, and matches the collision with errors.As:
//
//	p, err := profile.Load(profile.FileName)
//	if err != nil {
//		return err
//	}
//	res, err := compose.Compose(p)
//	var collision *compose.CollisionError
//	if errors.As(err, &collision) {
//		// collision.Collisions names the layers, collision.Suggest the excludes.
//	}
package compose
