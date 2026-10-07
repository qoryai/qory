package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/runner/accesskey"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/runnerdir"
)

// The seams of the key commands a test replaces: the key's source, the clock, and the
// HTTP client of the enrolment, nil for the runner's own.
var (
	generateKey = accesskey.Generate
	keyClock    = time.Now
	enrolClient *http.Client
)

// newAccessKeyCommand builds the access-key group: enrol a new key with a code, or make
// one whose public key an administrator pastes.
func newAccessKeyCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "access-key",
		Short: "Make this machine's access key for its server",
		Long: `Make the access key every run signs its requests to the server with.

The key is an Ed25519 key. Its secret stays on this machine, in access-key-secret beside
` + config.RunnerFileName + ` in ~/.config/qory, and the server keeps only its public key. A key is
never rotated: a new one is enrolled, approved, and the old one revoked.

  enrol   enrol a new key with a code from the server
  create  make a key whose public key an administrator pastes into the server

With --print, either command writes no key or setting on this machine and prints the
key for a CI's settings instead.

More: https://github.com/qoryai/qory/blob/main/docs/run.md`,
	}
	c.AddCommand(newAccessKeyEnrol(), newAccessKeyCreate())
	return c
}

// newAccessKeyEnrol builds the enrol verb.
func newAccessKeyEnrol() *cobra.Command {
	var print bool
	c := &cobra.Command{
		Use:   "enrol <server> <code>",
		Short: "Enrol a new access key with a code from the server",
		Long: `Enrol a new access key for this machine with an enrolment code an owner or
administrator of the server created. The code is valid for 15 minutes and used once.

qory makes the key, keeps its secret in access-key-secret, prints its fingerprint and
sends the server the public key. The server's signed answer gives the key its id, which
qory writes into the server section of ` + config.RunnerFileName + `, with the server's URL and its
public key where the section has none yet. The key then awaits approval: an owner or
administrator compares the fingerprint qory printed with the one the server shows, and
until then every run is refused with key_pending.

A secret already here is moved aside first, and deleted once the new key is approved
and a run uses it. Run the same command again within 15 minutes and it retries with the same key.

The key's name is instance.name of ` + config.RunnerFileName + `, else this machine's host name.

--print writes no key or setting and prints QORY_ACCESS_KEY_ID, QORY_ACCESS_KEY_SECRET
and QORY_APIARY_PUBLIC_KEY for a CI's settings. Only the secret belongs in its secret
store. The key is for another machine, so the server and the pin of ` + config.RunnerFileName + ` do not
apply; the code is checked against QORY_APIARY_PUBLIC_KEY when it is set.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md`,
		Example: `  qory access-key enrol https://apiary.example qec_F1XT-0RE0-0000-0000-0000-0000-00.uoES-kuj1vk0sq0qoGlmAg
  qory access-key enrol --print https://apiary.example "$code"   # for a CI's settings`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return input(fmt.Errorf("qory access-key enrol takes the server and the code"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return enrol(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1], print)
		},
	}
	c.Flags().BoolVar(&print, "print", false, "write no key or setting; print the key's three settings for a CI")
	return c
}

// newAccessKeyCreate builds the create verb.
func newAccessKeyCreate() *cobra.Command {
	var print bool
	c := &cobra.Command{
		Use:   "create",
		Short: "Make an access key whose public key an administrator pastes into the server",
		Long: `Make a new access key for this machine and print its public key and fingerprint.

An owner or administrator of the server pastes the public key into an existing node or
node pool, where it is approved at once. Its page then shows the lines for the server
section of ` + config.RunnerFileName + `: server.url, server.access_key_id and server.apiary_public_key.

The secret goes into access-key-secret beside ` + config.RunnerFileName + `. When that file exists,
create refuses: move it aside yourself first.

--print writes no key or setting and prints QORY_ACCESS_KEY_SECRET for a CI's secret
store.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md`,
		Example: `  qory access-key create
  qory access-key create --print   # for a CI's secret store`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return create(cmd.OutOrStdout(), cmd.ErrOrStderr(), print)
		},
	}
	c.Flags().BoolVar(&print, "print", false, "write no key or setting; print the key's secret for a CI")
	return c
}

// keyEnv returns one of the access key's variables, QORY_ACCESS_KEY_ID,
// QORY_ACCESS_KEY_SECRET or QORY_APIARY_PUBLIC_KEY, as qory took it when it started:
// the one place the key commands read them.
func keyEnv(name string) string {
	v := config.TakenServerVariables()
	switch name {
	case accesskey.EnvID:
		return v.AccessKeyID
	case accesskey.EnvSecret:
		return v.AccessKeySecret
	case accesskey.EnvPin:
		return v.ApiaryPublicKey
	}
	return ""
}

