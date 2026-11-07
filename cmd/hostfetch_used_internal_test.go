package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// TestAComposeLineOfAHiddenHarnessEndsWithTheUsedCode is each kind of a compose's error
// in a run from a run code whose harness is hidden, with the status it exits with. Each
// named case has its line. Any other line gets one of two starts by its status, whatever its text
// starts with, and one end, in place of a full stop. A line that has the end and a
// collision that was printed stay as they are.
func TestAComposeLineOfAHiddenHarnessEndsWithTheUsedCode(t *testing.T) {
	const (
		takeIn = ", then take the work item in again in Qory Apiary: "
		used   = "; the code is used"
	)
	n := source.NewNamed("the workspace's harness")
	withUser := &stack.Stack{Extends: stack.Source{Git: "https://reader:pw@git.example/acme/stack.git", Ref: "main"}}
	fix := compose.CheckBase(withUser, "the workspace's harness")
	fixLine := "remove the user name and password from the URL of the base of the workspace's harness: the run's stack proxy sets the harness credentials of the run configuration, so a source's URL needs none"
	printed := reported(&compose.CollisionError{})
	denied := &fs.PathError{Op: "open", Path: "/run/sources", Err: fs.ErrPermission}
	for _, c := range []struct {
		name string
		err  error
		want string
		exit int
	}{
		{"https", input(&source.SchemeError{URL: "http://git.example/x", Name: "the module tools"}),
			"use an https URL for the module tools" + takeIn + "a run from a control plane fetches git sources only over https" + used, ExitInput},
		{"a gone ref", input(&source.GoneError{URL: "https://git.example/x", Ref: "r", Name: "the module tools"}),
			"set the ref of the module tools to a branch, tag or commit its repository has" + takeIn + "the repository of the module tools has no branch or tag by the ref it is set to" + used, ExitInput},
		{"an export, under a module", input(fmt.Errorf("module tools: %w", &source.ExportGoneError{Where: "the module tools", Export: "module tools"})),
			"correct exports.dir in the qory.yaml of the module tools, or set the module tools to a ref whose repository has the folder of module tools" + takeIn + "the module tools exports module tools at a folder that is not there" + used, ExitInput},
		{"an export, under check", input(fmt.Errorf("check the base of the workspace's harness: %w", &source.ExportGoneError{Where: "the base of the workspace's harness", Export: "stack web"})),
			"correct exports.dir in the qory.yaml of the base of the workspace's harness, or set the base of the workspace's harness to a ref whose repository has the folder of stack web" + takeIn + "the base of the workspace's harness exports stack web at a folder that is not there" + used, ExitInput},
		{"a module's path", input(fmt.Errorf("module more: %w", &stack.SourcePathError{Path: "../x", Module: "more"})),
			"set the path of the module more to a folder inside its repository" + takeIn + "the path is absolute, the repository's root, or leads out of the repository" + used, ExitInput},
		{"the path of a module with no name", input(fmt.Errorf("modules[1]: %w", &stack.SourcePathError{Path: "../x", Module: "modules[1]"})),
			"set the path of the module modules[1] to a folder inside its repository" + takeIn + "the path is absolute, the repository's root, or leads out of the repository" + used, ExitInput},
		{"the path of extends", input(fmt.Errorf("extends: %w", &stack.SourcePathError{Path: "../x"})),
			"set the path of the base of the workspace's harness to a folder inside its repository" + takeIn + "the path is absolute, the repository's root, or leads out of the repository" + used, ExitInput},
		{"exit 2, a line that starts with a fix", input(fix),
			"correct the workspace's harness" + takeIn + fixLine + used, ExitInput},
		{"exit 2, a line with only a cause", input(errors.New("module world: qory-module.yaml in the module world: yaml: bad")),
			"correct the workspace's harness" + takeIn + "module world: qory-module.yaml in the module world: yaml: bad" + used, ExitInput},
		{"exit 2, a cause with a full stop", input(errors.New("the file is bad.")),
			"correct the workspace's harness" + takeIn + "the file is bad" + used, ExitInput},
		{"exit 1, an error of the machine", denied,
			"fix the cause" + takeIn + denied.Error() + used, 1},
		{"exit 1, a line that starts with a fix", errors.New("module tools: Ship fewer files. It contains more than 10,000 files, the most qory copies into the home."),
			"fix the cause" + takeIn + "module tools: Ship fewer files. It contains more than 10,000 files, the most qory copies into the home" + used, 1},
		{"exit 1, a git that is too old", fmt.Errorf("module tools: %w", &hostgit.GitVersionError{Version: "2.34.1"}),
			"fix the cause" + takeIn + "module tools: Install git 2.36 or later. qory reads git's worktree list in a form that git 2.34.1 does not print" + used, 1},
		{"a line that has the end", input(errors.New("Set harness.home to a folder outside the checkout" + used)),
			"Set harness.home to a folder outside the checkout" + used, ExitInput},
		{"a collision that was printed", printed, printed.Error(), ExitCollision},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := namedComposeError(c.err, n)
			if got.Error() != c.want {
				t.Errorf("the line is\n %q\nwant\n %q", got.Error(), c.want)
			}
			if code := ExitCode(got); code != c.exit {
				t.Errorf("exit %d, want %d", code, c.exit)
			}
		})
	}
	if !errors.Is(namedComposeError(printed, n), ErrReported) {
		t.Error("the collision is not marked as printed")
	}
}
