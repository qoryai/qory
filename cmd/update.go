package cmd

import (
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
		Short: "Update qory to the newest release",
		Long: `Update qory to the newest release.

The newest release is read from GitHub. How it is installed follows how this qory was
installed. A Homebrew install runs brew upgrade qory. A go install runs go install
github.com/qoryai/qory@latest. A release binary, the install script's or a downloaded
one, is replaced in place: the release's archive for this platform is downloaded, checked
against the release's checksums, and renamed over the running binary. A build from a
source checkout is not updated; rebuild it, or install a release.

--check reports whether a newer release exists and installs nothing.

A build from the main branch between releases carries a pseudo-version, which is ahead
of the newest release. Such a build is not updated on its own: on a terminal, update
offers to install the release over it and asks; in a script, --release says yes.

Every command looks for a newer release, when its error output is a terminal, and prints
a notice after its own output when the newest release is ahead of its version. GitHub is
asked at most once an hour; between asks the answer is read from a file under the user's
cache directory. A build from the main branch between releases carries a pseudo-version
and is told of a release only when it is behind one. QORY_NO_UPDATE_CHECK=1 turns the
look off, and so does CI being set.

--verbose adds nothing here.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			noticeOff = true
			b := build()
			u := ui.New(cmd.OutOrStdout())
			u.Title("qory update", b.title())
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			latest, err := update.GitHub.Latest(ctx)
			if err != nil {
				return err
			}
			var cache update.Cache
			if path, err := update.CachePath(); err == nil {
				cache = update.Cache{Path: path}
				_ = cache.Write(update.Record{Checked: time.Now(), Current: b.Version, Latest: latest})
			}
			if b.Version == "" {
				u.Text("This build carries no version, so nothing says whether it is behind.")
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
			if tool != nil {
				err = runTool(cmd, u, tool[0], tool[1:]...)
			} else if err = update.GitHub.Install(ctx, latest, exe, runtime.GOOS, runtime.GOARCH); err == nil {
				u.Success("updated %s to %s %s", b.title(), latest, ui.Pot)
			}
			if err != nil {
				return err
			}
			// The look was made by the binary that has just been replaced; the next
			// command looks for itself.
			return cache.Clear()
		},
	}
	c.Flags().BoolVar(&check, "check", false, "report whether a newer release exists and install nothing")
	c.Flags().BoolVar(&toRelease, "release", false, "install the newest release over a build that is ahead of it, without asking")
	return c
}

// runTool runs the installer that put this qory on the machine, brew or go, with its
// output passed through, and reports when it is done.
func runTool(cmd *cobra.Command, u *ui.UI, name string, args ...string) error {
	tool := exec.CommandContext(cmd.Context(), name, args...)
	tool.Stdout = cmd.OutOrStdout()
	tool.Stderr = cmd.ErrOrStderr()
	tool.Stdin = cmd.InOrStdin()
	if err := tool.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	u.Success("updated with %s %s", name, ui.Pot)
	return nil
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
// when the build carries no version, or when the command is qory update. A pseudo-version
// is compared like a release, so a build from main sees a notice only when a release is
// ahead of it. A failed look is silent; the next command tries again.
func StartUpdateCheck(w io.Writer) func() {
	f, ok := w.(*os.File)
	if os.Getenv("QORY_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" || !ok || !term.IsTerminal(f.Fd()) {
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
