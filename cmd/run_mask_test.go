//go:build unix

package cmd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/pool/prebuild"
)

// TestARunMasksTheAgentsOutput is a fake runtime on pipes that prints a value of --env
// on stdout and on stderr, once in two writes with a pause between: qory run prints
// each copy as ***, and never the value.
func TestARunMasksTheAgentsOutput(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	const value = "shop-token-value-12345"
	t.Setenv("SHOP_TOKEN", value)
	script := filepath.Join(t.TempDir(), "fake-runtime")
	writeFile(t, script, `#!/bin/sh
echo "out $SHOP_TOKEN"
echo "err $SHOP_TOKEN" >&2
sleep 0.2
printf 'split shop-token-'
sleep 0.2
printf 'value-12345 done\n'
exit 0
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	out, err := run(t, "run", "claude", "--env", "SHOP_TOKEN")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "out ***\n", "err ***\n", "split *** done\n", "✓ claude exited 0\n")
	lacks(t, out, value, "shop-token-")
}

// TestAPrebuildsLinesAreMasked is a prebuild from a run code whose lines contain the run
// key and a signed URL: qory run prints both as ***.
func TestAPrebuildsLinesAreMasked(t *testing.T) {
	docker, _ := fakeDocker(t)
	helper := staticELF(t)
	runKeyCheckout(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  image: "+prebuildAgent+"\n  command: "+docker+"\n  helper: "+helper+"\n  user: \"1000:1000\"\n"+prebuildImages)
	g := newCodeRelay(t)
	g.documents = prebuildDocuments(t, "prebuild.json")
	t.Cleanup(cmd.SetLaunchPrebuild(func(_ context.Context, s prebuild.Settings, r prebuild.Run) (prebuild.Result, error) {
		s.Log(r.RunID + ": docker push failed for https://blob.example/x?X-Amz-Signature=abc123def456 with " + string(r.Key.Bytes()))
		return prebuild.Result{}, nil
	}))
	out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "?X-Amz-Signature=*** with ***\n")
	lacks(t, out, g.runKey(), "abc123def456")
}

// TestRunPrepareMasksTheStartCommandsOutput is a start command that prints a signed URL:
// qory run prepare prints its signature as ***.
func TestRunPrepareMasksTheStartCommandsOutput(t *testing.T) {
	dir, _ := prepareRemote(t, map[string]string{
		".devcontainer.json": `{"postCreateCommand": "echo pulled https://blob.example/x?X-Amz-Signature=abc123def456"}`,
	})
	fetchedOnHost(t, dir)
	out, err := run(t, "run", "prepare")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "pulled https://blob.example/x?X-Amz-Signature=***\n")
	lacks(t, out, "abc123def456")
}

// TestTheSessionGetsTheRunsMaskList is a run with a value of --env: OpenSpec.Masker of
// its session is set, and replaces that value with ***.
func TestTheSessionGetsTheRunsMaskList(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	const value = "session-token-value-1"
	t.Setenv("SHOP_TOKEN", value)
	composedForFake(t, root, "/bin/true")
	var opened []session.OpenSpec
	_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{OnOpen: func(s session.OpenSpec) { opened = append(opened, s) }})
	defer restore()
	out, err := run(t, "run", "claude", "--env", "SHOP_TOKEN")
	if !errors.Is(err, cmd.ErrJoinedNowhere) || len(opened) != 1 {
		t.Fatalf("%d opens, %v\n%s", len(opened), err, out)
	}
	m := opened[0].Masker
	if m == nil || m.Replace("a "+value+" b") != "a *** b" {
		t.Errorf("OpenSpec.Masker %v does not mask the --env value", m)
	}
}
