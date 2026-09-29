package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qoryai/runner/runtimes/catalog"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/image"
	"github.com/qoryai/qory/internal/ui"
)

// newImage builds the image noun: the images the wall runs the agent in.
func newImage() *cobra.Command {
	c := &cobra.Command{
		Use:   "image",
		Short: "Check an image the wall runs the agent in",
		Long: `Check an image the wall runs the agent in.

Qory publishes four with each release, for linux/amd64 and linux/arm64:

  ghcr.io/qoryai/agent             Claude Code, git and gh
  ghcr.io/qoryai/agent-docker      the same, and a Docker daemon of the agent's own
  ghcr.io/qoryai/agent-go          agent, and Go
  ghcr.io/qoryai/agent-go-docker   agent-docker, and Go

Build yours FROM one of them, and check it with qory image check.`,
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
of ` + config.RunnerFileName + `.

From outside, it reads the image the engine holds: its platform, HOME in its
environment, and whether the reference is pinned by digest. Then it starts the image the
way the wall starts the agent, as a user the image does not know, with no capability and
no network, and qory's Linux build checks from inside:

  - HOME is a directory that user writes in
  - the system's authorities are where the wall reads them
  - /bin/sh is there, for the runtime's hooks
  - claude is the version the runner's descriptor is written against
  - git and gh run
  - no file is setuid or setgid
  - dockerd, for a Docker of the agent's own, and what it needs

The Linux build is this binary on Linux, and wall.helper elsewhere. The docker command
is wall.command, or docker.

The exit status is 0 when every image passes, and 1 when one fails.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#the-agents-image`,
		Example: `  qory image check                                # wall.image of ` + config.RunnerFileName + `
  qory image check ghcr.io/qoryai/agent:0.13.0
  qory image check my-agent:1 my-agent-docker:1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			stop := buzzing(cmd)
			defer stop()
			r, err := config.LoadRunner()
			if err != nil {
				return input(err)
			}
			var section config.RunnerWall
			if r != nil && r.Wall != nil {
				section = *r.Wall
			}
			refs := args
			if len(refs) == 0 {
				if section.Image == "" {
					return input(fmt.Errorf("name an image to check, or set wall.image in %s", config.RunnerFileName))
				}
				refs = []string{section.Image}
			}
			for _, ref := range refs {
				if err := image.CheckRef(ref); err != nil {
					return input(err)
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
				return input(fmt.Errorf("qory's Linux build %s: %w", helper, err))
			}
			rt, err := catalog.Lookup("claude", filepath.Join(config.UserDir(), DescriptorsDir))
			if err != nil {
				return input(err)
			}
			engine := image.Engine{Command: section.Command}
			platform, err := engine.Platform(cmd.Context())
			if err != nil {
				return err
			}

			u := ui.New(cmd.OutOrStdout())
			u.Title("qory image check", "engine "+platform)
			claude := rt.Version() + ", the runner's descriptor's"
			if rt.Version() == "" {
				claude = "any version; the runner's descriptor names none"
			}
			u.Fields([][2]string{{"probe", ui.Short(helper, "") + ", " + arch}, {"claude", claude}})
			var failed []string
			for _, ref := range refs {
				u.Blank()
				u.Heading(ref)
				checks := engine.Check(cmd.Context(), platform, image.Target{Ref: ref, Helper: helper, HelperArch: arch, Claude: rt.Version()})
				var facts [][2]string
				for _, c := range checks {
					switch c.Result {
					case image.Pass:
						u.Success("%s", c.Detail)
					case image.Fail:
						u.Fail(errors.New(c.Detail))
					default:
						facts = append(facts, [2]string{c.Name, c.Detail})
					}
				}
				u.Fields(facts)
				if image.Failed(checks) {
					failed = append(failed, ref)
				}
			}
			u.Blank()
			switch {
			case len(failed) > 0 && len(refs) == 1:
				u.Fail(fmt.Errorf("%s lacks what the wall needs", refs[0]))
				return reported(&exitError{code: 1})
			case len(failed) > 0:
				u.Fail(fmt.Errorf("%d of %d images lack what the wall needs: %s", len(failed), len(refs), strings.Join(failed, ", ")))
				return reported(&exitError{code: 1})
			case len(refs) == 1:
				u.Success("%s has what the wall needs %s", refs[0], ui.Pot)
			default:
				u.Success("%d images have what the wall needs %s", len(refs), ui.Pot)
			}
			return nil
		},
	}
}

// newImageProbe builds the hidden probe verb, what qory image check runs as the entry
// point of a container of the image: it checks the image from inside, as the user the
// container runs as, and prints an [image.Report] as JSON on its standard output. It
// fails only when it cannot print.
func newImageProbe() *cobra.Command {
	var claude string
	c := &cobra.Command{
		Use:    "probe",
		Short:  "Check the image this runs in, from inside, and print the report as JSON",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mounts, _ := os.ReadFile("/proc/self/mountinfo")
			p := image.Probe{
				Root:   "/",
				Env:    os.Getenv,
				Mounts: image.MountPoints(mounts),
				Claude: claude,
				User:   fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(p.Report(cmd.Context()))
		},
	}
	c.Flags().StringVar(&claude, "claude-version", "", "the version claude --version must report")
	return c
}
