package cmd_test

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	refusals "github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// fetchPassword is the password of the engine proxy files these tests write: 43
// characters of base64url, 32 bytes.
const fetchPassword = "FakeEngineProxyPassword-0123456789abcdefghg"

// writeEngineProxy writes the engine proxy file the machine's start unit writes, with
// address and [fetchPassword], mode 0400, and returns its path.
func writeEngineProxy(t *testing.T, address string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine-proxy.json")
	writeFile(t, path, `{"version": 1, "address": "`+address+`", "password": "`+fetchPassword+`"}`+"\n")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	return path
}

// fetchRun is what a walled run from a control plane that lists acme/shop came to, with
// more after its wall section in forager.yaml and fetch as its FetchImage: [setupRun],
// and the specs of its opens.
type fetchRun struct {
	setupRun
	opens []session.OpenSpec
}

// runFetching runs qory run claude with a run key behind the fake wall, as [runSetUp]
// does with wall.setup absent and acme/shop listed, with more after the wall section of
// forager.yaml and fetch as the run's FetchImage, nil for one that succeeds.
func runFetching(t *testing.T, more string, fetch func(context.Context) error) fetchRun {
	t.Helper()
	h := newHostFetch(t, "", "main")
	docker, _ := fakeDocker(t)
	section := "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: " + docker + "\n  helper: " + staticELF(t) + "\n  user: \"1000:1000\"\n" + more
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), section)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	var r fetchRun
	defer cmd.SetJoinSession(func(s session.SessionSpec) { r.joins = append(r.joins, s) })()
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: "acme/shop",
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		StackProxy: h.server.Stack.URL, StackCABundle: h.server.Stack.CABundle,
		Setup: func(s session.SetupSpec) error {
			r.setups = append(r.setups, s)
			return nil
		},
		AgentEnv: func(session.SessionSpec) ([]string, error) {
			return []string{"QORY_RUN_REPOSITORIES=acme/shop", "QORY_RUN_SYSTEM=" + proxygittest.Host}, nil
		},
		OnOpen:     func(s session.OpenSpec) { r.opens = append(r.opens, s) },
		FetchImage: fetch,
	})
	defer restore()
	r.out, r.err = run(t, "run", "claude", "--home", t.TempDir(), "--run-key-fd", descriptor(t, key))
	r.closes, r.calls = closes(), calls()
	return r
}

// TestAWalledRunFetchesItsImageBeforeTheSetup is a walled run with machine and
// wall.engine_proxy_file set, and one with neither: the run opens with OpenSpec.Machine
// and OpenSpec.EngineProxy from them, or with none, and fetches its image once the
// harness is composed, before the setup and the join. No line contains the password.
func TestAWalledRunFetchesItsImageBeforeTheSetup(t *testing.T) {
	file := writeEngineProxy(t, "127.0.0.1:3129")
	for _, c := range []struct {
		name, more, machine string
		proxy               bool
	}{
		{"both keys", "  engine_proxy_file: " + file + "\nmachine: aws-ec2\n", "aws-ec2", true},
		{"neither key", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runFetching(t, c.more, nil)
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || len(r.opens) != 1 || len(r.joins) != 1 {
				t.Fatalf("%d opens, %d joins, %v (exit %d)\n%s", len(r.opens), len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
			}
			if want := []string{"host proxy", "host proxy closed", "stack proxy", "stack proxy closed", "fetch image", "agent env", "setup", "join"}; !slices.Equal(r.calls, want) {
				t.Errorf("the calls: %v, want %v", r.calls, want)
			}
			o := r.opens[0]
			if o.Machine != c.machine {
				t.Errorf("OpenSpec.Machine is %q, want %q", o.Machine, c.machine)
			}
			if got := o.EngineProxy != nil; got != c.proxy {
				t.Errorf("OpenSpec.EngineProxy %v, want one %v", o.EngineProxy, c.proxy)
			} else if c.proxy && (o.EngineProxy.Addr != "127.0.0.1:3129" || o.EngineProxy.Password != fetchPassword) {
				t.Errorf("OpenSpec.EngineProxy is %v", o.EngineProxy)
			}
			lacks(t, r.out, fetchPassword)
		})
	}
}

