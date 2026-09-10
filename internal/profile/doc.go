// Package profile reads harness-compose.yaml and finds the one that covers a checkout.
//
// A profile names the target runtime the harness is rendered for, the optional model, and
// the ordered layers the harness is composed from. Each layer carries a name, a source
// directory, an optional forced variant, and an optional exclude: per kind, the entry names
// the compose leaves out. The layer order is the merge order of settings fragments and
// instruction sections, so it is kept as written.
//
// A caller discovers the file, then loads it:
//
//	file, err := profile.Discover(checkout)
//	p, err := profile.Load(file)
//
// [Load] refuses an unknown field, an apiVersion other than [APIVersion], a kind other than
// [Kind], a missing target.runtime, an empty layer list, a layer without a name, two layers
// with the same name, a layer without source.path, and an exclude that names a kind outside
// [Kinds]. It reads one file and nothing else: a layer's source directory is resolved by
// [github.com/qoryai/qory/internal/source] and read by
// [github.com/qoryai/qory/internal/layer].
//
// [Profile.File] is the absolute path the profile was read from, and a layer's relative path
// source resolves against [Profile.Dir], not against the caller's working directory.
package profile
