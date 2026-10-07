package cmd

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

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
	// fromFD is the descriptor --access-key-secret-fd names.
	fromFD
)

// accessKey is the access key a command signs its requests with, and where its secret
// came from.
type accessKey struct {
	key    *accesskey.Key
	source keySource
}

// readAccessKey reads the access key's secret: the one read from the descriptor
// --access-key-secret-fd names, when fd is not nil, else QORY_ACCESS_KEY_SECRET, as qory
// took it when it started, else the file access-key-secret in the runner file's
// directory. A secret that is not one, a file the rules refuse and the published fixture
// key are errors that never contain the value. No secret anywhere is (nil, nil).
func readAccessKey(dir runnerdir.Dir, fd *accesskey.Key) (*accessKey, error) {
	if fd != nil {
		return &accessKey{key: fd, source: fromFD}, nil
	}
	if v := config.TakenServerVariables().AccessKeySecret; v != "" {
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

// maxSecretFD is the most read from the secret's descriptor: a secret is one line of 47
// characters.
const maxSecretFD = 4 << 10

// secretFDFlag is the flag that names the descriptor the access key's secret is read
// from.
const secretFDFlag = "access-key-secret-fd"

// secretFDUsage is the help of --access-key-secret-fd.
const secretFDUsage = "read the access key's secret from this file descriptor, 3 or above; it wins over " + accesskey.EnvSecret + " and the access-key-secret file"

// readSecretFD reads the access key's secret from file descriptor n, to its end, and
// closes the descriptor, so nothing qory starts inherits it. The standard input, output
// and error are refused. No error contains the value.
func readSecretFD(n int) (*accesskey.Key, error) {
	if n < 3 {
		return nil, input(fmt.Errorf("--%s %d: the standard input, output and error carry no secret; name a descriptor of 3 or above", secretFDFlag, n))
	}
	f := os.NewFile(uintptr(n), "--"+secretFDFlag)
	if f == nil {
		return nil, input(fmt.Errorf("--%s %d is not an open descriptor", secretFDFlag, n))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSecretFD+1))
	if err != nil {
		return nil, input(fmt.Errorf("--%s %d: %w", secretFDFlag, n, err))
	}
	defer clear(b)
	if len(b) > maxSecretFD {
		return nil, input(fmt.Errorf("--%s %d: more than an access key secret", secretFDFlag, n))
	}
	k, err := runnerdir.ParseSecret(b)
	if err != nil {
		return nil, input(fmt.Errorf("--%s %d: %w", secretFDFlag, n, err))
	}
	return k, nil
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
// the one read from --access-key-secret-fd when fd is not nil, and this instance's id
// and name. It prints a line when the instance id cannot be kept in the directory and
// lives for this process alone.
func identify(r *config.Runner, report io.Writer, verb string, fd *accesskey.Key) (*serverIdentity, error) {
	dir := machineDir()
	key, err := readAccessKey(dir, fd)
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
		text = fmt.Sprintf("the server refused a request signed with the access key %s: it does not know the key, has revoked it, or this machine's clock is more than five minutes off; check the clock, else enrol a new key with qory access-key enrol", fingerprint)
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

// codeServerNeedsWall is the refusal of an unwalled run while the machine keeps stored
// secrets: the server's discovery lists them, or the stored-secrets marker exists.
const codeServerNeedsWall = "server_needs_wall"

// needsWall is the server_needs_wall refusal, saying why.
func needsWall(dir runnerdir.Dir, listed bool) error {
	why := "the stored-secrets marker " + dir.Path(runnerdir.MarkerFile) + " exists: this machine's access key may receive stored secrets"
	if listed {
		why = "the server lists stored secrets for this machine's access key"
	}
	return &refusedError{
		text: fmt.Sprintf("%s, so every run needs a wall: --wall %s, or wall in %s", why, config.WallDocker, config.RunnerFileName),
		err:  &session.Refusal{Code: codeServerNeedsWall},
	}
}

// startRun is the start of every run, before anything is started: under the key lock
// held shared, it refuses an unwalled run that contacts no server while the
// stored-secrets marker exists, then creates and holds the run's own lock file, naming
// whether it is walled, and drops the key lock. A key command, which holds the key lock
// exclusively, so waits for starting runs, and they for it. In a directory that cannot
// be written no key command can work either, and the run goes on without the locks.
func startRun(dir runnerdir.Dir, runID string, walled, noServer bool) (*runnerdir.Lock, error) {
	if dir == "" {
		return nil, nil
	}
	key, err := dir.LockKey(false)
	if err != nil && !errors.Is(err, runnerdir.ErrReadOnly) {
		return nil, fmt.Errorf("the key lock: %w", err)
	}
	defer key.Release()
	// A marker that cannot be looked at counts as one.
	marker, _ := dir.HasMarker()
	if marker && noServer && !walled {
		return nil, needsWall(dir, false)
	}
	if key == nil {
		return nil, nil
	}
	return dir.LockRun(runID, walled)
}

// discovered is what a run against the server does once the server's signed discovery
// is read, before the ping: it prints the node. When the run's secret is the one in
// access-key-secret, still there, it then, under the key lock held exclusively, deletes
// every secret moved aside, since the key's signed discovery succeeded; writes the
// stored-secrets marker when discovery lists secrets, a marker it cannot write being no
// run; and removes it when discovery lists none and that secret is the directory's only
// one. An unwalled run is refused, server_needs_wall, when discovery lists secrets or
// the marker still exists.
func discovered(dir runnerdir.Dir, id *serverIdentity, walled bool, report io.Writer) func(session.Discovery) error {
	return func(d session.Discovery) error {
		fmt.Fprintf(report, "qory run: node %s, instance %s\n", d.NodeID, id.instanceID)
		if id.key.source == fromFile {
			if err := settleMarker(dir, id.key.key, d.Secrets); err != nil {
				return err
			}
		}
		if walled {
			return nil
		}
		if d.Secrets {
			return needsWall(dir, true)
		}
		if marker, _ := dir.HasMarker(); marker {
			return needsWall(dir, false)
		}
		return nil
	}
}

// settleMarker is the marker rule of a signed discovery under the secret of
// access-key-secret, under the key lock held exclusively.
func settleMarker(dir runnerdir.Dir, key *accesskey.Key, secrets bool) error {
	lock, err := dir.LockKey(true)
	if errors.Is(err, runnerdir.ErrReadOnly) {
		if secrets {
			if err := dir.WriteMarker(); err != nil {
				return err
			}
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("the key lock: %w", err)
	}
	defer lock.Release()
	if !dir.SameSecret(key) {
		return nil
	}
	if err := dir.DeleteOldSecrets(); err != nil {
		return fmt.Errorf("delete the secrets moved aside: %w", err)
	}
	if secrets {
		return dir.WriteMarker()
	}
	if old, err := dir.OldSecrets(); err == nil && len(old) == 0 {
		return dir.RemoveMarker()
	}
	return nil
}

// newRunID returns a new run id, a UUID version 7 in the canonical lower-case form, so
// the run's lock file is named before the runner starts.
func newRunID() string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