// TestAnImageRefusalClosesTheRunWithItsCode is Forager's refusal of the run's image at
// its fetch, image_missing and image_user_root: qory run prints one line, the fix first
// and Forager's detail after it, closes the run failed with the refusal's code and -1,
// and exits 1. Neither the setup nor a session runs.
func TestAnImageRefusalClosesTheRunWithItsCode(t *testing.T) {
	const image = "registry.example/agents/go@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		err  error
		code string
		line string
	}{
		{refusals.New(refusals.ImageMissing, []string{image}, "the image %s is not in its registry", image), refusals.ImageMissing,
			"\nqory run: push the image to its registry, or select another image in the run's policy: the image " + image + " is not in its registry (image_missing)\n"},
		{refusals.New(refusals.ImageUserRoot, []string{image}, "the image %s runs as root; a run's image runs as the wall's user", image), refusals.ImageUserRoot,
			"\nqory run: set USER in the image to a user other than root that its /etc/passwd lists, or select another image in the run's policy: the image " + image + " runs as root; a run's image runs as the wall's user (image_user_root)\n"},
	} {
		t.Run(c.code, func(t *testing.T) {
			r := runFetching(t, "", func(context.Context) error { return c.err })
			if r.err == nil || cmd.ExitCode(r.err) != 1 {
				t.Fatalf("%v (exit %d)\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if want := []session.Outcome{{State: "failed", Reason: c.code, ExitCode: -1}}; !slices.Equal(r.closes, want) {
				t.Errorf("the closes: %v, want %v", r.closes, want)
			}
			if len(r.joins) != 0 || len(r.setups) != 0 {
				t.Errorf("%d joins, %d setups, want none", len(r.joins), len(r.setups))
			}
			wants(t, r.out, c.line)
			if strings.Count(r.out, c.code) != 1 {
				t.Errorf("the line is printed more than once:\n%s", r.out)
			}
		})
	}
}

// TestAFailedImageFetchFailsTheRun is a fetch of the run's image that fails otherwise,
// with the engine proxy's password in Forager's error: qory run prints one line, the fix
// first and Forager's text with the password hidden, closes the run failed with no
// session, and exits 1. Neither the setup nor a session runs.
func TestAFailedImageFetchFailsTheRun(t *testing.T) {
	file := writeEngineProxy(t, "127.0.0.1:3129")
	failed := errors.New("wall docker: pull: proxyconnect tcp: http://qory:" + fetchPassword + "@127.0.0.1:3129: connection refused")
	r := runFetching(t, "  engine_proxy_file: "+file+"\n", func(context.Context) error { return failed })
	if r.err == nil || cmd.ExitCode(r.err) != 1 || errors.Is(r.err, failed) {
		t.Fatalf("%v (exit %d)\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if got := (keyRun{closes: r.closes, joined: len(r.joins) > 0}); !got.stoppedBeforeJoin() || len(r.setups) != 0 {
		t.Errorf("the run closed with %v, joined %v, %d setups, want failed before the setup", r.closes, got.joined, len(r.setups))
	}
	wants(t, r.out, "\nqory run: run again: the fetch of the run's image failed: wall docker: pull: proxyconnect tcp: http://qory:<hidden>@127.0.0.1:3129: connection refused\n")
	lacks(t, r.out, fetchPassword)
}

// TestAnEngineProxyFileThatDoesNotReadStopsTheRun is wall.engine_proxy_file set to a file
// of mode 0644 that has the password: qory run stops before the run opens, exit 2, with
// the fix first and the path, and prints no password.
func TestAnEngineProxyFileThatDoesNotReadStopsTheRun(t *testing.T) {
	file := writeEngineProxy(t, "127.0.0.1:3129")
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	r := runFetching(t, "  engine_proxy_file: "+file+"\n", nil)
	if cmd.ExitCode(r.err) != cmd.ExitInput || len(r.opens) != 0 || !strings.HasPrefix(r.err.Error(), "run chmod 0400 "+file+": its mode is 0644") {
		t.Fatalf("%d opens, %v (exit %d)\n%s", len(r.opens), r.err, cmd.ExitCode(r.err), r.out)
	}
	lacks(t, r.out+r.err.Error(), fetchPassword)
}

// TestConfigListsTheMachineAndTheEngineProxyFile lists machine and
// wall.engine_proxy_file, the path alone: qory config prints no password.
func TestConfigListsTheMachineAndTheEngineProxyFile(t *testing.T) {
	emptyDir(t)
	file := writeEngineProxy(t, "127.0.0.1:3129")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nmachine: aws-ec2\nwall:\n  adapter: docker\n  image: example.com/agent:1\n  engine_proxy_file: "+file+"\n")
	out, err := run(t, "config")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "machine", "aws-ec2", "wall.engine_proxy_file", file)
	lacks(t, out, fetchPassword)
}

