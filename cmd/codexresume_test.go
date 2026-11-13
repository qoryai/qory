package cmd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/receiver"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/render/codex"
)

// The paused run a Codex continuation continues: its id, Codex's session id in it, and
// the path of Codex's transcript in it, below that run's CODEX_HOME, the home of a
// walled Codex run. pausedTranscript is the transcript, one line of Codex's rollout.
const (
	pausedRunID      = "0192f0c1-7d4e-7a2b-8c3d-00000000a5ed"
	pausedSessionID  = "0192f0c1-0000-7000-8000-00000000c0de"
	pausedRolloutRel = "2026/11/13/rollout-2026-11-13T09-00-00-" + pausedSessionID + ".jsonl"
	pausedRollout    = codex.HomeInWall + "/sessions/" + pausedRolloutRel
	pausedTranscript = `{"type":"session_meta","payload":{"id":"` + pausedSessionID + `"}}` + "\n"
)

// resumeControl is a control plane in this process whose run configuration of every run
// continues the paused run [pausedRunID], with no policy, and which serves that run's
// transcript, [pausedTranscript], at <run.url>/<run_id>/resume/transcript. asked are the
// run ids of the transcript requests it got.
type resumeControl struct {
	mu    sync.Mutex
	asked []string
}

// startResumeControl starts a [resumeControl] and has the gateway of [codeAtGateway] take
// each run's configuration from it, for the rest of the test.
func startResumeControl(t *testing.T) *resumeControl {
	t.Helper()
	key, signer := newKey(t), newKey(t)
	store, err := receiver.OpenFile(filepath.Join(t.TempDir(), "received.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	pin := accesskey.Pin{{Alg: "ed25519", PublicKey: signer.PublicKey().String()}}
	c := &resumeControl{}
	var srv *httptest.Server
	h := &receiver.Handler{
		Keys: func(id string) (receiver.AccessKey, bool) {
			return receiver.AccessKey{PublicKey: key.PublicKey()}, id == gatewayAccessKey
		},
		Signer: signer,
		Store:  store,
		Configuration: func() ([]byte, string) {
			doc := `{"version":1,"node_id":"nd_f1xt0re00000000e","workspaces":["ws_f1xt0re00000000e"]` +
				`,"apiary_public_key":[{"alg":"ed25519","public_key":"` + signer.PublicKey().String() + `"}]` +
				`,"events":{"url":"` + srv.URL + receiver.DefaultEventsPath + `","types":["*"]},"run":{"url":"` + srv.URL + receiver.DefaultRunPath + `"}}`
			return []byte(doc), digest(doc)
		},
		RunConfiguration: func(receiver.Run) ([]byte, string, *receiver.Refusal) {
			doc := `{"version":1,"resume":{"run_id":"` + pausedRunID + `","session_id":"` + pausedSessionID + `","path":"` + pausedRollout + `"}}`
			return []byte(doc), digest(doc), nil
		},
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, _ := strings.CutPrefix(r.URL.Path, receiver.DefaultRunPath+"/")
		runID, path, _ := strings.Cut(rest, "/")
		if r.Method != http.MethodGet || path != "resume/transcript" {
			h.ServeHTTP(w, r)
			return
		}
		c.mu.Lock()
		c.asked = append(c.asked, runID)
		c.mu.Unlock()
		sum := sha256.Sum256([]byte(pausedTranscript))
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Length", strconv.Itoa(len(pausedTranscript)))
		w.Header().Set(server.HeaderSignature, signer.SignAnswer(accesskey.Answer{Status: http.StatusOK,
			RequestSignature: r.Header.Get(server.HeaderSignature), Digest: hex.EncodeToString(sum[:])}))
		_, _ = w.Write([]byte(pausedTranscript))
	}))
	t.Cleanup(srv.Close)
	codeGatewayServer = &gateway.Server{Version: 1, URL: srv.URL, AccessKeyID: gatewayAccessKey, ApiaryPublicKey: pin, AccessKey: key, InstanceID: "i_test"}
	t.Cleanup(func() { codeGatewayServer = nil })
	return c
}

// transcriptAsked fails the test unless the control plane got one transcript request,
// for the run keyRunID.
func (c *resumeControl) transcriptAsked(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !slices.Equal(c.asked, []string{keyRunID}) {
		t.Errorf("the transcript requests were for the runs %q, want one for %s", c.asked, keyRunID)
	}
}

