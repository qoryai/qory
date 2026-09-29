package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/ui"
	"github.com/qoryai/qory/internal/update"
)

// newUpdate builds the update verb, which installs the newest release over the running
// binary, the way that binary was installed.
func newUpdate() *cobra.Command {
	var check, toRelease bool
	c := &cobra.Command{
		Use:   "update",
		Short: "Install the newest release of qory",
		Long: `Install the newest release of qory from GitHub, the way this qory was installed:

  Homebrew        brew upgrade qory
  go install      go install github.com/qoryai/qory@latest
  release binary  download the archive, check it against the release's checksums,
                  and replace the binary in place

A build from a source checkout is not updated. Rebuild it, or install a release.

A build from main between releases is ahead of the newest release. update asks before it
installs the release over it. In a script, --release says yes.

Every command checks for a newer release when it runs at a terminal. When there is one,
it prints a notice after its output. It asks GitHub at most once an hour, and caches the
answer. A build from main gets a notice only when a release is ahead of it.
QORY_NO_UPDATE_CHECK=1 turns the check off. So does CI.

--verbose adds nothing here.`,
		Example: `  qory update           # install the newest release
  qory update --check   # only report whether there is one`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			noticeOff = true
			b := build()
			u := ui.New(cmd.OutOrStdout())
			u.Title("qory update", b.title())
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			flight := u.Fly("looking for the newest release")
			latest, err := update.GitHub.Latest(ctx)
			flight.Stop()
			if err != nil {
				return err
			}
			var cache update.Cache
			if path, err := update.CachePath(); err == nil {
				cache = update.Cache{Path: path}
				_ = cache.Write(update.Record{Checked: time.Now(), Current: b.Version, Latest: latest})
			}
			if b.Version == "" {
				u.Text("This build has no version, so whether it is behind is unknown.")
				u.Success("the newest release is %s", latest)
				return nil
			}
			rows := [][2]string{{"newest", latest}, {"changelog", update.ReleaseURL(latest)}}
			switch {
			case update.Ahead(b.Version, latest):
				u.Fields(rows)
				u.Text("This build is ahead of the newest release.")
				if check {
					u.Success("ahead of %s, the newest release", latest)
					return nil
				}
				if !toRelease {
					ask := asker(cmd.InOrStdin(), cmd.OutOrStdout())
					if ask == nil {
						return input(fmt.Errorf("this build, %s, is ahead of the newest release, %s; add --release to install the release over it", b.Version, latest))
					}
					answer, err := ask(fmt.Sprintf("Install the release %s over it? [y/N] ", latest))
					if err != nil {
						return err
					}
					if answer != "y" && answer != "yes" {
						u.Success("kept %s", b.Version)
						return nil
					}
				}
				rows = nil
			case !update.Newer(b.Version, latest):
				u.Success("up to date: %s is the newest release", latest)
				return nil
			case check:
				u.Fields(rows)
				u.Success("%s is available; run qory update to install it", latest)
				return nil
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			var tool []string
			channel := update.Detect(exe, b.Source == "release", update.GoBin())
			switch channel {
			case update.Homebrew:
				tool = []string{"brew", "upgrade", "qory"}
			case update.GoInstall:
				tool = []string{"go", "install", "github.com/" + update.Repo + "@v" + latest}
			case update.Release:
				rows = append(rows, [2]string{"binary", ui.Short(exe, "")})
			default:
				u.Fields(rows)
				return fmt.Errorf("this qory is a source build at %s, which update does not replace\nrebuild it from its checkout, or install a release:\n  go install github.com/%s@v%s", ui.Short(exe, ""), update.Repo, latest)
			}
			if tool != nil {
				rows = append(rows, [2]string{"running", strings.Join(tool, " ")})
			}
			if rows != nil {
				u.Fields(rows)
			}
			switch channel {
			case update.Homebrew:
				err = runTool(cmd, u, "", "", tool[0], tool[1:]...)
			case update.GoInstall:
				// go install fetches and builds in one silent run, so the module is fetched
				// first, as a step of its own that stays on the screen when it is done. The
				// modules a release newly needs still arrive with the build.
				err = runTool(cmd, u, "downloading qory "+latest, "downloaded qory "+latest, "go", "mod", "download", "github.com/"+update.Repo+"@v"+latest)
				if err == nil {
					err = runTool(cmd, u, "building qory "+latest+" with go install", "built qory "+latest, tool[0], tool[1:]...)
				}
			default:
				flight := u.Fly("downloading qory " + latest)
				site := update.GitHub
				site.Progress = flight.Progress
				err = site.Install(ctx, latest, exe, runtime.GOOS, runtime.GOARCH)
				if err != nil {
					flight.Stop()
				} else {
					flight.Land("downloaded qory " + latest)
					u.Success("updated %s to %s %s", b.title(), latest, ui.Pot)
				}
			}
			if err != nil {
				return err
			}
			if tool != nil {
				u.Success("updated with %s %s", tool[0], ui.Pot)
			}
			// The look was made by the binary that has just been replaced; the next
			// command looks for itself.
			return cache.Clear()
		},
	}
	c.Flags().BoolVar(&check, "check", false, "report whether a newer release exists and install nothing")
	c.Flags().BoolVar(&toRelease, "release", false, "install the newest release over a build that is ahead of it, without a confirmation prompt")
	return c
}