// enginePull sends a GET of target through the proxy at proxyURL, as the engine's pull
// sends its requests, and again with another password, and writes into out a line for
// each, "with: <status>" or "with: error", then "without: <status>" or "without: error".
func enginePull(proxyURL, target, out string) int {
	get := func(proxy string) string {
		u, err := url.Parse(proxy)
		if err != nil {
			return "error"
		}
		c := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
		res, err := c.Get(target)
		if err != nil {
			return "error"
		}
		res.Body.Close()
		return strconv.Itoa(res.StatusCode)
	}
	wrong := strings.Replace(proxyURL, ":"+fetchPassword+"@", ":"+strings.Repeat("A", len(fetchPassword))+"@", 1)
	lines := "with: " + get(proxyURL) + "\nwithout: " + get(wrong) + "\n"
	if err := os.WriteFile(out, []byte(lines), 0o600); err != nil {
		return 1
	}
	return 0
}

// pinnedImage is the run's image these tests name by its reference, pinned by digest.
const pinnedImage = "registry.example/agents/go@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// engineDocker is a program that stands in for docker, as [fakeDocker] does, and writes
// every command line into the log it returns. It answers the image inspect of the
// repository digests with pinnedImage when held or once pulled, and with no such image
// otherwise; the image's config with the user 1000; and a pull by sending a request for
// http://registry.example/v2/ through proxyURL, which [enginePull] writes the results of
// into the file it returns.
func engineDocker(t *testing.T, held bool, proxyURL string) (docker, log, pulled string) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log, pulled = filepath.Join(dir, "docker.log"), filepath.Join(dir, "pulled")
	// The engine holds the image once the file at holds is there: from the start when
	// held, and after a pull otherwise.
	holds := filepath.Join(dir, "holds")
	if held {
		writeFile(t, holds, "")
	}
	inspect := `test -e ` + holds + ` && { echo '["` + pinnedImage + `"]'; exit 0; }; echo "Error response from daemon: No such image: $5" >&2; exit 1`
	docker = filepath.Join(dir, "docker")
	writeFile(t, docker, `#!/bin/sh
echo "$*" >> `+log+`
case "$*" in
"image inspect --format {{json .RepoDigests}} "*) `+inspect+` ;;
"image inspect --format {{json .Config}} "*) echo '{"User":"1000"}'; exit 0 ;;
"pull "*) : > `+holds+`; QORY_TEST_ENGINE_PULL=1 exec '`+bin+`' -test.run='^$' -- '`+proxyURL+`' http://registry.example/v2/ `+pulled+` ;;
esac
case "$1 $2" in
"version --format") echo "28.0.0" ;;
"network inspect")
	case "$*" in
	*.Subnet*) echo "172.30.0.0/16 " ;;
	*) echo "172.30.0.1 " ;;
	esac ;;
"logs "*) echo "relay: listening" ;;
"run "*) echo "inside the container"; exit 4 ;;
esac
`)
	if err := os.Chmod(docker, 0o755); err != nil {
		t.Fatal(err)
	}
	return docker, log, pulled
}

