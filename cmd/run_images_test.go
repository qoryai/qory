package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// imagesSection is a wall section, behind a program that stands in for docker, that
// defines two images, one with a Docker of its own, and names the first as the default.
func imagesSection(docker, helper string) string {
	return "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: go\n  images:\n" +
		"    go: {ref: example.com/agent-go:1}\n" +
		"    go-docker: {ref: example.com/agent-go-docker:1, runtime: sysbox-runc, docker: true}\n" +
		"  command: " + docker + "\n  helper: " + helper + "\n  user: \"1000:1000\"\n"
}

// imagePolicy writes a run's own policy, outside the checkout, that selects image, or no
// image when it is empty.
func imagePolicy(t *testing.T, image string) string {
	t.Helper()
	body := "version: 1\negress:\n  mode: observe\n"
	if image != "" {
		body += "image: " + image + "\n"
	}
	path := filepath.Join(t.TempDir(), "policy.yaml")
	writeFile(t, path, body)
	return path
}

// TestRunStartsTheImageTheMachineDefines runs behind the Docker wall with a program
// standing in for docker. wall.image names the default by its name in wall.images,
// --image sets another by name, and the run's policy, its own or the server's, selects
// one by name over both; a reference is still one. An image with a Docker of its own
// starts under its runtime as the container's root, with qory's helper as the entry
// point in its nest mode, the agent's user and the launch after it; the record names the
// image, its runtime and its daemon.
func TestRunStartsTheImageTheMachineDefines(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	helper := staticELF(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), imagesSection(docker, helper))
	nested := []string{
		"--runtime sysbox-runc --user 0:0 --security-opt no-new-privileges --init --mount type=volume,dst=/var/lib/docker ",
		"src=" + helper + ",dst=/qory/qory,readonly",
		"--entrypoint /qory/qory example.com/agent-go-docker:1 run nest --user 1000:1000 -- /qory/qory run exec --unset ",
		" -- claude --settings " + runsDir(t, root),
	}
	for _, c := range []struct {
		name    string
		args    []string
		want    []string
		lack    []string
		started map[string]any
	}{
		{"the default, by name", nil,
			[]string{"--entrypoint /qory/qory example.com/agent-go:1 run exec --unset ", " -- claude --settings " + runsDir(t, root)},
			[]string{"--runtime", "run nest", "agent-go-docker", session.PreparationScript},
			map[string]any{"image": "example.com/agent-go:1", "image_name": "go", "container_runtime": nil, "docker": nil}},
		{"--image, by name", []string{"--image", "go-docker"}, nested, []string{"--entrypoint claude"},
			map[string]any{"image": "example.com/agent-go-docker:1", "image_name": "go-docker", "container_runtime": "sysbox-runc", "docker": true}},
		{"the policy's selection", []string{"--policy", imagePolicy(t, "go-docker")}, nested, []string{"--entrypoint claude"},
			map[string]any{"image": "example.com/agent-go-docker:1", "image_name": "go-docker", "container_runtime": "sysbox-runc", "docker": true}},
		{"the policy's selection over --image", []string{"--image", "go", "--policy", imagePolicy(t, "go-docker")}, nested, []string{"--entrypoint claude"},
			map[string]any{"image": "example.com/agent-go-docker:1", "image_name": "go-docker", "container_runtime": "sysbox-runc", "docker": true}},
		{"--image over the policy's none", []string{"--image", "go-docker", "--policy", imagePolicy(t, "")}, nested, []string{"--entrypoint claude"},
			map[string]any{"image": "example.com/agent-go-docker:1", "image_name": "go-docker", "container_runtime": "sysbox-runc", "docker": true}},
		{"--image, a reference", []string{"--image", "example.com/other:3"},
			[]string{"--entrypoint /qory/qory example.com/other:3 run exec --unset ", " -- claude --settings"},
			[]string{"--runtime", "run nest"},
			map[string]any{"image": "example.com/other:3", "image_name": nil, "container_runtime": nil, "docker": nil}},
	} {
		if err := os.RemoveAll(runsDir(t, root)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(log); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		out, err := run(t, append([]string{"run", "claude"}, c.args...)...)
		if cmd.ExitCode(err) != 4 {
			t.Fatalf("%s: run returned %v (exit %d)\n%s", c.name, err, cmd.ExitCode(err), out)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		wants(t, string(data), c.want...)
		lacks(t, string(data), c.lack...)
		_, evs := events(t, root)
		started := sessionStarted(t, evs)
		for k, v := range c.started {
			if started[k] != v {
				t.Errorf("%s: session.started %s is %v, want %v", c.name, k, started[k], v)
			}
		}
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	srv := newFakeServer(t, `{"version":1,"egress":{"mode":"observe"},"image":"go-docker"}`)
	serverFile(t, srv, strings.TrimPrefix(imagesSection(docker, helper), "apiVersion: qory.dev/v1alpha1\n"))
	if out, err := run(t, "run", "claude", "--image", "go"); cmd.ExitCode(err) != 4 {
		t.Fatalf("the server's selection: run returned %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	_, evs := events(t, root)
	if started := sessionStarted(t, evs); started["image_name"] != "go-docker" || started["docker"] != true {
		t.Errorf("the server's selection: session.started %v", started)
	}
	if applied := sessionApplied(t, evs); applied["image"] != "go-docker" {
		t.Errorf("the server's selection: run.policy_applied %v", applied)
	}
}

// TestRunRefusesAnImageSelectionItCannotStart pins the refusals of an image before
// anything starts, each an input error that leaves no record: a policy that selects an
// image the machine does not define, or names a reference not pinned by digest, which
// Forager's policy format refuses, or selects one for a run without a wall, nested
// Docker without a runtime, and a wall with no image at all. A run whose --policy
// selects an image needs no default, without wall.images_allow; a run whose policy the
// server supplies does, since the server's run configuration arrives once the run starts
// and may select none, and the refusal says so. A run configuration from the server that selects an image the
// machine does not define is Forager's to refuse, in its words.
func TestRunRefusesAnImageSelectionItCannotStart(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, _ := fakeDocker(t)
	helper := staticELF(t)
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	writeFile(t, file, imagesSection(docker, helper))
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "--policy", imagePolicy(t, "media")}, "the policy selects the image media, which wall.images in forager.yaml does not define"},
		{[]string{"run", "--policy", imagePolicy(t, "example.com/agent-go:1")}, "the policy names its image by a reference that is not pinned by digest"},
		{[]string{"run", "--wall", "none", "--policy", imagePolicy(t, "go")}, "the policy selects the image go, which needs a wall: wall in forager.yaml, or --wall docker"},
	} {
		out, err := run(t, c.args...)
		if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v (exit %d), want %q and exit %d\n%s", c.args, err, cmd.ExitCode(err), c.want, cmd.ExitInput, out)
		}
	}
	writeFile(t, file, "apiVersion: qory.dev/v1alpha1\n")
	if _, err := run(t, "run", "--wall", "docker", "--policy", imagePolicy(t, "go")); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "the policy selects the image go, which wall.images in forager.yaml does not define") {
		t.Errorf("--wall docker with no wall section, under a policy that selects an image: %v", err)
	}
	writeFile(t, file, strings.Replace(imagesSection(docker, helper), "runtime: sysbox-runc, ", "", 1))
	want := "forager.yaml: wall.images.go-docker: docker needs a runtime that runs a daemon without privileges, such as runtime: sysbox-runc"
	for _, args := range [][]string{{"run"}, {"config"}} {
		if _, err := run(t, args...); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), want) {
			t.Errorf("%v with a Docker of its own and no runtime: %v", args, err)
		}
	}
	writeFile(t, file, strings.Replace(imagesSection(docker, helper), "  image: go\n", "", 1))
	if _, err := run(t, "run"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "a wall needs the container's image: --image, or wall.image in forager.yaml, a name of wall.images or a reference") {
		t.Errorf("a wall with no image: %v", err)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a refused run left a record")
	}
	if out, err := run(t, "run", "--policy", imagePolicy(t, "go")); cmd.ExitCode(err) != 4 {
		t.Errorf("a run whose policy selects an image, with no default: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	noDefault := strings.TrimPrefix(strings.Replace(imagesSection(docker, helper), "  image: go\n", "", 1), "apiVersion: qory.dev/v1alpha1\n")
	srv := newFakeServer(t, `{"version":1,"egress":{"mode":"observe"},"image":"go"}`)
	serverFile(t, srv, noDefault)
	if _, err := run(t, "run"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "a name of wall.images or a reference; with a server, set one even when its run configuration selects an image: that arrives once the run starts, and may select none") {
		t.Errorf("a run whose policy the server supplies, with no default: %v", err)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Error("a refused run left a record")
	}
	if out, err := run(t, "run", "--local", "--policy", imagePolicy(t, "go")); cmd.ExitCode(err) != 4 {
		t.Errorf("a --local run whose own policy selects an image, with no default: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	srv = newFakeServer(t, `{"version":1,"egress":{"mode":"observe"},"image":"media"}`)
	serverFile(t, srv, strings.TrimPrefix(imagesSection(docker, helper), "apiVersion: qory.dev/v1alpha1\n"))
	if _, err := run(t, "run"); err == nil || !strings.Contains(err.Error(), `the policy selects the image "media", which this machine does not define`) {
		t.Errorf("a server's run configuration that selects an image the machine does not define: %v", err)
	}
}

// TestConfigListsTheImages lists every image the machine defines under the default, with
// its reference, its runtime and its daemon.
func TestConfigListsTheImages(t *testing.T) {
	emptyDir(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), imagesSection("docker", "/opt/qory/qory-linux"))
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	rows := fieldRows(out)
	for key, want := range map[string]string{
		"wall.image":            "go (wall.images.go)",
		"wall.images.go":        "example.com/agent-go:1",
		"wall.images.go-docker": "example.com/agent-go-docker:1, runtime sysbox-runc, docker (experimental)",
	} {
		if got := rows[key]; len(got) != 1 || !strings.HasPrefix(got[0], want+"  ") || !strings.HasSuffix(got[0], "  ~/.config/qory/forager.yaml") {
			t.Errorf("row %s = %q, want %q from forager.yaml", key, got, want)
		}
	}
}

// TestNestIsHiddenAndTakesItsArgumentsAsTheyAre pins the helper's nest mode, what the
// wall starts in a container with a Docker of its own: it is not in the help, and its
// arguments, --user and -- among them, reach Forager's nest unparsed. The root user
// is refused wherever it runs, so nothing is started.
func TestNestIsHiddenAndTakesItsArgumentsAsTheyAre(t *testing.T) {
	emptyDir(t)
	root := cmd.Root()
	if c, _, err := root.Find([]string{"run", "nest"}); err != nil || c.Name() != "nest" || !c.Hidden {
		t.Fatalf("run nest is %v, %v", c, err)
	}
	out, err := run(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "nest")
	const unread = "nest: want --user uid:gid -- command [args]"
	if _, err := run(t, "run", "nest", "--user", "1000:1000", "claude"); err == nil || err.Error() != unread {
		t.Errorf("without --: %v", err)
	}
	_, err = run(t, "run", "nest", "--user", "0:0", "--", "claude", "--settings", "x")
	if err == nil || !strings.HasPrefix(err.Error(), "nest: ") || err.Error() == unread {
		t.Errorf("with its arguments: %v", err)
	}
}

// TestARunsPolicyNamesAnImageByReference is a --policy that names its image by a
// reference pinned by digest: qory leaves it to Forager. Without wall.images_allow,
// Forager refuses it as the run starts, image_not_allowed, and the run is recorded;
// under a prefix of the list, the image's manifest is checked at its registry, a
// registry on this machine, and the wall fetches it. A list without a default image is
// refused as input before anything starts.
func TestARunsPolicyNamesAnImageByReference(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log := fakeDocker(t)
	helper := staticELF(t)
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	ref, policy, allowed, requests := localImage(t)

	writeFile(t, file, imagesSection(docker, helper))
	out, err := run(t, "run", "--policy", policy)
	if err == nil || cmd.ExitCode(err) != 1 || !strings.Contains(err.Error(), "image_not_allowed") {
		t.Fatalf("without wall.images_allow: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	ids := recorded(t, root)
	if len(ids) != 1 {
		t.Fatalf("the refused run is recorded %v times", ids)
	}
	data, err := os.ReadFile(filepath.Join(runsDir(t, root), ids[0], "session.jsonl"))
	if err != nil || !strings.Contains(string(data), `"type":"dev.qory.run.refused"`) || !strings.Contains(string(data), `"code":"image_not_allowed"`) {
		t.Errorf("the record holds %s (%v)", data, err)
	}

	if err := os.RemoveAll(runsDir(t, root)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, imagesSection(docker, helper)+allowed+localGateway)
	out, err = run(t, "run", "--policy", policy)
	if err == nil || strings.Contains(err.Error(), "image_not_allowed") {
		t.Errorf("under wall.images_allow: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), "pull "+ref+"\n")
	if want := []string{"HEAD /v2/agents/go/manifests/" + ref[strings.Index(ref, "@")+1:]}; !slices.Equal(requests(), want) {
		t.Errorf("the registry got %q, want %q", requests(), want)
	}

	writeFile(t, file, strings.Replace(imagesSection(docker, helper), "  image: go\n", "", 1)+allowed+localGateway)
	_, err = run(t, "run", "--policy", policy)
	want := "wall.images_allow in forager.yaml needs a default image, which an image by reference starts under: --image, or wall.image"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a list without a default: %v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
}