// runTool runs one command of the installer that put this qory on the machine, brew or
// go. With no waiting label the tool's output is passed through, which is for brew,
// since it reports its own progress and may ask. With one, a flight carrying the label
// is shown while the tool runs and lands as landed when it is done, and the tool's
// output is kept back, to follow the error when it fails, which is for go, since it
// works for a while and says next to nothing. The caller reports the update itself,
// once every command of it is done.
func runTool(cmd *cobra.Command, u *ui.UI, waiting, landed, name string, args ...string) error {
	tool := exec.CommandContext(cmd.Context(), name, args...)
	var kept bytes.Buffer
	var err error
	if waiting == "" {
		tool.Stdout = cmd.OutOrStdout()
		tool.Stderr = cmd.ErrOrStderr()
		tool.Stdin = cmd.InOrStdin()
		err = tool.Run()
	} else {
		tool.Stdout = &kept
		tool.Stderr = &kept
		flight := u.Fly(waiting)
		if err = tool.Run(); err != nil {
			flight.Stop()
		} else {
			flight.Land(landed)
		}
	}
	if err != nil {
		if out := strings.TrimSpace(kept.String()); out != "" {
			return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// walledVerb reports whether args select a verb the wall runs inside a container, one
// that carries [inWall]: qory run nest, relay or forward. The command tree finds it, so a
// flag before it, such as -v, is read as the tree reads it.
func walledVerb(args []string) bool {
	c, _, err := Root().Find(args)
	return err == nil && c.Annotations[inWall] != ""
}

// noticeOff is set by qory update, which reports the newest release itself and needs no
// notice after it.
var noticeOff bool

// StartUpdateCheck begins the look for a newer release and returns what prints the
// notice. The main package calls it before [Execute] and calls the result after, once the
// command's output and any error are written, so the notice comes last. The look runs
// beside the command, and the result waits for it no longer than the request's timeout.
//
// Nothing is looked for when QORY_NO_UPDATE_CHECK or CI is set, when w is not a terminal,
// when the build has no version, when args, the command line without the program, select
// a verb the wall starts inside a container, or when the command is qory update. Inside
// a container the look would be a connection in the run's record that the agent did not
// make, and qory run nest, at a terminal there, runs as the container's root. A
// pseudo-version is compared like a release, so a build from main sees a notice only
// when a release is ahead of it. A failed look is silent; the next command tries again.
func StartUpdateCheck(w io.Writer, args []string) func() {
	f, ok := w.(*os.File)
	if os.Getenv("QORY_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" || !ok || !term.IsTerminal(f.Fd()) || walledVerb(args) {
		return func() {}
	}
	b := build()
	if b.Version == "" {
		return func() {}
	}
	path, err := update.CachePath()
	if err != nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		latest, err := update.Check(ctx, update.GitHub, update.Cache{Path: path}, b.Version, time.Now())
		if err != nil && !errors.Is(err, context.Canceled) {
			latest = ""
		}
		done <- latest
	}()
	return func() {
		defer cancel()
		if noticeOff {
			return
		}
		if latest := <-done; update.Newer(b.Version, latest) {
			notice(ui.New(w), b.Version, latest)
		}
	}
}

// notice prints the box that says a newer release is available: the two versions, where
// the changelog is, and the command that installs it.
func notice(u *ui.UI, current, latest string) {
	u.Box(
		"Update available! "+u.Alert(current)+" → "+u.Brand(latest),
		"Changelog: "+update.ReleaseURL(latest),
		"Run \""+u.Strong("qory update")+"\" to update.",
	)
}
