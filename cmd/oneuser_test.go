package cmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/cmd"
)

// oneUserFix and oneUserCause are line 1 for a local or run-key run and for a run from a run
// code.
const (
	oneUserFix   = "qory run: run qory as a dedicated user, and set wall.user in forager.yaml to its uid:gid\n"
	oneUserCause = "qory run: qory and the wall user are not one ordinary user, so the run did not start; the code is used\n"
)

// oneUserUIDs is line 2 of both, for qory's uid and the wall user's.
func oneUserUIDs(uid, wall string) string { return oneUserLine(uid, "uid "+wall) }

// oneUserLine is line 2 of both, for qory's uid and the wall user as the line shows it.
func oneUserLine(uid, wall string) string {
	return "  qory runs as uid " + uid + " and the wall user is " + wall + "; a walled run needs one ordinary user for both.\n"
}

// wantsNoEngineCommand fails the test when the fake engine's log exists: the engine's
// program ran.
func wantsNoEngineCommand(t *testing.T, log string) {
	t.Helper()
	if got := checkLog(t, log); got != "" {
		t.Errorf("the engine's program ran:\n%s", got)
	}
}

// wallUserIs writes a forager.yaml whose wall is docker, a program standing in for it, with
// user as wall.user, none for "".
func wallUserIs(t *testing.T, docker, user string) {
	t.Helper()
	line := ""
	if user != "" {
		line = "  user: \"" + user + "\"\n"
	}
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n"+line)
}

// TestALocalWalledRunNeedsOneOrdinaryUser is a walled run with no control plane: qory as
// root, or as another uid than wall.user's, stops before the engine's program runs, and the
// run closes failed machine_setup_invalid. A wall.user that is not <uid> or <uid>:<gid> in
// digits stops it too, with the value as it prints and at most 200 characters of it. qory as wall.user's uid, whatever the gid, and
// as an ordinary user with no wall.user, runs the agent.
func TestALocalWalledRunNeedsOneOrdinaryUser(t *testing.T) {
	for _, c := range []struct {
		name string
		uid  int
		user string
		// line2 is line 2 of the stop, "" for a run that runs the agent.
		line2 string
	}{
		{"root with another wall.user", 0, "1000:1000", oneUserUIDs("0", "1000")},
		{"root with wall.user root", 0, "0:0", oneUserUIDs("0", "0")},
		{"root with no wall.user", 0, "", oneUserUIDs("0", "0")},
		{"another uid", 2000, "1000:1000", oneUserUIDs("2000", "1000")},
		{"wall.user root", 1000, "0:0", oneUserUIDs("1000", "0")},
		{"wall.user a name", 1000, "agent", oneUserLine("1000", "agent")},
		{"wall.user a name and a group", 1000, "agent:staff", oneUserLine("1000", "agent:staff")},
		{"wall.user a sign", 1000, "+1000", oneUserLine("1000", "+1000")},
		{"wall.user no gid after the colon", 1000, "1000:", oneUserLine("1000", "1000:")},
		{"wall.user a space", 1000, " 1000", oneUserLine("1000", " 1000")},
		{"wall.user a group name", 1000, "1000:x", oneUserLine("1000", "1000:x")},
		{"wall.user a group alone", 1000, ":1000", oneUserLine("1000", ":1000")},
		{"wall.user past a uid", 1000, "99999999999999999999", oneUserUIDs("1000", "99999999999999999999")},
		{"root with a name", 0, "agent", oneUserLine("0", "agent")},
		{"wall.user a name that does not print", 1000, `ag\n\e[31ment`, oneUserLine("1000", "ag??[31ment")},
		{"wall.user an access key secret", 1000, "qak_value-for-the-test-only", oneUserLine("1000", "(a value that contains an access key secret)")},
		{"wall.user a long name", 1000, strings.Repeat("a", 300), oneUserLine("1000", strings.Repeat("a", 200)+"...")},
		{"qory's uid", 1000, "1000:1000", ""},
		{"qory's uid and another gid", 1000, "1000:2000", ""},
		{"qory's uid and no gid", 1000, "1000", ""},
		{"no wall.user", 1000, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.user == "" && c.line2 == "" && os.Geteuid() == 0 {
				t.Skip("needs the real uid: Forager gives the containers the uid:gid of this process, and refuses root's")
			}
			root := newCheckout(t)
			copyFixture(t, "two-modules", root)
			composedForFake(t, root, "claude")
			docker, log := fakeDocker(t)
			wallUserIs(t, docker, c.user)
			t.Cleanup(cmd.SetQoryUID(c.uid))
			out, err := run(t, "run", "claude")
			if c.line2 == "" {
				if cmd.ExitCode(err) != 4 {
					t.Fatalf("%v (exit %d), want the agent's 4\n%s", err, cmd.ExitCode(err), out)
				}
				wants(t, out, "inside the container")
				return
			}
			if cmd.ExitCode(err) != 1 {
				t.Errorf("%v (exit %d), want 1", err, cmd.ExitCode(err))
			}
			wants(t, out, oneUserFix+c.line2)
			wantsNoEngineCommand(t, log)
			ids := recorded(t, root)
			if len(ids) != 1 {
				t.Fatalf("the records are %q, want one", ids)
			}
			rec, err := os.ReadFile(filepath.Join(runsDir(t, root), ids[0], "session.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if got := lastExited(t, rec); got != "failed/machine_setup_invalid/-1" {
				t.Errorf("the run ended %s, want failed/machine_setup_invalid/-1", got)
			}
		})
	}
}

