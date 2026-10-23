package cmd_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// hiddenLinks are the links of the hello stack's modules in the tests of a hidden
// harness: a file link and a directory link, each to a place inside its module.
var hiddenLinks = map[string]string{
	"examples/hello/modules/hello/GUIDE.md": "AGENTS.md",
	"examples/hello/modules/world/docs":     "skills",
}

// linksIn is every link under home, by its path relative to home.
func linksIn(t *testing.T, home string) []string {
	t.Helper()
	if home == "" {
		t.Fatal("the session has no harness home")
	}
	var links []string
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			rel, _ := filepath.Rel(home, p)
			links = append(links, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return links
}

// regularFiles fails the test for each path, relative to home, that is not a regular
// file.
func regularFiles(t *testing.T, home string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if info, err := os.Lstat(filepath.Join(home, filepath.FromSlash(p))); err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s is not a regular file: %v", p, err)
		}
	}
}

// TestARunCodeWithAHiddenHarnessComposesNoLink is a run from a run code whose documents
// set harness_hidden to true, without a wall, of a stack whose modules contain a link to
// a file and a link to a directory: the home contains copies of every module, skill and
// command, and no link, each link of a module copied as the file or directory it leads
// to. The copy keeps the hello module's greet skill, which the stack's exclude leaves
// out of the compose, as a link to the module would show it. The run's open sets
// HarnessHidden.
func TestARunCodeWithAHiddenHarnessComposesNoLink(t *testing.T) {
	agents := helloFiles(t)["examples/hello/modules/hello/AGENTS.md"]
	r := runFromCodeLinks(t, nil, nil, nil, nil, nil, hiddenLinks)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if !r.open.HarnessHidden {
		t.Error("the open of a run whose harness is hidden does not set HarnessHidden")
	}
	home := r.spec.HarnessHome
	if links := linksIn(t, home); len(links) != 0 {
		t.Errorf("the home of a hidden harness has the links %v", links)
	}
	regularFiles(t, home, "modules/hello/GUIDE.md", "modules/world/docs/greet/SKILL.md", "modules/hello/commands/hello.md",
		"modules/hello/skills/greet/SKILL.md", "skills/greet/SKILL.md", "claude/plugin/skills/greet/SKILL.md")
	guide, err := os.ReadFile(filepath.Join(home, "modules", "hello", "GUIDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(guide) != agents {
		t.Errorf("modules/hello/GUIDE.md contains %q, want the module's AGENTS.md, %q", guide, agents)
	}
}

// TestARunCodeWithAShownHarnessKeepsItsLinks is a run from a run code whose documents set
// harness_hidden to false, without a wall: the home links to the modules qory fetched,
// as a run without a wall does, and the run's open leaves HarnessHidden unset.
func TestARunCodeWithAShownHarnessKeepsItsLinks(t *testing.T) {
	r := runFromCodeLinks(t, nil, nil, nil, func(d map[string]any) { d["harness_hidden"] = false }, nil, hiddenLinks)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if r.open.HarnessHidden {
		t.Error("the open of a run whose harness is shown sets HarnessHidden")
	}
	links := linksIn(t, r.spec.HarnessHome)
	for _, want := range []string{"modules/hello", "modules/world", "skills/greet"} {
		if !slices.Contains(links, want) {
			t.Errorf("%s is not a link; the links are %v", want, links)
		}
	}
}

// TestAWalledRunCodeWithAHiddenHarnessComposesNoLink is a walled run from a run code whose
// documents set harness_hidden to true: the home contains no link, leaves out the hello
// module's greet skill, which the stack's exclude leaves out, as a walled run's copy
// does, and the run's open sets HarnessHidden.
func TestAWalledRunCodeWithAHiddenHarnessComposesNoLink(t *testing.T) {
	r := runFromCodeLinks(t, func(string) []string {
		docker, _ := fakeDocker(t)
		walledBy(t, docker)
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, nil, nil, nil, hiddenLinks)
	if !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if !r.open.HarnessHidden {
		t.Error("the open of a walled run whose harness is hidden does not set HarnessHidden")
	}
	home := r.spec.HarnessHome
	if links := linksIn(t, home); len(links) != 0 {
		t.Errorf("the home of a walled hidden harness has the links %v", links)
	}
	regularFiles(t, home, "modules/hello/GUIDE.md", "modules/world/docs/greet/SKILL.md", "skills/greet/SKILL.md")
	if _, err := os.Lstat(filepath.Join(home, "modules", "hello", "skills", "greet")); !os.IsNotExist(err) {
		t.Errorf("behind a wall, the copy keeps the hello module's greet skill, which the stack leaves out: %v", err)
	}
}

// TestARunCodeWithAHiddenHarnessStopsOnALinkOutOfAModule is a run from a run code whose
// documents set harness_hidden to true, of a stack whose hello module contains a link
// to the stack's README.md, outside the module: the compose stops before the join, exit
// 1, with a line that starts with its fix, as the copy of a walled run's module stops.
func TestARunCodeWithAHiddenHarnessStopsOnALinkOutOfAModule(t *testing.T) {
	r := runFromCodeLinks(t, nil, nil, nil, nil, nil, map[string]string{"examples/hello/modules/hello/README.md": "../../README.md"})
	if r.joined || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.err.Error(), "module hello: Remove the link README.md or point it inside the module. It links to ", "/examples/hello/README.md, outside the module, and qory copies only the module's files into the home.")
}

// TestARunCodeWithAHiddenHarnessStopsAtAPluginOf10000 is a run from a run code whose
// documents set harness_hidden to true, without a wall, of a stack whose two modules
// each contain a skill of 5,000 files more: each module is copied, and the plugin, with
// more than 10,000 entries, stops the compose with the fix for a home that copies every
// module, since moving a module out of the checkout copies it too.
func TestARunCodeWithAHiddenHarnessStopsAtAPluginOf10000(t *testing.T) {
	extra := map[string]string{"examples/hello/modules/hello/skills/bulk/SKILL.md": "---\nname: bulk\ndescription: A skill of the test.\n---\n\nSteps.\n"}
	for _, dir := range []string{"examples/hello/modules/hello/skills/bulk", "examples/hello/modules/world/skills/greet"} {
		for i := range 5_000 {
			extra[fmt.Sprintf("%s/f%05d", dir, i)] = ""
		}
	}
	r := runFromCodeLinks(t, nil, nil, nil, nil, extra, nil)
	if r.joined || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.err.Error(), "Ship fewer files in the modules. claude/plugin contains more than 10,000 files, directories and links, the most a Claude Code run behind a wall or with a hidden harness takes.")
	lacks(t, r.err.Error(), "Move a module out of the checkout")
}
