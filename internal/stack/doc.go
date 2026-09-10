// Package stack reads qory-stack.yaml and qory-compose.yaml and finds the one that covers a
// checkout.
//
// A stack names the target runtime the harness is rendered for, the optional model, and
// the ordered modules the harness is composed from. A compose file names the stack it
// extends instead of a target, and the modules it appends. Each module carries a name, a source
// directory, an optional forced variant, an optional link, a name at the checkout root
// for the module's directory, and an optional exclude: per kind, the entry names the
// compose leaves out. The stack may carry extensions, maps qory writes into the report
// and does not read. The module order is the merge order of settings fragments and
// instruction sections, so it is kept as written.
//
// A caller discovers the file, then loads it:
//
//	file, err := stack.Discover(checkout)
//	p, err := stack.Load(file)
//
// [Load] refuses an unknown field, a second document in the file, an apiVersion other than
// [APIVersion], a stack without target.runtime or with extends, a compose file without extends or with a
// target or an extending block, an empty module list, a
// module without a name or with a name that is not one path segment, two modules with the
// same name, a path source without its path, a git source without its ref or with a path
// leaving the repository, a ref without a git source, an exclude that names a kind
// outside [Kinds], a link that is not one path segment or is named by two modules, and an
// extension that is not a map. It reads one file and nothing else: a module's source directory is
// resolved by
// [github.com/qoryai/qory/internal/source] and read by
// [github.com/qoryai/qory/internal/module].
//
// [Stack.File] is the absolute path the stack was read from, and a module's relative path
// source resolves against [Stack.Dir], not against the caller's working directory.
package stack