// TestARunKeysWalledRunNeedsOneOrdinaryUser is a walled run with a run key and no run code,
// with qory as root, through Forager's open at a gateway in this process: the output starts
// with the fix, the run closes failed machine_setup_invalid, and the engine's program does
// not run.
func TestARunKeysWalledRunNeedsOneOrdinaryUser(t *testing.T) {
	keyedFolder(t, true)
	docker, log := fakeDocker(t)
	wallUserIs(t, docker, "1000:1000")
	t.Cleanup(cmd.SetQoryUID(0))
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	closes, calls, restore := cmd.OpenNowhereWith(cmd.Nowhere{Proxy: "http://qory:nowhere@127.0.0.1:9", AtGateway: true})
	defer restore()
	out, err := run(t, "run", "claude", "--run-key-fd", descriptor(t, key))
	if cmd.ExitCode(err) != 1 {
		t.Errorf("%v (exit %d), want 1", err, cmd.ExitCode(err))
	}
	wants(t, out, oneUserFix+oneUserUIDs("0", "1000"))
	wantsMachineSetupInvalid(t, closes(), calls())
	wantsNoEngineCommand(t, log)
}

// TestARunCodesWalledRunNeedsOneOrdinaryUser is a walled run from a run code, with qory as
// root and as another uid than wall.user's, through Forager's open at a gateway in this
// process: the output has the cause alone, the run closes failed machine_setup_invalid, and
// the engine's program does not run.
func TestARunCodesWalledRunNeedsOneOrdinaryUser(t *testing.T) {
	for _, c := range []struct {
		name  string
		uid   int
		shown string
	}{
		{"root", 0, "0"},
		{"another uid", 2000, "2000"},
	} {
		t.Run(c.name, func(t *testing.T) {
			docker, log := fakeDocker(t)
			p := scopedRepo(t, docker)
			p.real = true
			t.Cleanup(cmd.SetQoryUID(c.uid))
			r := p.run(t, nil, nil)
			if cmd.ExitCode(r.err) != 1 {
				t.Errorf("%v (exit %d), want 1", r.err, cmd.ExitCode(r.err))
			}
			wants(t, r.out, oneUserCause+oneUserUIDs(c.shown, "1000"))
			lacks(t, r.out, "run qory as a dedicated user")
			wantsMachineSetupInvalid(t, r.closes, r.calls)
			wantsNoEngineCommand(t, log)
		})
	}
}

