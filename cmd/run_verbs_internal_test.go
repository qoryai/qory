package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/runner/session"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/render"
)

// TestNoRuntimeIsNamedAfterAVerbTheWallRuns holds the runtimes this qory renders apart
// from the verbs that carry [inWall]: qory run takes a runtime as its argument, and cobra
// finds a verb of qory run before it reads an argument, so a runtime named after one of
// them could never be started with qory run <name>. The verbs are found by walking the
// command tree and the runtimes are the ones registered, so a new verb or a new runtime
// is held to it without a change here.
func TestNoRuntimeIsNamedAfterAVerbTheWallRuns(t *testing.T) {
	var verbs []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Annotations[inWall] != "" {
			verbs = append(verbs, c.Name())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(Root())
	for _, want := range []string{"forward", "nest", "relay"} {
		if !slices.Contains(verbs, want) {
			t.Errorf("the walk found the verbs %v, without %s: it no longer sees what carries %s", verbs, want, inWall)
		}
	}
	runtimes := render.Names()
	if len(runtimes) == 0 {
		t.Fatal("no runtime is registered")
	}
	for _, name := range runtimes {
		if slices.Contains(verbs, name) {
			t.Errorf("runtime %s has the name of a verb the wall starts inside a container: qory run %s runs the verb, so the runtime could not be started with qory run %s", name, name, name)
		}
	}
}

// TestUnusedEnvSaysWhy is a line for each --env value the runner left out, and none for
// a value of another source that lost: the server's host when the server's value won,
// the deny list, the harness's fixed value, and the runner's own reason otherwise.
func TestUnusedEnvSaysWhy(t *testing.T) {
	var out strings.Builder
	unusedEnv(&out, "https://apiary.example.com/base")(session.Applied{
		{Name: "LOG_LEVEL", From: session.FromApiary, Lost: []session.Loss{{From: session.FromRun, Why: session.WhyOverridden}}},
		{Name: "PATH", From: session.FromShell, Lost: []session.Loss{{From: session.FromRun, Why: session.WhyDenied}}},
		{Name: "CODEX_HOME", From: session.FromFixed, Lost: []session.Loss{{From: session.FromRun, Why: session.WhyFixed}}},
		{Name: "PROFILE", From: session.FromRun, Lost: []session.Loss{{From: session.FromHarness, Why: session.WhyOverridden}}},
		{Name: "ODD", Lost: []session.Loss{{From: session.FromRun, Why: "other"}}},
	})
	want := "qory run: LOG_LEVEL from --env is not used: apiary.example.com sets it\n" +
		"qory run: PATH from --env is not used: no source may set it\n" +
		"qory run: CODEX_HOME from --env is not used: the harness sets it\n" +
		"qory run: ODD from --env is not used: the runner left it out (other)\n"
	if out.String() != want {
		t.Errorf("lines\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	unusedEnv(&out, "")(session.Applied{{Name: "LOG_LEVEL", From: session.FromApiary, Lost: []session.Loss{{From: session.FromRun, Why: session.WhyOverridden}}}})
	if got := out.String(); got != "qory run: LOG_LEVEL from --env is not used: the server sets it\n" {
		t.Errorf("no server: %q", got)
	}
}

// TestMountRefusedSaysHowTheMountStands is the runner's mount_contains_runner_files
// worded with Overlap's relation: a mount that lies inside the runner's directory reads
// the access key when access-key-secret is there, and could change one of the runner's
// files when it is not; a mount of another of the runner's files could change it, and
// any other error is left to the rest.
func TestMountRefusedSaysHowTheMountStands(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "locks")
	refusal := func(mount, path string) error {
		return fmt.Errorf("wrapped: %w", &session.Refusal{Code: "mount_contains_runner_files", Names: []string{mount, path}})
	}
	if got, want := mountRefused(refusal(inner, dir), dir, nil).Error(), "the mount "+inner+" lies inside "+dir+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("lies inside, no access key: %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(dir, "access-key-secret"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := mountRefused(refusal(inner, dir), dir, nil).Error(), "the mount "+inner+" lies inside "+dir+", which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("lies inside: %q, want %q", got, want)
	}
	other := filepath.Join(dir, "wall-files")
	if got, want := mountRefused(refusal(dir, other), filepath.Join(dir, "elsewhere"), nil).Error(), "the mount "+dir+" contains "+other+", which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path (mount_contains_runner_files)"; got != want {
		t.Errorf("another file: %q, want %q", got, want)
	}
	var ref *session.Refusal
	if err := mountRefused(refusal(dir, dir), dir, nil); !errors.As(err, &ref) || ref.Code != "mount_contains_runner_files" {
		t.Errorf("the refusal does not unwrap: %v", err)
	}
	for _, err := range []error{errors.New("cannot resolve the mount"), &session.Refusal{Code: "variable_reserved", Names: []string{"A", "B"}}} {
		if got := mountRefused(err, dir, nil); got != nil {
			t.Errorf("%v: %v", err, got)
		}
	}
}

// TestConfigLinksGuardsTheLinksOnTheWay is configLinks over a configuration directory
// whose descriptor is the first of two links: the link on the way goes as its place, a
// mount of whose directory the runner refuses and one beside it in that directory it
// does not; where the chain leads goes as it is; a descriptor whose chain loops is
// passed as it is, so the runner cannot resolve it and refuses the run; and a link in
// runtimes/ that is not a descriptor is not followed.
func TestConfigLinksGuardsTheLinksOnTheWay(t *testing.T) {
	resolve := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	dir, hop, last := resolve(t.TempDir()), resolve(t.TempDir()), resolve(t.TempDir())
	runtimes := filepath.Join(dir, DescriptorsDir)
	beside := filepath.Join(hop, "beside")
	for _, d := range []string{runtimes, beside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(last, "goose.yaml"), []byte("name: goose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]string{
		{filepath.Join(last, "goose.yaml"), filepath.Join(hop, "goose.yaml")},
		{filepath.Join(hop, "goose.yaml"), filepath.Join(runtimes, "goose.yaml")},
		{"loop.yaml", filepath.Join(runtimes, "loop.yaml")},
		{"notes", filepath.Join(runtimes, "notes")},
	} {
		if err := os.Symlink(l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	files, shown := configLinks(dir)
	place := linkPlace(filepath.Join(hop, "goose.yaml"))
	if want := []string{place, filepath.Join(last, "goose.yaml"), filepath.Join(runtimes, "loop.yaml")}; !slices.Equal(files, want) {
		t.Errorf("files %q, want %q", files, want)
	}
	if got := shown[place]; got != filepath.Join(hop, "goose.yaml") {
		t.Errorf("the place %s shows %q", place, got)
	}
	if how := session.Overlap(hop, place); how != "contains" {
		t.Errorf("a mount of the link's directory %q the link", how)
	}
	if how := session.Overlap(beside, place); how != "" {
		t.Errorf("a mount beside the link %q it", how)
	}
	for _, name := range []string{"goose.yaml", "-a", "]a", "^a", `\a`, "[a", "a*b?c[d]"} {
		pattern := filepath.Base(linkPlace(filepath.Join(hop, name)))
		if ok, err := filepath.Match(pattern, name); !ok || err != nil || pattern == name || !strings.Contains(pattern, "[") {
			t.Errorf("%q as %q: %v, %v", name, pattern, ok, err)
		}
		if ok, _ := filepath.Match(pattern, name+"x"); ok {
			t.Errorf("%q matches %q", pattern, name+"x")
		}
	}
}
