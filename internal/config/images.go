package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/qoryai/runner/session"
	"gopkg.in/yaml.v3"
)

// RunnerImage is one entry of wall.images: an image this machine defines by name. A
// run's policy selects one by that name and names no reference, so a repository never
// chooses what it runs under; wall.image and --image name the default by it, or by a
// reference.
type RunnerImage struct {
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
	// without privileges. Experimental: the runner contract's §The wall says why.
	Docker bool
}

// Session is the definition as the runner takes it.
func (i RunnerImage) Session() session.Image {
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
// order. It refuses what the runner would refuse before a run, a name defined twice and a
// daemon without a runtime among them, so qory config shows it too; which runtimes the
// engine has is the engine's to say when a run starts.
func readImages(path string, node *yaml.Node) ([]RunnerImage, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: wall.images is a mapping from a name to an image", path)
	}
	var out []RunnerImage
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := node.Content[i].Value
		if !imageName.MatchString(name) {
			return nil, fmt.Errorf("%s: wall.images: %q is not 1 to 64 of a-z, 0-9, underscore, dot and dash, starting with a letter or a digit", path, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: wall.images.%s is defined twice", path, name)
		}
		seen[name] = true
		fail := func(format string, a ...any) error {
			return fmt.Errorf("%s: wall.images.%s%s", path, name, fmt.Sprintf(format, a...))
		}
		entry := node.Content[i+1]
		if entry.Kind != yaml.MappingNode {
			return nil, fail(" is a mapping: ref, and runtime and docker when it needs them")
		}
		img := RunnerImage{Name: name}
		var hasRef bool
		keys := map[string]bool{}
		for k := 0; k+1 < len(entry.Content); k += 2 {
			key, value := entry.Content[k].Value, entry.Content[k+1]
			if !slices.Contains(imageKeys, key) {
				return nil, fail(": key %q is not one; an image has %s", key, strings.Join(imageKeys, ", "))
			}
			if keys[key] {
				return nil, fail(".%s appears twice", key)
			}
			keys[key] = true
			switch key {
			case "ref":
				if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || !imageRef.MatchString(value.Value) {
					return nil, fail(".ref %q is not an image reference, such as ghcr.io/acme/agent@sha256:…", value.Value)
				}
				img.Ref, hasRef = value.Value, true
			case "runtime":
				if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || !runtimeName.MatchString(value.Value) {
					return nil, fail(".runtime %q is not a container runtime's name, such as sysbox-runc", value.Value)
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

// Defines reports whether the wall section defines an image of that name: one a policy
// may select, and one wall.image and --image read as that image before a reference.
func (w *RunnerWall) Defines(name string) bool {
	return w != nil && slices.ContainsFunc(w.Images, func(i RunnerImage) bool { return i.Name == name })
}

// imageRow is an image's value in the rows: its reference, then its runtime and its
// daemon when it has them.
func imageRow(i RunnerImage) string {
	v := i.Ref
	if i.Runtime != "" {
		v += ", runtime " + i.Runtime
	}
	if i.Docker {
		v += ", docker (experimental)"
	}
	return v
}
