package prebuild_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/pool/prebuild"
	devimage "github.com/qoryai/qory/internal/prebuild"
)

// testStep is a step of the test's run, its out folder out.
func testStep(out string) prebuild.Step {
	return prebuild.Step{
		Out: out, RunID: testRunID, ImageDestination: testDestination, EnvironmentDestination: testEnvironmentDestination,
		AgentImage: testAgent, User: "1000:1000",
	}
}

// TestStepArgsReadBack writes a step as the helper's arguments and reads them back the
// same; a step from an environment image leaves out --environment-destination.
func TestStepArgsReadBack(t *testing.T) {
	for _, st := range []prebuild.Step{
		testStep("/w/out"),
		{Out: "/w/out", RunID: testRunID, ImageDestination: testDestination, EnvironmentImage: testEnvImage, AgentImage: testAgent, User: "1000:1000"},
	} {
		got, err := prebuild.ParseStep(st.Args())
		if err != nil || got != st {
			t.Errorf("%q read as %+v, %v; want %+v", st.Args(), got, err, st)
		}
	}
	if args := testStep("/w/out").Args(); slices.Contains(args, "--environment-image") {
		t.Errorf("%q has an empty member", args)
	}
}

// TestParseStepErrors is an error for arguments the helper is not run with, and for a
// step for which Launch returns an error as a run, before anything is built.
func TestParseStepErrors(t *testing.T) {
	args := testStep("/w/out").Args()
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", append(slices.Clone(args), "--tag", "x"), "give qory run prebuild-step only --out, --run-id, --image-destination, --environment-destination, --environment-image, --agent-image and --user: it does not take "},
		{"a flag twice", append(slices.Clone(args), "--user", "0:0"), "give --user once: qory run prebuild-step got it twice"},
		{"no value", append(slices.Clone(args), "--environment-image"), "give a value after --environment-image: qory run prebuild-step got none"},
		{"relative out", testStep("out").Args(), "give qory run prebuild-step --out with an absolute path: it writes its result there"},
		{"no out", args[2:], "give qory run prebuild-step --out with an absolute path: it writes its result there"},
		{"run id", append(slices.Clone(args[:2]), "--run-id", "../x"), "the run id is not a UUID"},
		{"agent image by tag", prebuild.Step{Out: "/w", RunID: testRunID, ImageDestination: testDestination, EnvironmentDestination: testEnvironmentDestination, AgentImage: "a:1", User: "1:1"}.Args(), "the agent image is not pinned by digest"},
		{"both environment members", prebuild.Step{Out: "/w", RunID: testRunID, ImageDestination: testDestination, EnvironmentDestination: testEnvironmentDestination, EnvironmentImage: testEnvImage, AgentImage: testAgent, User: "1:1"}.Args(), "ask Qory Apiary's instance admin: the run has both or neither"},
	} {
		if _, err := prebuild.ParseStep(c.args); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// TestRunStepWritesItsError writes the build's error to the result, image_missing for
// an environment image not in its registry, and a new result file alone: a result left
// in out, or a link there, fails the step.
func TestRunStepWritesItsError(t *testing.T) {
	e := newEnv(t)
	e.script.EnvironmentImage, e.script.Build = testEnvImage, "missing"
	b, _ := json.Marshal(e.script)
	if err := os.WriteFile(filepath.Join(e.settings.RunConfig, scriptFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	st := prebuild.Step{Out: out, RunID: testRunID, ImageDestination: testDestination, EnvironmentImage: testEnvImage, AgentImage: testAgent, User: "1000:1000"}
	settings := prebuild.StepSettings{Dir: t.TempDir(), Env: e.stepEnv(), Docker: os.Args[0], Build: e.build}
	err := prebuild.RunStep(context.Background(), st, settings)
	if !errors.Is(err, devimage.ErrImageMissing) {
		t.Fatalf("%v, want the missing image", err)
	}
	var res map[string]any
	data, rerr := os.ReadFile(filepath.Join(out, prebuild.ResultFile))
	if rerr != nil || json.Unmarshal(data, &res) != nil {
		t.Fatalf("the result %q: %v", data, rerr)
	}
	if res["image_missing"] != true || !strings.HasPrefix(res["error"].(string), "prebuild: check that the Docker engine reaches the registry of r.example/env:1") || res["target"] != nil {
		t.Errorf("the result is %v", res)
	}
	if got := e.tags(t, "pushed"); len(got) != 0 {
		t.Errorf("docker push pushed %q", got)
	}
	// A result already there is not written over.
	e.script.Build = ""
	b, _ = json.Marshal(e.script)
	os.WriteFile(filepath.Join(e.settings.RunConfig, scriptFile), b, 0o600)
	if err := prebuild.RunStep(context.Background(), st, settings); err == nil || !errors.Is(err, os.ErrExist) {
		t.Errorf("%v, want the result's file to exist", err)
	}
}

// TestLaunchResultErrors gives the error of each result Launch does not take from
// behind the wall, its fix first; the run closes failed and nothing is reported.
func TestLaunchResultErrors(t *testing.T) {
	const prefix = "check the prebuild's lines behind the wall: "
	for _, c := range []struct{ result, want string }{
		{"link", prefix + "its result, out/result.json, could not be read: "},
		{"large", prefix + "its result, out/result.json, could not be read: it is not a regular file of at most 1 MiB"},
		{"fifo", prefix + "its result, out/result.json, could not be read: it is not a regular file of at most 1 MiB"},
		{"member", prefix + "its result, out/result.json, could not be read: it does not read as the step's result"},
		{"gone", prefix + "its result, out/result.json, could not be read: "},
		{"elsewhere", prefix + `its result has the image "localhost:5001/elsewhere" for the run's image_destination, which is "` + testDestination + `"`},
	} {
		t.Run(c.result, func(t *testing.T) {
			e := newEnv(t)
			e.script.Result = c.result
			_, err := e.launch(t, false)
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("%v, want %q", err, c.want)
			}
			if len(e.reported()) != 0 || !slices.Equal(e.closed(), []session.Outcome{closedFailed}) {
				t.Errorf("reported %+v, closed %+v", e.reported(), e.closed())
			}
			e.noLeak(t, err)
		})
	}
	e := newEnv(t)
	e.script.Exit = 3
	if _, err := e.launch(t, false); err == nil || err.Error() != "check the prebuild's lines behind the wall: the step exited 3 and wrote no error" {
		t.Errorf("exit 3: %v", err)
	}
	e = newEnv(t)
	e.script.Join = "fail"
	want := "check the wall and wall.prebuild_image in the run configuration's forager.yaml: the prebuild's session behind the wall did not start: the image prebuild of the wall could not start"
	if _, err := e.launch(t, false); err == nil || err.Error() != want {
		t.Errorf("join failed: %v, want %q", err, want)
	}
}

// TestLaunchNeedsTheServersPolicy stops a run whose policy is not the server's run
// configuration's, as the run's record has it, before anything is fetched: the error
// starts with its fix, the run closes failed, and no session joins.
func TestLaunchNeedsTheServersPolicy(t *testing.T) {
	const prefix = "set a run configuration for the run's workspace in Qory Apiary: a prebuild runs under its policy alone, and the run's policy is "
	for policy, want := range map[string]string{"config": "the gateway's machine's", "none": "none", "-": "not recorded"} {
		e := newEnv(t)
		e.script.Policy = policy
		_, err := e.launch(t, false)
		if err == nil || err.Error() != prefix+want {
			t.Errorf("%s: %v, want %q", policy, err, prefix+want)
		}
		if len(e.fetched) != 0 || len(e.joins) != 0 || !slices.Equal(e.closed(), []session.Outcome{closedFailed}) {
			t.Errorf("%s: fetched %d, joined %d, closed %+v", policy, len(e.fetched), len(e.joins), e.closed())
		}
	}
}

// TestPolicySourceReadsTheLastPolicy reads the source of the last
// dev.qory.run.policy_applied of the record, passing over lines that do not read.
func TestPolicySourceReadsTheLastPolicy(t *testing.T) {
	dir := t.TempDir()
	if got := prebuild.PolicySource(dir); got != "" {
		t.Errorf("no record: %q", got)
	}
	body := `{"type":"dev.qory.run.policy_applied","data":{"source":"none"}}` + "\n" + "not json\n" +
		`{"type":"dev.qory.run.started","data":{"source":"config"}}` + "\n" +
		`{"type":"dev.qory.run.policy_applied","data":{"source":"fetched"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := prebuild.PolicySource(dir); got != "fetched" {
		t.Errorf("%q, want fetched", got)
	}
}

// TestLaunchNeedsTheRegistry stops a run whose image_destination or
// environment_destination is not under pool.registry, when it is set, before anything
// is made; one under it, or the registry itself, runs.
func TestLaunchNeedsTheRegistry(t *testing.T) {
	const want = "set pool.registry to the registry of Qory Apiary's QORY_REGISTRY, or ask its instance admin: the run's %s is not under %s"
	for _, c := range []struct{ registry, member string }{
		{"localhost:5001/targets", "environment_destination"},
		{"localhost:5001/environments", "image_destination"},
		{"localhost:5001/targets/0123", "image_destination"},
		{"registry.example.com", "image_destination"},
	} {
		e := newEnv(t)
		e.settings.Registry = c.registry
		r := testRun(&holder{b: []byte(testKey)}, "")
		_, err := prebuild.Launch(context.Background(), e.settings, r)
		w := strings.Replace(strings.Replace(want, "%s", c.member, 1), "%s", `"`+c.registry+`"`, 1)
		if err == nil || err.Error() != w {
			t.Errorf("%s: %v, want %q", c.registry, err, w)
		}
		if len(e.specs) != 0 {
			t.Errorf("%s: the run opened", c.registry)
		}
	}
	for _, registry := range []string{"localhost:5001", testDestination[:strings.LastIndex(testDestination, "/targets")]} {
		e := newEnv(t)
		e.settings.Registry = registry
		if _, err := e.launch(t, false); err != nil {
			t.Errorf("%s: %v", registry, err)
		}
	}
	// An environment image from elsewhere is not pushed, so the registry does not apply.
	e := newEnv(t)
	e.settings.Registry = "localhost:5001/targets"
	e.script.EnvironmentImage = testEnvImage
	if _, err := e.launch(t, false); err != nil {
		t.Errorf("from an environment image: %v", err)
	}
}