// freeAddress is a loopback host:port that no program listens on, as a start unit
// writes into the daemon's proxy setting.
func freeAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// imageSteps are the image steps of a run's events: the name of each step of kind
// image, and the state each ended in.
func imageSteps(evs map[string][]map[string]any) (names, states []string) {
	images := map[float64]bool{}
	for _, s := range evs["dev.qory.run.step_started"] {
		if s["kind"] == "image" {
			n, _ := s["step"].(float64)
			images[n] = true
			name, _ := s["name"].(string)
			names = append(names, name)
		}
	}
	for _, s := range evs["dev.qory.run.step_exited"] {
		if n, _ := s["step"].(float64); images[n] {
			state, _ := s["state"].(string)
			states = append(states, state)
		}
	}
	return names, states
}

// TestAWalledRunPullsItsImageThroughTheEngineProxy is a walled run whose policy names its
// image by a reference the engine does not hold, with wall.engine_proxy_file and machine
// set, under Forager's session and the gateway qory run starts: after the compose, the
// image step, named after the reference, pulls the image before the join; the pull's request
// reaches the forwarder at the file's address with its password, and the gateway's
// proxy, while one with another password gets no response; the join pulls no more;
// run.started has the machine; and no file of the run's record, and no line, contains
// the password.
func TestAWalledRunPullsItsImageThroughTheEngineProxy(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	addr := freeAddress(t)
	docker, log, pulled := engineDocker(t, false, "http://qory:"+fetchPassword+"@"+addr)
	file := writeEngineProxy(t, addr)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), imagesSection(docker, staticELF(t))+"  images_allow: [registry.example/agents/]\n  engine_proxy_file: "+file+"\nmachine: aws-ec2\n")
	out, err := run(t, "run", "claude", "--policy", imagePolicy(t, pinnedImage))
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "pull "+pinnedImage+"\n"); n != 1 {
		t.Errorf("the image is pulled %d times, want once:\n%s", n, data)
	}
	results, err := os.ReadFile(pulled)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(results)), "\n"); len(lines) != 2 || lines[0] == "with: error" || lines[1] != "without: error" {
		t.Errorf("the requests through the engine proxy: %q", results)
	}
	dir, evs := events(t, root)
	if names, states := imageSteps(evs); !slices.Equal(names, []string{pinnedImage}) || !slices.Equal(states, []string{"succeeded"}) {
		t.Errorf("the image steps %q ended %q, want one of %s, succeeded", names, states, pinnedImage)
	}
	var kinds []string
	for _, s := range evs["dev.qory.run.step_started"] {
		kind, _ := s["kind"].(string)
		kinds = append(kinds, kind)
	}
	if want := []string{"compose", "image", "join", "wall"}; !slices.Equal(kinds, want) {
		t.Errorf("the steps are of the kinds %q, want %q", kinds, want)
	}
	reached := false
	for _, e := range evs["dev.qory.run.egress"] {
		if e["host"] == "registry.example" {
			reached = true
		}
	}
	if !reached {
		t.Errorf("the gateway's proxy has no request for registry.example: %v", evs["dev.qory.run.egress"])
	}
	if started := evs["dev.qory.run.started"]; len(started) != 1 || started[0]["machine"] != "aws-ec2" {
		t.Errorf("run.started is %v, want the machine aws-ec2", started)
	}
	lacks(t, out, fetchPassword)
	noFileContains(t, dir, fetchPassword)
}