// userAgent is the User-Agent of every request to the server, the enrolment's included.
func userAgent() string { return "qory-runner/" + build().title() }

// makeKey generates a new access key, refusing one of the contract's published fixture
// keys.
func makeKey() (*accesskey.Key, error) {
	k, err := generateKey()
	if err != nil {
		return nil, err
	}
	if k.PublicKey().Fixture() {
		return nil, errors.New("the new key is one of the runner contract's published fixture keys, whose secret anyone can read; no key was kept")
	}
	return k, nil
}

// refuseKeyEnv refuses a key command that keeps its key on this machine while the
// environment sets the access key's id, its secret or the pin: the id and the pin in
// runner.yaml too would stop every run on a value set in both, and a secret in the
// environment would win over the one the command keeps.
func refuseKeyEnv(verb string) error {
	var set []string
	for _, name := range []string{accesskey.EnvID, accesskey.EnvSecret, accesskey.EnvPin} {
		if keyEnv(name) != "" {
			set = append(set, name)
		}
	}
	if len(set) == 0 {
		return nil
	}
	names, is, they, them := set[0], "is", "it", "it"
	if len(set) > 1 {
		names = strings.Join(set[:len(set)-1], ", ") + " and " + set[len(set)-1]
		is, they, them = "are", "they", "them"
	}
	return input(fmt.Errorf("%s %s set, and qory access-key %s keeps the key in this machine's files, which %s would contradict: unset %s, or use --print", names, is, verb, they, them))
}

// checkOrigin refuses a server the enrolment cannot reach, before a key is made: runner's
// enrolment posts over https, or over http to localhost, 127.0.0.1 or [::1] alone.
func checkOrigin(server string) error {
	u, err := url.Parse(server)
	if err != nil {
		return err
	}
	if h := u.Hostname(); u.Scheme == "http" && h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return fmt.Errorf("the server %s is http to a host other than localhost, 127.0.0.1 or [::1], which enrolment does not reach; use https", server)
	}
	return nil
}

// keyLock takes the key lock exclusively and refuses while a run without a wall is
// live. A directory that cannot be written is no key for a command that writes, and
// for --print no lock: no run can hold a lock file there either.
func keyLock(dir runnerdir.Dir, print bool) (*runnerdir.Lock, error) {
	lock, err := dir.LockKey(true)
	if errors.Is(err, runnerdir.ErrReadOnly) {
		if print {
			return nil, nil
		}
		return nil, fmt.Errorf("%s cannot be written, so no key can be kept there; use --print for a key kept elsewhere", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("the key lock: %w", err)
	}
	unwalled, err := dir.Sweep()
	if err != nil {
		lock.Release()
		return nil, fmt.Errorf("the run locks: %w", err)
	}
	if len(unwalled) > 0 {
		lock.Release()
		return nil, input(fmt.Errorf("a run without a wall is running on this machine (%s): it must end before a key is made", strings.Join(unwalled, ", ")))
	}
	return lock, nil
}

// prepareDir makes the runner file's directory mode 0700, saying so when it changes
// it, and writes the stored-secrets marker: a marker that cannot be written is no key.
func prepareDir(dir runnerdir.Dir, out io.Writer) error {
	changed, err := dir.Ensure()
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(out, "%s is now mode 0700: it holds the access key's secret\n", dir)
	}
	return nil
}

// effectivePin is the pin a code is checked against, nil when there is none: the
// runner file's, else QORY_APIARY_PUBLIC_KEY. With --print the key is for another
// machine, so the runner file's is not, and the variable's alone is. A pin that lists a
// published fixture key is refused.
func effectivePin(r *config.Runner, print bool) (accesskey.Pin, error) {
	if !print && r != nil && r.Server != nil && len(r.Server.Pin) > 0 {
		return r.Server.Pin, nil
	}
	v := keyEnv(accesskey.EnvPin)
	if v == "" {
		return nil, nil
	}
	p, err := accesskey.ParsePin([]byte(v))
	if err == nil && p.Fixture() {
		err = errors.New("it lists the runner contract's published fixture key, whose secret anyone can read; pin your server's own key")
	}
	if err != nil {
		return nil, input(fmt.Errorf("%s: %w", accesskey.EnvPin, err))
	}
	return p, nil
}

