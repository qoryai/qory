package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/policy"
	"github.com/qoryai/forager/session"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
)

// ForagerImage is one entry of wall.images: an image this machine defines by name. A
// run's policy selects one by that name, or names a reference under one of
// wall.images_allow, so a repository chooses what it runs under only among what the
// machine allows; wall.image and --image name the default by it, or by a reference.
type ForagerImage struct {
	// Name is what a policy, wall.image and --image select the image by, in a
	// credential's grammar.
	Name string
	// Ref is the image's reference, pinned by digest where the machine wants the same
	// image every time.
	Ref string
	// Runtime is the container runtime the wall starts the image under, one the
	// engine has, such as sysbox-runc; empty is the engine's default.
	Runtime string
	// Docker gives the agent a Docker daemon of its own inside the container, which
	// qory's helper starts before the agent. It needs a Runtime that runs a daemon
	// without privileges. Experimental: the Forager contract's §The wall says why.
	Docker bool
}

// Session is the definition as Forager takes it.
func (i ForagerImage) Session() session.Image {
	return session.Image{Name: i.Name, Ref: i.Ref, Runtime: i.Runtime, Docker: i.Docker}
}

// imageName is the grammar of an image's name, a credential's: what a policy selects it
// by.
var imageName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// imageRef and runtimeName are the shapes the wall takes on a command line for an image's
// reference and a container runtime's name, checked here so the file says what is wrong
// before a run does.
var (
	imageRef    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
	runtimeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// imageKeys are the keys of an entry of wall.images.
var imageKeys = []string{"ref", "runtime", "docker"}

// readImages reads wall.images, a mapping from a name to a definition, in the file's
// order. It refuses what Forager would refuse before a run, a name defined twice and a
// daemon without a runtime among them, so qory config shows it too; which runtimes the
// engine has is the engine's to say when a run starts.
func readImages(path string, node *yaml.Node) ([]ForagerImage, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: wall.images is a mapping from a name to an image", path)
	}
	var out []ForagerImage
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		at := entryAt("wall.images", node.Content[i])
		if !imageName.MatchString(name) {
			return nil, fmt.Errorf("%s: wall.images: %s is not 1 to 64 of a-z, 0-9, underscore, dot and dash, starting with a letter or a digit", path, quotedName(node.Content[i]))
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: %s is defined twice", path, at)
		}
		seen[name] = true
		fail := func(format string, a ...any) error {
			return fmt.Errorf("%s: %s%s", path, at, fmt.Sprintf(format, a...))
		}
		entry := node.Content[i+1]
		if entry.Kind != yaml.MappingNode {
			return nil, fail(" is a mapping: ref, and runtime and docker when it needs them")
		}
		img := ForagerImage{Name: name}
		var hasRef bool
		keys := map[string]bool{}
		for k := 0; k+1 < len(entry.Content); k += 2 {
			key, value := entry.Content[k].Value, entry.Content[k+1]
			if !slices.Contains(imageKeys, key) {
				return nil, keyNotOne(path, at, entry.Content[k], "; an image has "+strings.Join(imageKeys, ", "))
			}
			if keys[key] {
				return nil, fail(".%s appears twice", key)
			}
			keys[key] = true
			switch key {
			case "ref":
				if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || !imageRef.MatchString(value.Value) {
					return nil, fail(".ref is not an image reference, such as ghcr.io/acme/agent@sha256:…")
				}
				img.Ref, hasRef = value.Value, true
			case "runtime":
				if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || !runtimeName.MatchString(value.Value) {
					return nil, fail(".runtime is not a container runtime's name, such as sysbox-runc")
				}
				img.Runtime = value.Value
			case "docker":
				if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!bool" || value.Decode(&img.Docker) != nil {
					return nil, fail(".docker is true or false")
				}
			}
		}
		if !hasRef {
			return nil, fail(".ref is required: the image's reference")
		}
		if img.Docker && img.Runtime == "" {
			return nil, fail(": docker needs a runtime that runs a daemon without privileges, such as runtime: sysbox-runc")
		}
		if err := img.Session().Check(); err != nil {
			return nil, fail(": %v", err)
		}
		out = append(out, img)
	}
	return out, nil
}

// readImagesAllow reads wall.images_allow, a list of the prefixes of the references
// pinned by digest a run's policy may name its image by. It refuses an entry Forager
// would refuse, by its index and without its value: an empty one, *, and one that is
// not the start of such a reference in lower case, up to a whole part of it.
func readImagesAllow(path string, node *yaml.Node) ([]string, error) {
	notPrefix := func(i, _ int) error {
		return fmt.Errorf("%s: wall.images_allow[%d] is not the start of an image reference pinned by digest, in lower case, up to a whole part of it: a registry, a name or a path ending in a slash, or a name and its @", path, i)
	}
	allow, err := stringList(path, "wall.images_allow", node, notPrefix)
	if err != nil {
		return nil, err
	}
	for i, entry := range allow {
		if policy.CheckImagePrefix(entry) != nil {
			return nil, notPrefix(i, 0)
		}
	}
	return allow, nil
}

// Defines reports whether the wall section defines an image of that name: one a policy
// may select, and one wall.image and --image read as that image before a reference.
func (w *ForagerWall) Defines(name string) bool {
	return w != nil && slices.ContainsFunc(w.Images, func(i ForagerImage) bool { return i.Name == name })
}

// defaultRow is wall.image's value in the rows: the value and what it is read as, the
// image of wall.images it names or a reference, so a name mistyped shows as a
// reference. A reference prints as [exports.Value] prints it.
func defaultRow(w *ForagerWall) string {
	switch {
	case w.Image == "":
		return "(none)"
	case w.Defines(w.Image):
		return accesskey.ShownName(w.Image, false) + " (wall.images." + accesskey.ShownName(w.Image, false) + ")"
	}
	return exports.Value(w.Image, false) + " (a reference)"
}

// imageRow is an image's value in the rows: its reference, then its runtime and its
// daemon when it has them, the reference and the runtime as [exports.Value] prints them.
func imageRow(i ForagerImage) string {
	v := exports.Value(i.Ref, false)
	if i.Runtime != "" {
		v += ", runtime " + exports.Value(i.Runtime, false)
	}
	if i.Docker {
		v += ", docker (experimental)"
	}
	return v
}