// helloSkill is the skill workflow-hello of a stack's module, which a Codex run starts.
var helloSkill = map[string]string{
	"examples/hello/modules/hello/skills/workflow-hello/SKILL.md": "---\nname: workflow-hello\ndescription: Say hello.\n---\n\nSay hello.\n",
}

// stubResumedCodex is a program codex for a continuation: it prints its arguments, one
// per line, CODEX_HOME, and the path below CODEX_HOME and the text of each file in
// $CODEX_HOME/sessions, and exits 0.
const stubResumedCodex = `#!/bin/sh
for a in "$@"; do echo "stub: arg $a"; done
echo "stub: CODEX_HOME=$CODEX_HOME"
cd "$CODEX_HOME" || exit 7
find sessions -type f 2>/dev/null | while read -r f; do echo "stub: file $f"; echo "stub: text $(cat "$f")"; done
exit 0
`

// stubArgs are the arguments the program of [stubResumedCodex] printed in out.
func stubArgs(out string) []string {
	var args []string
	for line := range strings.Lines(out) {
		if a, ok := strings.CutPrefix(line, "stub: arg "); ok {
			args = append(args, strings.TrimSuffix(a, "\n"))
		}
	}
	return args
}

// checkResumedArgs fails the test unless args, the arguments of a continuation of
// Codex, are want from exec on, with resume and the paused run's session id right
// before the last argument, the prompt, which starts with mention, the skill
// workflow-hello as the run mentions it. It returns the arguments from exec on.
func checkResumedArgs(t *testing.T, args, want []string, mention string) []string {
	t.Helper()
	i, n := slices.Index(args, "exec"), len(args)
	if i < 0 || !strings.HasPrefix(args[n-1], mention+" ") {
		t.Fatalf("the arguments %q have no exec or do not end with the prompt", args)
	}
	want = slices.Concat(want, []string{"resume", pausedSessionID, args[n-1]})
	if !slices.Equal(args[i:], want) {
		t.Errorf("Codex started with %q from exec on, want %q", args[i:], want)
	}
	return args[i:]
}

// fixedValue is the value of the fixed variable name of spec, and "" for none.
func fixedValue(spec session.SessionSpec, name string) string {
	for _, kv := range spec.LaunchFixed {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

// TestAnUnwalledCodexContinuationResumesInItsCodexHome is the continuation of a paused
// Codex run, from a run code, with no wall, at a gateway whose control plane has the
// paused run's resume. The paused run was walled, so its transcript's path is below
// /var/lib/qory-codex-home. Forager writes the transcript at the same names below this
// run's CODEX_HOME, the runtime's directory of the composed home, before Codex starts,
// and starts codex exec with resume and the session id right before the prompt.
func TestAnUnwalledCodexContinuationResumesInItsCodexHome(t *testing.T) {
	atGateway(t)
	codeReal = true
	t.Cleanup(func() { codeReal = false })
	ctl := startResumeControl(t)
	stub := filepath.Join(tempDir(t), "codex")
	writeFile(t, stub, stubResumedCodex)
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tempDir(t), "home")
	r := runFromCodeLinks(t, func(string) []string {
		machineConfig(t, "harness:\n  launch:\n    codex:\n      command: "+stub+"\n")
		return nil
	}, []string{"--home", home}, writesHello, func(d map[string]any) {
		d["run_settings"] = map[string]any{"runtime": "codex"}
	}, helloSkill, nil)
	if !r.joined || r.err != nil {
		record, _ := os.ReadFile(filepath.Join(r.open.RunsDir, r.open.RunID, "session.jsonl"))
		t.Fatalf("%v (exit %d), want the join and exit 0\n%s\n%s", r.err, cmd.ExitCode(r.err), r.out, record)
	}
	t.Logf("qory run:\n%s", r.out)
	ctl.transcriptAsked(t)
	codexHome := fixedValue(r.spec, codex.HomeVariable)
	if filepath.Base(codexHome) != codex.Runtime || !strings.HasPrefix(codexHome, home+"/") {
		t.Errorf("CODEX_HOME is %s, want the folder codex of the composed home below %s", codexHome, home)
	}
	wants(t, r.out, "stub: CODEX_HOME="+codexHome+"\n", "stub: file sessions/"+pausedRolloutRel+"\n", "stub: text "+strings.TrimSuffix(pausedTranscript, "\n")+"\n")
	if r.inputDir == "" {
		t.Fatalf("no --add-dir in %q", r.spec.Args)
	}
	checkResumedArgs(t, stubArgs(r.out), []string{"exec", "--json", "--add-dir", r.inputDir}, "$workflow-hello")
}