// enrol is qory access-key enrol: it makes a key, or for a retry of the same code takes
// the one made for it, posts the enrolment and acts on the signed answer.
func enrol(ctx context.Context, out, errOut io.Writer, rawServer, rawCode string, print bool) error {
	server := strings.TrimSuffix(rawServer, "/")
	if err := config.CheckServerURL(server); err != nil {
		return input(err)
	}
	if err := checkOrigin(server); err != nil {
		return input(err)
	}
	code, err := accesskey.NormaliseCode(rawCode)
	if err != nil {
		return input(err)
	}
	if !print {
		if err := refuseKeyEnv("enrol"); err != nil {
			return err
		}
	}
	// With --print the key is for another machine: its server section does not apply,
	// so neither stops the command, and instance.name alone is read.
	load := config.LoadRunner
	if print {
		load = config.LoadRunnerInstance
	}
	r, err := load()
	if err != nil {
		return input(err)
	}
	if !print && r != nil && r.Server != nil && r.Server.URL != server {
		return input(fmt.Errorf("%s names the server %s, and this command %s: enrol with the server %s names, or change its server.url first", r.File, r.Server.URL, server, config.RunnerFileName))
	}
	pin, err := effectivePin(r, print)
	if err != nil {
		return err
	}
	if err := pin.CheckCode(code); err != nil {
		return input(err)
	}
	name := r.InstanceNameOrDefault()
	if name == "" {
		return input(fmt.Errorf("this machine's host name does not fit an access key's name: set instance.name in %s", config.RunnerFileName))
	}
	if err := accesskey.CheckName(name); err != nil {
		return input(fmt.Errorf("instance.name: %w", err))
	}
	dir := machineDir()
	if dir == "" {
		return fmt.Errorf("no configuration directory: set HOME or XDG_CONFIG_HOME")
	}
	// info is where the lines for a person go, stderr, so stdout carries the printed
	// settings alone.
	info := errOut
	if !print {
		if err := prepareDir(dir, info); err != nil {
			return err
		}
	}
	lock, err := keyLock(dir, print)
	if err != nil {
		return err
	}
	defer lock.Release()
	if !print {
		if err := dir.WriteMarker(); err != nil {
			return fmt.Errorf("%w; no key was made", err)
		}
	}
	now := keyClock()
	var key *accesskey.Key
	switch {
	case print:
		if key, err = makeKey(); err != nil {
			return err
		}
	case dir.Pending(code, now) && dir.HasSecret():
		if key, err = dir.ReadSecret(); err != nil {
			return err
		}
		fmt.Fprintln(info, "this code was tried within the last 15 minutes: retrying with the key made for it")
	default:
		moved, err := dir.MoveAside(now)
		if err != nil {
			return err
		}
		if moved != "" {
			fmt.Fprintf(info, "moved the access key secret that was here aside: %s\n", moved)
		}
		if key, err = makeKey(); err != nil {
			return err
		}
		if err := dir.WriteSecret(key); err != nil {
			return err
		}
		if err := dir.WritePending(code, now); err != nil {
			return err
		}
	}
	fingerprint := key.Fingerprint()
	fmt.Fprintf(info, "access key fingerprint %s: compare it with the one the server shows\n", fingerprint)
	req, err := accesskey.NewEnrolmentRequest(key, code, name, now)
	if err != nil {
		return input(err)
	}
	ans, err := req.Post(ctx, enrolClient, server, userAgent())
	if err != nil {
		return enrolFailed(dir, err, fingerprint, print, now)
	}
	if ans.Pin.Fixture() {
		return errors.New("the server's answer lists the runner contract's published fixture key, whose secret anyone can read: it is no server to pin; nothing was written")
	}
	kind := "node"
	if ans.NodeKind == "pool" {
		kind = "node pool"
	}
	fmt.Fprintf(info, "enrolled as %s in the %s %s\n", ans.AccessKeyID, kind, ans.NodeID)
	fmt.Fprintf(info, "stored secrets: %s\n", yesNo(ans.StoredSecrets))
	if ans.Approved {
		fmt.Fprintln(info, "approved: runs can start")
	} else {
		fmt.Fprintf(info, "awaiting approval: an owner or administrator of the server compares the fingerprint %s with the one the server shows and approves the key; until then every run is refused with key_pending\n", fingerprint)
	}
	if print {
		pinJSON, err := json.Marshal(ans.Pin)
		if err != nil {
			return err
		}
		fmt.Fprintf(info, "Only %s belongs in the CI's secret store; %s and %s are plain settings. No key or setting was written on this machine.\n", accesskey.EnvSecret, accesskey.EnvID, accesskey.EnvPin)
		fmt.Fprintf(out, "%s=%s\n%s=%s\n%s=%s\n", accesskey.EnvID, ans.AccessKeyID, accesskey.EnvSecret, key.Secret(), accesskey.EnvPin, pinJSON)
		return nil
	}
	e := config.Enrolment{URL: server, AccessKeyID: ans.AccessKeyID}
	if len(pin) == 0 {
		e.Pin = ans.Pin
	}
	path := filepath.Join(string(dir), config.RunnerFileName)
	if err := config.WriteEnrolment(path, e); err != nil {
		return fmt.Errorf("the key is enrolled, and %s could not be written: %w; run the same command again within 15 minutes", path, err)
	}
	if err := dir.RemovePending(); err != nil {
		return err
	}
	written := "server.access_key_id"
	if r == nil || r.Server == nil {
		written = "server.url, " + written
	}
	if e.Pin != nil {
		written += " and server.apiary_public_key"
	}
	fmt.Fprintf(info, "wrote %s to %s\n", written, path)
	return nil
}

