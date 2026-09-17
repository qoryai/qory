// Package stack reads qory-stack.yaml.
//
// A stack names the ordered modules the harness is composed from, and, under extending,
// what a checkout extending it may add and which targets it is written for. It carries
// no target: a stack is delivered to be extended, and the checkout extending it sets
// the runtime and the model it composes for. A checkout's qory.yaml names, under
// harness, the stack it extends, the target it composes it for, and the modules it
// appends, or its own stack, a target and modules; the
// configuration package reads that and builds the same Stack with [NewCompose]. Each module carries a name, a source,
// a directory, a git repository at a ref, or a module the repository exports by name
// in the exports section of its qory.yaml, an optional forced variant, an optional link, a name at the checkout root
// for the module's directory, and an optional exclude: per kind, the entry names the
// compose leaves out. The stack may carry extensions, values of any shape qory writes into the
// report and does not read. The module order is the merge order of settings fragments and
// instruction sections, so it is kept as written.
//
// A caller discovers the file, then loads it:
//
//	p, err := stack.Load(file)
//
// [Load] refuses an unknown field, a second document in the file, an apiVersion other than
// [APIVersion] or a retired spelling of it, a stack with a target or with extends, a checkout's qory.yaml with an
// extending block, or without extends and without target.runtime, an empty module list, a
// module without a name or with a name that is not one path segment, two modules with the
// same name, a path source without its path, a git source without its ref or with a path
// leaving the repository, a ref without a git source, a source naming both a path
// inside a git repository and an export, or both a module and a stack, a module's source
// naming a stack and an extends naming a module, an exclude that names a kind
// outside [Kinds], a link that is not one path segment or is named by two modules, an
// extension that is not a map, and an extending.target naming nothing, an empty name or
// a name twice. It reads one file and nothing else, except the qory key of the qory.yaml
// at the root of the repository the stack is in, which [Stack.Qory] joins with the
// stack's own: a module's source directory is
// resolved by
// [github.com/qoryai/qory/internal/source] and read by
// [github.com/qoryai/qory/internal/module].
//
// [Stack.File] is the absolute path the stack was read from, and a module's relative path
// source resolves against [Stack.Dir], not against the caller's working directory.
package stack
