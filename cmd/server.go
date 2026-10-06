package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/qoryai/runner/accesskey"
	"github.com/qoryai/runner/session"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/runnerdir"
)

// machineDir is the runner file's directory: everything qory keeps for its server.
func machineDir() runnerdir.Dir { return runnerdir.Dir(config.UserDir()) }

// keySource says where a run's access key secret came from.
type keySource int

const (
	// fromFile is access-key-secret in the runner file's directory.
	fromFile keySource = iota + 1
	// fromEnv is QORY_ACCESS_KEY_SECRET.
	fromEnv
)

// accessKey is the access key a command signs its requests with, and where its secret
// came from.
type accessKey struct {
	key    *accesskey.Key
	source keySource
}

// readAccessKey reads the access key's secret: QORY_ACCESS_KEY_SECRET, else the file
// access-key-secret in the runner file's directory. A secret that is not one, a file the
// rules refuse and the published fixture key are errors that never contain the value.
// No secret anywhere is (nil, nil).
func readAccessKey(dir runnerdir.Dir) (*accessKey, error) {
	if v, ok := os.LookupEnv(accesskey.EnvSecret); ok && v != "" {
		k, err := runnerdir.ParseSecret([]byte(v))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", accesskey.EnvSecret, err)
		}
		return &accessKey{key: k, source: fromEnv}, nil
	}
	if dir == "" {
		return nil, nil
	}
	k, err := dir.ReadSecret()
	if errors.Is(err, runnerdir.ErrNoSecret) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &accessKey{key: k, source: fromFile}, nil
}

// forgetAccessKeyEnv removes the access key's variables from qory's own environment once
// they are read, so nothing qory starts inherits them: the secret, the id and the pin.
func forgetAccessKeyEnv() {
	for _, name := range []string{accesskey.EnvSecret, accesskey.EnvID, accesskey.EnvPin} {
		os.Unsetenv(name)
	}
}

// serverIdentity is what a request to the server is signed and named with: the access
// key, the instance id and the instance's display name.
type serverIdentity struct {
	key          *accessKey
	instanceID   string
	instanceName string
}

// identify reads what a command that talks to the runner file's server signs with: the
// access key's id, which the server section or QORY_ACCESS_KEY_ID holds, its secret,
// and this instance's id and name. It prints a line when the instance id cannot be kept
// in the directory and lives for this process alone.
func identify(r *config.Runner, report io.Writer, verb string) (*serverIdentity, error) {
	dir := machineDir()
	key, err := readAccessKey(dir)
	forgetAccessKeyEnv()
	if err != nil {
		return nil, input(err)
	}
	if r.Server.AccessKeyID == "" {
		return nil, input(fmt.Errorf("%s: the server has no access_key_id: qory access-key enrol writes it, or set server.access_key_id or %s", config.RunnerFileName, accesskey.EnvID))
	}
	if key == nil {
		return nil, input(fmt.Errorf("no access key secret for the server %s: qory access-key enrol makes one, or set %s", r.Server.URL, accesskey.EnvSecret))
	}
	id := &serverIdentity{key: key, instanceName: r.InstanceNameOrDefault()}
	instance, kept, err := dir.InstanceID(runnerdir.MachineID())
	if err != nil {
		return nil, fmt.Errorf("the instance id: %w", err)
	}
	if !kept {
		fmt.Fprintf(report, "qory %s: instance %s, this process's own: %s cannot be written\n", verb, instance, dir.Path(runnerdir.InstanceFile))
	}
	id.instanceID = instance
	return id, nil
}

// sessionServer is the server document the runner takes.
func sessionServer(s *config.RunnerServer) *session.Server {
	return &session.Server{Version: 1, URL: s.URL, AccessKeyID: s.AccessKeyID, ApiaryPublicKey: s.Pin}
}

// refusedError is a refusal of the runner's, said for the person: what it means and what
// to do, then the runner's own words with the code. It unwraps to the refusal.
type refusedError struct {
	text string
	err  error
}

func (e *refusedError) Error() string { return e.text + " (" + e.err.Error() + ")" }

func (e *refusedError) Unwrap() error { return e.err }

// explain words a refusal of the server's or the runner's for the person, with the
// access key's fingerprint and the instance id where they help. An error that is no
// refusal is returned as it is.
func explain(err error, id *serverIdentity) error {
	var ref *session.Refusal
	if !errors.As(err, &ref) {
		return err
	}
	fingerprint, instance := "", ""
	if id != nil {
		fingerprint, instance = id.key.key.Fingerprint(), id.instanceID
	}
	var text string
	switch ref.Code {
	case accesskey.CodeApiaryPublicKeyMissing:
		text = fmt.Sprintf("the server has no pinned apiary_public_key, so no answer of it could be verified: qory access-key enrol writes it, or set server.apiary_public_key in %s or %s", config.RunnerFileName, accesskey.EnvPin)
	case accesskey.CodeUnauthorized:
		text = fmt.Sprintf("the server does not accept the access key %s: it does not know the key, or has revoked it; enrol a new key with qory access-key enrol", fingerprint)
	case accesskey.CodeAnswerUnsigned:
		text = "an answer of the server does not verify under the pinned apiary_public_key, so the run does not start: check server.url and the pin"
	case accesskey.CodeKeyPending:
		text = fmt.Sprintf("the access key %s awaits approval: an owner or administrator of the server compares this fingerprint with the one the server shows, and approves the key", fingerprint)
	case accesskey.CodeInstanceLimit:
		text = fmt.Sprintf("the node's live instances have reached its limit, so the instance %s does not start: wait for a run of another instance to end, or have an owner or administrator clear that instance", instance)
	case accesskey.CodeRunClosed:
		text = "the server closed the run before it started"
	default:
		return err
	}
	return &refusedError{text: text, err: err}
}