// enrolFailed acts on an enrolment that got no 201: on the refusal's code alone, never
// on an HTTP status. unauthorized and key_invalid move the secret made for the code
// aside and end the pending enrolment; key_limit, an unsigned answer and an answer that
// never came keep both, so the same command within 15 minutes retries with the same
// key. --print kept nothing, so a lost answer cannot be retried.
func enrolFailed(dir runnerdir.Dir, err error, fingerprint string, print bool, now time.Time) error {
	var ref *accesskey.Refusal
	if !errors.As(err, &ref) {
		if print {
			return fmt.Errorf("the enrolment did not complete: %w; a --print enrolment cannot be retried: get a new code, and have your administrator reject the key %s should it await approval", err, fingerprint)
		}
		return fmt.Errorf("the enrolment did not complete: %w; run the same command again within 15 minutes and it retries with the same key", err)
	}
	// discard moves the secret made for the code aside and ends the pending enrolment.
	discard := func() string {
		if print {
			return ""
		}
		dir.RemovePending()
		moved, err := dir.MoveAside(now)
		if err != nil || moved == "" {
			return ""
		}
		return "; the secret made for it was moved aside to " + moved
	}
	var text string
	switch ref.Code {
	case accesskey.CodeUnauthorized:
		text = "this code was used or has expired; if you did not use it, tell your administrator, who must reject the pending key. Enrolling needs a new code" + discard()
	case accesskey.CodeKeyInvalid:
		names := ""
		if len(ref.Names) > 0 {
			names = " (" + strings.Join(ref.Names, ", ") + ")"
		}
		text = "the server refused the key" + names + ". Enrolling needs a new code" + discard()
	case accesskey.CodeKeyLimit:
		text = "the node already holds a key awaiting approval, or two approved keys: once an owner or administrator has revoked or rejected one, the same command, run within the code's 15 minutes, succeeds"
	case accesskey.CodeAnswerUnsigned:
		text = fmt.Sprintf("the server did not enrol the key (HTTP %d, unsigned); try again later", ref.Status)
		if print {
			text += "; a --print enrolment cannot be retried: get a new code, and have your administrator reject the key " + fingerprint + " should it await approval"
		}
	default:
		return err
	}
	return &refusedError{text: text, err: err}
}

// create is qory access-key create: a new key whose public key an administrator pastes
// into the server.
func create(out, errOut io.Writer, print bool) error {
	if !print {
		if err := refuseKeyEnv("create"); err != nil {
			return err
		}
	}
	dir := machineDir()
	if dir == "" {
		return fmt.Errorf("no configuration directory: set HOME or XDG_CONFIG_HOME")
	}
	info := out
	if print {
		info = errOut
	} else if err := prepareDir(dir, info); err != nil {
		return err
	}
	lock, err := keyLock(dir, print)
	if err != nil {
		return err
	}
	defer lock.Release()
	if !print {
		if dir.HasSecret() {
			return input(fmt.Errorf("%s already holds this machine's access key secret, and create does not replace it: move it aside yourself first, or use --print for a key kept elsewhere", dir.Path(runnerdir.SecretFile)))
		}
		if err := dir.WriteMarker(); err != nil {
			return fmt.Errorf("%w; no key was made", err)
		}
	}
	key, err := makeKey()
	if err != nil {
		return err
	}
	if !print {
		if err := dir.WriteSecret(key); err != nil {
			return err
		}
	}
	fmt.Fprintf(info, "public key %s\n", key.PublicKey())
	fmt.Fprintf(info, "fingerprint %s\n", key.Fingerprint())
	fmt.Fprintf(info, "An owner or administrator of the server pastes the public key into the node or node pool, where it is approved at once; its page then shows the server lines for %s.\n", config.RunnerFileName)
	if print {
		fmt.Fprintf(info, "Only %s belongs in the CI's secret store. No key or setting was written on this machine.\n", accesskey.EnvSecret)
		fmt.Fprintf(out, "%s=%s\n", accesskey.EnvSecret, key.Secret())
		return nil
	}
	fmt.Fprintf(info, "wrote the secret to %s\n", dir.Path(runnerdir.SecretFile))
	return nil
}

// yesNo is a flag as a person reads it.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
