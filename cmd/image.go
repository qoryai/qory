package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session/runtimes/catalog"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/image"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/ui"
)

// newImage builds the image noun: the images the wall runs the agent in.
func newImage() *cobra.Command {
	c := &cobra.Command{
		Use:   "image",
		Short: "Check an image the wall runs the agent in",
		Long: `Check an image the wall runs the agent in.

Four are defined under images/ in qory's repository, for linux/amd64 and linux/arm64:

  agent             Claude Code, git and gh
  agent-docker      agent, and nested Docker: a Docker daemon inside the wall, separate from the machine's
  agent-go          agent, and Go
  agent-go-docker   agent-docker, and Go

They are not published for you to pull. Build them from a checkout of qory at the commit qory version
prints, build yours FROM one of them, and check it with qory image check.`,
	}
	c.AddCommand(newImageCheck(), newImageProbe())
	return c
}

// newImageCheck builds the check verb, which checks each image it is given, or the
// machine's wall.image, against what the wall needs of it: from outside through the
// docker command, and from inside with qory's Linux build as the probe, the one the wall
// mounts as its helper. It prints a line per check and fails when one fails; it changes
// no image.
func newImageCheck() *cobra.Command {
	return &cobra.Command{
		Use:   "check [image...]",
		Short: "Check that an image has what the wall needs of it",
		Long: `Check that an image has what the wall needs of it. With no image, check wall.image
of ` + config.ForagerFileName + `. A name of wall.images is read as that image's ref first, as
qory run reads --image.

From outside, it reads the image the engine holds: its platform, HOME in its
environment, whether the reference is pinned by digest, and every file, for one that is
setuid or setgid or has file capabilities. Then it starts the image the way the
wall starts the agent, as a user the image does not know, with no capability and no
network, and qory's Linux build checks from inside:

  - HOME is a directory that user writes in
  - the system's authorities are where the wall reads them
  - /bin/sh is there, for the runtime's hooks and the session's API-key approval
  - each runtime Forager describes is the version its descriptor is written against
  - git and gh run
  - dockerd, for nested Docker, and what it runs

The Linux build is wall.helper, for the engine's architecture; on Linux, this binary
when wall.helper is not set. The docker command is wall.command, or docker.

The exit status is 0 when every image passes, and 1 when one fails.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#qorys-images`,
		Example: `  qory image check                                # wall.image of ` + config.ForagerFileName + `
  qory image check qory-agent
  qory image check go-docker                      # an image wall.images defines
  qory image check my-agent:1 my-agent-docker:1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			stop := buzzing(cmd)
			defer stop()
			r, err := config.LoadForager()
			if err != nil {
				return input(err)
			}
			var section config.ForagerWall
			if r != nil && r.Wall != nil {
				section = *r.Wall
			}
			names := args
			if len(names) == 0 {
				if section.Image == "" {
					return input(fmt.Errorf("name an image to check, or set wall.image in %s", config.ForagerFileName))
				}
				names = []string{section.Image}
			}
			refs := make([]string, len(names))
			for i, name := range names {
				refs[i] = imageRef(section, name)
				if err := image.CheckRef(refs[i]); err != nil {
					return input(exports.HideValueError(err, refs[i]))
				}
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			helper, err := wallHelper(section, exe)
			if err != nil {
				return err
			}
			arch, err := image.HelperArch(helper)
			if err != nil {
				return input(fmt.Errorf("qory's Linux build %s: %w", exports.Value(helper, false), exports.HideError(err)))
			}
			user, err := config.UserDir()
			if err != nil {
				return input(err)
			}
			listed, err := foragerRuntimes()
			if err != nil {
				return err
			}
			// Each runtime of Forager's list, at the version in its descriptor, a
			// descriptor in the machine's runtimes folder first, as a run reads it.
			probed := make([]image.Runtime, len(listed))
			for i, name := range listed {
				rt, err := catalog.Lookup(name, filepath.Join(user, DescriptorsDir))
				if err != nil {
					return input(err)
				}
				probed[i] = image.Runtime{Name: name, Version: rt.Version()}
			}
			engine := image.Engine{Command: section.Command}
			platform, err := engine.Platform(cmd.Context())
			if err != nil {
				return err
			}
			// The probe runs as the engine runs it, whatever the image's architecture.
			if want := image.Arch(platform); arch != want {
				return input(fmt.Errorf("qory's Linux build %s is for %s, and the engine runs %s; set wall.helper to the Linux build for %s", exports.Value(helper, false), arch, platform, want))
			}

			u := ui.New(cmd.OutOrStdout())
			u.Title("qory image check", "engine "+platform)
			fields := [][2]string{{"probe", exports.Value(ui.Short(helper, ""), false) + ", " + arch}}
			for _, rt := range probed {
				version := rt.Version + ", Forager's descriptor's"
				if rt.Version == "" {
					version = "any version; Forager's descriptor names none"
				}
				fields = append(fields, [2]string{rt.Name, version})
			}
			u.Fields(fields)
			var failed []string
			for i, ref := range refs {
				u.Blank()
				if names[i] == ref {
					u.Heading(shownImage(ref, ref))
				} else {
					u.Heading(shownImage(names[i], ref) + " (" + exports.Value(ref, false) + ")")
				}
				checks := engine.Check(cmd.Context(), platform, image.Target{Ref: ref, Helper: helper, Runtimes: probed})
				var facts [][2]string
				for _, c := range checks {
					switch c.Result {
					case image.Pass:
						u.Success("%s", exports.HideValue(c.Detail, ref))
					case image.Fail:
						u.Fail(errors.New(exports.HideValue(c.Detail, ref)))
					default:
						facts = append(facts, [2]string{c.Name, exports.HideValue(c.Detail, ref)})
					}
				}
				u.Fields(facts)
				if image.Failed(checks) {
					failed = append(failed, shownImage(names[i], refs[i]))
				}
			}
			u.Blank()
			switch {
			case len(failed) > 0 && len(names) == 1:
				err := fmt.Errorf("%s lacks what the wall needs", failed[0])
				u.Fail(err)
				return reported(err)
			case len(failed) > 0:
				err := fmt.Errorf("%d of %d images lack what the wall needs: %s", len(failed), len(names), strings.Join(failed, ", "))
				u.Fail(err)
				return reported(err)
			case len(names) == 1:
				u.Success("%s has what the wall needs %s", shownImage(names[0], refs[0]), ui.Pot)
			default:
				u.Success("%d images have what the wall needs %s", len(names), ui.Pot)
			}
			return nil
		},
	}
}

// shownImage is an image as qory image check names it: the reference, as
// [exports.Value] prints it, for an image named by its reference, ref, and the name of
// wall.images, as [accesskey.ShownName] prints it, for one named there.
func shownImage(name, ref string) string {
	if name == ref {
		return exports.Value(ref, false)
	}
	return accesskey.ShownName(name, false)
}

// imageRef is the reference qory image check reads name as: the ref of the image
// wall.images defines by that name, as qory run reads --image and wall.image, else name.
func imageRef(section config.ForagerWall, name string) string {
	for _, i := range section.Images {
		if i.Name == name {
			return i.Ref
		}
	}
	return name
}

// newImageProbe builds the hidden probe verb, what qory image check runs as the entry
// point of a container of the image: it checks the image from inside, as the user the
// container runs as, for each runtime its arguments list as <name>=<version>, an empty
// version taking any, and prints an [image.Report] as JSON on its standard output. It
// fails only when it cannot print.
func newImageProbe() *cobra.Command {
	return &cobra.Command{
		Use:    "probe [runtime=version...]",
		Short:  "Check the image this runs in, from inside, and print the report as JSON",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := image.Probe{
				Root:     "/",
				Runtimes: probeRuntimes(args),
				User:     fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(p.Report(cmd.Context()))
		},
	}
}

// probeRuntimes are the runtimes of the probe's arguments, each <name>=<version>, with
// the program each one's launch template starts and its [render.Installer] package. A
// runtime this qory does not render, or renders with no launch template, has no
// program, and its line fails.
func probeRuntimes(args []string) []image.Runtime {
	out := make([]image.Runtime, len(args))
	for i, a := range args {
		name, version, _ := strings.Cut(a, "=")
		out[i] = image.Runtime{Name: name, Version: version}
		rt, err := render.Lookup(name)
		if err != nil {
			continue
		}
		if l, ok := rt.(render.Launcher); ok {
			out[i].Command = l.Template().Command
		}
		if in, ok := rt.(render.Installer); ok && version != "" {
			out[i].Install = in.Install(version)
		}
	}
	return out
}