// TestAWalledRunStartsTheImageItsEngineHolds is a walled run whose policy names its
// image by a reference the engine holds by that digest, with no engine proxy, under
// Forager's session: nothing is pulled, the run's image step is the one Forager writes,
// named after the reference and succeeded, and the agent's container starts.
func TestAWalledRunStartsTheImageItsEngineHolds(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, log, _ := engineDocker(t, true, "http://qory:"+fetchPassword+"@127.0.0.1:9")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), imagesSection(docker, staticELF(t))+"  images_allow: [registry.example/agents/]\n")
	out, err := run(t, "run", "claude", "--policy", imagePolicy(t, pinnedImage))
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(data), "pull ")
	wants(t, string(data), "image inspect --format {{json .RepoDigests}} "+pinnedImage+"\n")
	_, evs := events(t, root)
	if names, states := imageSteps(evs); !slices.Equal(names, []string{pinnedImage}) || !slices.Equal(states, []string{"succeeded"}) {
		t.Errorf("the image steps %q ended %q, want one of %s, succeeded", names, states, pinnedImage)
	}
	if started := evs["dev.qory.run.started"]; len(started) != 1 || started[0]["machine"] != nil {
		t.Errorf("run.started is %v, want no machine", started)
	}
}

// noFileContains fails t when a file under dir contains secret.
func noFileContains(t *testing.T, dir, secret string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), secret) {
			t.Errorf("%s contains the password", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAnImageFetchFromARunCodeSaysTheCodeIsUsed is a walled run from a run code whose
// image's fetch fails, and whose image Forager refuses, image_missing: the code is used
// by then, so qory run's line takes the work item in again, or ends with "; the code is
// used", and exits 1. No session joins.
func TestAnImageFetchFromARunCodeSaysTheCodeIsUsed(t *testing.T) {
	missing := refusals.New(refusals.ImageMissing, []string{pinnedImage}, "the image %s is not in its registry", pinnedImage)
	for _, c := range []struct {
		err  error
		line string
	}{
		{errors.New("wall docker: pull: connection refused"),
			"\nqory run: take the work item in again in Qory Apiary: the fetch of the run's image failed: wall docker: pull: connection refused; the code is used\n"},
		{missing,
			"\nqory run: push the image to its registry, or select another image in the run's policy: the image " + pinnedImage + " is not in its registry (image_missing); the code is used\n"},
	} {
		t.Run(c.err.Error(), func(t *testing.T) {
			codeFetchImage = func(context.Context) error { return c.err }
			t.Cleanup(func() { codeFetchImage = nil })
			r := runFromCodeWith(t, func(string) []string {
				docker, _ := fakeDocker(t)
				walledBy(t, docker)
				return nil
			}, nil, nil, nil)
			if r.err == nil || cmd.ExitCode(r.err) != 1 || r.joined || !slices.Contains(r.calls, "fetch image") {
				t.Fatalf("%v (exit %d), joined %v, the calls %v\n%s", r.err, cmd.ExitCode(r.err), r.joined, r.calls, r.out)
			}
			wants(t, r.out, c.line)
		})
	}
}

// TestAMountThatCoversTheEngineProxyFileStopsTheRun is a walled run whose --mount covers
// the folder of wall.engine_proxy_file: the file is one of Forager's, so the run stops
// before the agent starts, mount_contains_forager_files, with the line that names the
// file, and no line contains the password.
func TestAMountThatCoversTheEngineProxyFileStopsTheRun(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, "claude")
	docker, _ := fakeDocker(t)
	file := writeEngineProxy(t, freeAddress(t))
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\nwall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  engine_proxy_file: "+file+"\n")
	dir, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(dir, filepath.Base(file))
	for mount, want := range map[string]string{
		dir:         "the mount " + dir + " contains " + resolved + ", which holds one of Forager's files; the agent could change it, so the run does not start. Mount a narrower path",
		dir + ":ro": "the mount " + dir + " contains " + resolved + ", which holds one of Forager's files; the agent could read it, so the run does not start. Mount a narrower path",
	} {
		out, err := run(t, "run", "claude", "--mount", mount)
		if err == nil || !strings.Contains(out+err.Error(), want) {
			t.Errorf("--mount %s: %v (exit %d), want %q\n%s", mount, err, cmd.ExitCode(err), want, out)
		}
		lacks(t, out, fetchPassword)
	}
}