// TestARootQoryNeverReachesAScopeCheck is a write run from a run code whose session leaves a
// commit past its start, with no wall.user. As an ordinary user, qory runs the scope check of
// the plan type before the push. As root, the run stops before its session, so no scope check
// runs, which would run as root, see [pushcheck.ScopeUser].
func TestARootQoryNeverReachesAScopeCheck(t *testing.T) {
	for _, c := range []struct {
		name   string
		uid    int
		checks bool
	}{
		{"root", 0, false},
		{"an ordinary user", 1000, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			docker, log := scopeDocker(t, "exit 1", "src/app.go: outside the plan\n")
			p := scopedRepo(t, docker)
			wallUserIs(t, docker, "")
			t.Cleanup(cmd.SetQoryUID(c.uid))
			r := p.run(t, leavesApp(t), nil)
			if got := checkLog(t, log); (got != "") != c.checks {
				t.Errorf("the scope checks are %q, want one: %t\n%s", got, c.checks, r.out)
			}
			if c.checks {
				p.wantsNoPush(t, r, contracts.EndCodePushScopeRefused, "src/app.go: outside the plan\n")
				return
			}
			p.wantsNoPush(t, r, contracts.EndCodeMachineSetupInvalid, oneUserCause+oneUserUIDs("0", "0"))
			wantsMachineSetupInvalid(t, r.closes, r.calls)
		})
	}
}

// TestAPrebuildNeedsOneOrdinaryUser is a prebuild from a run code, with qory as root, as
// another uid than wall.user's, and with a name in wall.user: it stops after the redemption
// with the cause alone, exit 1, the engine's program does not run, and the prebuild, whose
// first steps open the run and fetch the target, does not start. Qory's uid starts it.
func TestAPrebuildNeedsOneOrdinaryUser(t *testing.T) {
	for _, c := range []struct {
		name string
		uid  int
		user string
		// line2 is line 2 of the stop, "" for a prebuild that starts.
		line2 string
	}{
		{"root", 0, "1000:1000", oneUserUIDs("0", "1000")},
		{"root with wall.user root", 0, "0:0", oneUserUIDs("0", "0")},
		{"another uid", 2000, "1000:1000", oneUserUIDs("2000", "1000")},
		{"wall.user a name", 1000, "agent", oneUserLine("1000", "agent")},
		{"wall.user no gid after the colon", 1000, "1000:", oneUserLine("1000", "1000:")},
		{"qory's uid", 1000, "1000:1000", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			docker, log := fakeDocker(t)
			t.Cleanup(cmd.SetQoryUID(c.uid))
			got, out, err, _ := prebuildFromCode(t, prebuildDocuments(t, "prebuild.json"), "wall:\n  adapter: docker\n  image: "+prebuildAgent+"\n  images_allow: [registry.example.com/agents/]\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \""+c.user+"\"\n"+prebuildImages, nil)
			wantsNoEngineCommand(t, log)
			if c.line2 == "" {
				if err != nil || got.calls != 1 {
					t.Errorf("%v, %d prebuilds, want one\n%s", err, got.calls, out)
				}
				return
			}
			want := strings.TrimSuffix(strings.TrimPrefix(oneUserCause, "qory run: ")+c.line2, "\n")
			if err == nil || err.Error() != want || cmd.ExitCode(err) != 1 {
				t.Errorf("%v (exit %d), want %q, exit 1\n%s", err, cmd.ExitCode(err), want, out)
			}
			if got.calls != 0 {
				t.Error("the prebuild started")
			}
			lacks(t, out, "through the gateway")
		})
	}
}

// wantsMachineSetupInvalid fails the test unless the run closed once, failed
// machine_setup_invalid, with no image fetch, setup or join among calls.
func wantsMachineSetupInvalid(t *testing.T, closes []session.Outcome, calls []string) {
	t.Helper()
	want := session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodeMachineSetupInvalid), ExitCode: -1}
	if len(closes) != 1 || closes[0] != want {
		t.Errorf("the run closed with %+v, want %+v", closes, want)
	}
	for _, call := range calls {
		if slices.ContainsFunc([]string{"image", "setup", "join"}, func(s string) bool { return strings.HasPrefix(call, s) }) {
			t.Errorf("the run called %s; the calls are %q", call, calls)
		}
	}
}
