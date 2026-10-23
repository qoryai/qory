package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// TestRunPoolHidesASecretInTheJournalsPath returns the refusal of a journal that is not
// a file with the part of its path that contains an access key secret printed as a
// phrase, before any run is claimed.
func TestRunPoolHidesASecretInTheJournalsPath(t *testing.T) {
	srv := provisionertest.New(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "qak_state", "qory", "pool")
	if err := os.MkdirAll(filepath.Join(state, pool.JournalFile), 0o700); err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	s := &poolSettings{
		Forager: &config.Forager{Server: &config.ForagerServer{URL: srv.URL + "/", AccessKeyID: provisionertest.KeyID, Pin: srv.Pin()}},
		Pool:    &config.ForagerPool{Runs: 1, Registry: "localhost:5001/qory-env", RunConfig: filepath.Join(root, "runs")},
		Runs:    &config.Forager{Wall: &config.ForagerWall{Image: "agent", User: "1000:1000", Helper: filepath.Join(root, "qory-linux")}},
		State:   state, InstanceID: provisionertest.InstanceID, InstanceName: "pool-test", Stderr: &stderr,
	}
	err = runPool(context.Background(), s, srv.Pool, srv.Pin())
	shown := filepath.Join(root, "(a name that contains an access key secret)", "qory", "pool", pool.JournalFile)
	if err == nil || !strings.HasPrefix(err.Error(), shown+" is not a journal") || accesskey.ContainsSecret(err.Error()+stderr.String()) {
		t.Errorf("%v, logged %q, want it to start with %s", err, stderr.String(), shown)
	}
	if len(srv.Dispatches()) != 0 {
		t.Error("a run was claimed")
	}
}
