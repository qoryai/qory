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

// The seams of the key commands a test replaces: the key's source, the clock, the
// HTTP client of the enrolment, nil for the runner's own, and what stops an enrolment
// after one of the steps that follow the server's answer, as a crash would.
var (
	generateKey = accesskey.Generate
	keyClock    = time.Now
	enrolClient *http.Client
	afterStep   = func(step string) error { return nil }
)

// newAccessKeyCommand builds the access-key group: enrol a new key with a code.
func newAccessKeyCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "access-key",
		Short: "Make this machine's access key for its server",
		Long: `Make the access key every run signs its requests to the server with.

The key is an Ed25519 key. Its secret stays on this machine, in access-key-secret beside
` + config.RunnerFileName + ` in ~/.config/qory, and the server keeps only its public key. A key is
never rotated: a new one is enrolled, and the old one revoked.

  enrol   enrol a new key with a code from the server

With --print, enrol writes no key or setting on this machine and prints the key for a
CI's settings instead.

More: https://github.com/qoryai/qory/blob/main/docs/run.md`,
	}
	c.AddCommand(newAccessKeyEnrol())
	return c
}

// newAccessKeyEnrol builds the enrol verb.
func newAccessKeyEnrol() *cobra.Command {
	var print, replace bool
	c := &cobra.Command{
		Use:   "enrol <server> <code>",
		Short: "Enrol a new access key with a code from the server",
		Long: `Enrol a new access key for this machine with an enrolment code an owner or
administrator of the server created. The code is valid for 15 minutes and used once.

qory makes the key, keeps its secret in access-key-secret, prints its fingerprint and
sends the server the public key. The server's signed answer gives the key its id, which
qory writes into the server section of ` + config.RunnerFileName + `, with the server's URL and its
public key where the section has none yet. The key is active from that answer on: runs
can start. qory keeps the answer in enrolment-answer until the enrolment is finished:
should the command stop after the answer came, the same command finishes it on this
machine, at any time, without asking the server again.

When access-key-secret exists, enrol refuses, so it never replaces this machine's key
unasked: use --replace to move this machine to a new key, or --print for a key kept
elsewhere. The one exception is a retry: run the same command again within the code's
15 minutes, while access-key-secret still holds the key made for it, and it retries
with that key.

--replace moves this machine to a new key, and the old key stays in use until the new
one is active. qory makes the new key in access-key-secret.new and enrols it, leaving
access-key-secret and ` + config.RunnerFileName + ` as they are, so an enrolment that fails leaves the
old key working. Once the server's signed answer has come, the new secret takes the old
one's place, ` + config.RunnerFileName + ` names the new key, and the old secret is removed. The old
key still works on the server until an owner or administrator revokes it on the node's
page, unless it is revoked already. Before the server's answer, the same command within
the code's 15 minutes retries; after it, the same command finishes the replacement on
this machine, at any time. On a machine without a key, --replace enrols as the command
does without it.

The key's name is instance.name of ` + config.RunnerFileName + `, else this machine's host name.

--print writes no key or setting and prints QORY_ACCESS_KEY_ID, QORY_ACCESS_KEY_SECRET
and QORY_APIARY_PUBLIC_KEY for a CI's settings. Only the secret belongs in its secret
store. The key is for another machine, so the server and the pin of ` + config.RunnerFileName + ` do not
apply; the code is checked against QORY_APIARY_PUBLIC_KEY when it is set.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md`,
		Example: `  qory access-key enrol https://apiary.example qec_F1XT-0RE0-0000-0000-0000-0000-00.uoES-kuj1vk0sq0qoGlmAg
  qory access-key enrol --replace https://apiary.example "$code"   # move this machine to a new key
  qory access-key enrol --print https://apiary.example "$code"   # for a CI's settings`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return input(fmt.Errorf("qory access-key enrol takes the server and the code"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if replace && print {
				return input(errors.New("--replace and --print do not go together: --print keeps nothing on this machine, so it replaces nothing"))
			}
			return enrol(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1], print, replace)
		},
	}
	c.Flags().BoolVar(&print, "print", false, "write no key or setting; print the key's three settings for a CI")
	c.Flags().BoolVar(&replace, "replace", false, "move this machine to a new key; the old key stays in use until the new one is active")
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

// refuseKeyEnv refuses an enrolment that keeps its key on this machine while the
// environment sets the access key's id, its secret or the pin: the id and the pin in
// runner.yaml too would stop every run on a value set in both, and a secret in the
// environment would win over the one the command keeps.
func refuseKeyEnv() error {
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
	return input(fmt.Errorf("%s %s set, and qory access-key enrol keeps the key in this machine's files, which %s would contradict: unset %s, or use --print", names, is, they, them))
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
// the one made for it, posts the enrolment and acts on the signed answer. Any other
// secret already here stops it, unless replace is set: then the new key is made in
// access-key-secret.new, and takes the place of the secret here only once the server's
// signed answer has come, see [replaceKey].
func enrol(ctx context.Context, out, errOut io.Writer, rawServer, rawCode string, print, replace bool) error {
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
		if err := refuseKeyEnv(); err != nil {
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
	now := keyClock()
	// An enrolment the server has answered already is finished on this machine: the
	// server refuses a code it used and a key it enrolled, so nothing posts it again.
	if !print {
		k, ans, err := answerKept(dir, server, code)
		if err != nil {
			return err
		}
		if k != nil {
			if err := dir.WriteMarker(); err != nil {
				return err
			}
			fmt.Fprintf(info, "access key fingerprint %s\n", k.Fingerprint())
			printEnrolled(info, ans)
			return finishEnrolment(dir, info, r, server, pin, k, ans)
		}
	}
	// retry is the same code again within its 15 minutes while access-key-secret still
	// holds the key made for it, which the retry takes. Any other secret here is this
	// machine's key, which enrol replaces only with replace set: replacing.
	var key *accesskey.Key
	var retry, replacing bool
	if !print {
		if dir.HasSecret() {
			held, err := dir.ReadSecret()
			switch {
			case err == nil && dir.Pending(code, held.PublicKey(), now):
				key, retry = held, true
			case !replace:
				return input(fmt.Errorf("%s already holds this machine's access key secret: to move this machine to a new key, run qory access-key enrol --replace %s <code>; for a key kept elsewhere, use --print", dir.Path(runnerdir.SecretFile), server))
			default:
				replacing = true
			}
		}
		if err := dir.WriteMarker(); err != nil {
			return fmt.Errorf("%w; no key was made", err)
		}
	}
	switch {
	case print:
		if key, err = makeKey(); err != nil {
			return err
		}
	case retry:
		fmt.Fprintln(info, "this code was tried within the last 15 minutes: retrying with the key made for it")
	case replacing:
		// The same code again within its 15 minutes retries with the new key made for
		// it. Any other new key here is one an earlier --replace made for another code,
		// or longer ago, and did not put in place: it is moved aside, as a key a refused
		// enrolment made is.
		if staged, err := dir.ReadNewSecret(); err == nil && dir.Pending(code, staged.PublicKey(), now) {
			key = staged
			fmt.Fprintln(info, "this code was tried within the last 15 minutes: retrying with the key made for it")
			break
		}
		if dir.HasNewSecret() {
			// A key the server answered for another code may be active: it stays.
			if staged, err := dir.ReadNewSecret(); err == nil && answeredFor(dir, staged.PublicKey()) {
				return fmt.Errorf("%s holds the new key of an enrolment the server answered for another code: run that command again to finish it", dir.Path(runnerdir.NewSecretFile))
			}
			moved, err := dir.MoveNewAside(now)
			if err != nil {
				return fmt.Errorf("%w; no key was made", err)
			}
			fmt.Fprintf(info, "moved the new key of an earlier --replace, made for another code or more than 15 minutes ago, aside to %s\n", moved)
		}
		if key, err = makeKey(); err != nil {
			return err
		}
		if err := dir.WriteNewSecret(key); err != nil {
			return err
		}
		if err := dir.WritePending(code, key.PublicKey(), now); err != nil {
			return err
		}
	default:
		if key, err = makeKey(); err != nil {
			return err
		}
		if err := dir.WriteSecret(key); err != nil {
			return err
		}
		if err := dir.WritePending(code, key.PublicKey(), now); err != nil {
			return err
		}
	}
	fingerprint := key.Fingerprint()
	fmt.Fprintf(info, "access key fingerprint %s\n", fingerprint)
	req, err := accesskey.NewEnrolmentRequest(key, code, name, now)
	if err != nil {
		return input(err)
	}
	client, recorder := recordingClient()
	ans, err := req.Post(ctx, client, server, userAgent())
	if err != nil {
		return enrolFailed(dir, err, key, print, now)
	}
	if ans.Pin.Fixture() {
		return refuseFixturePin(dir, key, print, now)
	}
	printEnrolled(info, ans)
	if print {
		pinJSON, err := json.Marshal(ans.Pin)
		if err != nil {
			return err
		}
		fmt.Fprintf(info, "Only %s belongs in the CI's secret store; %s and %s are plain settings. No key or setting was written on this machine.\n", accesskey.EnvSecret, accesskey.EnvID, accesskey.EnvPin)
		fmt.Fprintf(out, "%s=%s\n%s=%s\n%s=%s\n", accesskey.EnvID, ans.AccessKeyID, accesskey.EnvSecret, key.Secret(), accesskey.EnvPin, pinJSON)
		return nil
	}
	// The answer is kept before anything here changes, so the same command finishes
	// the enrolment from it after a stop. An answer that cannot be kept stops nothing:
	// the steps that follow then run without it.
	if record, err := recorder.record(server, req); err == nil && keepAnswer(dir, record) == nil {
		if err := afterStep("answer"); err != nil {
			return err
		}
	}
	return finishEnrolment(dir, info, r, server, pin, key, ans)
}

// printEnrolled prints what the server's verified 201 says.
func printEnrolled(info io.Writer, ans *accesskey.EnrolmentAnswer) {
	kind := "node"
	if ans.NodeKind == "pool" {
		kind = "node pool"
	}
	fmt.Fprintf(info, "enrolled as %s in the %s %s\n", ans.AccessKeyID, kind, ans.NodeID)
	fmt.Fprintf(info, "stored secrets: %s\n", yesNo(ans.StoredSecrets))
	fmt.Fprintln(info, "the key is active: runs can start")
}

// finishEnrolment is what follows the server's verified 201 for key on this machine,
// the first time or from the answer enrolment-answer keeps: a new key of --replace,
// still in access-key-secret.new, takes the place of access-key-secret, see
// [replaceKey]; the runner file is written; access-key-secret.replaced, the pending
// enrolment and the kept answer are removed, in that order.
func finishEnrolment(dir runnerdir.Dir, info io.Writer, r *config.Runner, server string, pin accesskey.Pin, key *accesskey.Key, ans *accesskey.EnrolmentAnswer) error {
	e := config.Enrolment{URL: server, AccessKeyID: ans.AccessKeyID}
	if len(pin) == 0 {
		e.Pin = ans.Pin
	}
	path := filepath.Join(string(dir), config.RunnerFileName)
	if dir.SameNewSecret(key) {
		if err := replaceKey(dir); err != nil {
			return err
		}
	}
	if err := config.WriteEnrolment(path, e); err != nil {
		return fmt.Errorf("the key is enrolled, and %s could not be written: %w; run the same command again", path, err)
	}
	if err := afterStep("runner"); err != nil {
		return err
	}
	// The secret a replacement put aside: what the old key is named by, then removed.
	var old string
	if dir.HasReplaced() {
		old = oldKeyName(dir, r, ans.AccessKeyID)
		if err := dir.RemoveReplaced(); err != nil {
			return fmt.Errorf("the new key is in place, and %w; run the same command again", err)
		}
		if err := afterStep("unlinked"); err != nil {
			return err
		}
	}
	if err := dir.RemovePending(); err != nil {
		return err
	}
	if err := afterStep("pending"); err != nil {
		return err
	}
	if err := dir.RemoveAnswer(); err != nil {
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
	if old != "" {
		fmt.Fprintf(info, "revoke the old key %s on the node's page, unless it is revoked already: until then it still works on the server\n", old)
	}
	return nil
}

// replaceKey puts the new key of --replace in place of access-key-secret, once the
// server's signed answer has made it active and enrolment-answer keeps that answer; the
// caller then writes the runner file and removes access-key-secret.replaced, the
// pending enrolment and the kept answer. Every step is one rename, link or unlink, and
// the directory is synced after each, so a crash between two leaves one of these
// states, each of which the same command finishes from the kept answer, at any time and
// without asking the server, which would refuse the used code:
//
//   - before access-key-secret.replaced is made, or after it is made: access-key-secret
//     and the runner file still hold the old key, which runs keep using; .new holds the
//     key the answer names, and the finish starts with this swap, removing a .replaced
//     left over first.
//   - after access-key-secret.new takes the place of access-key-secret, before the
//     runner file is written: runs fail, the new secret beside the old key's id, and
//     .replaced holds the old secret. The finish writes the runner file and removes
//     .replaced.
//   - after the runner file names the new key: runs use the new key; the finish removes
//     .replaced.
//   - after .replaced is removed, or the pending enrolment: the finish removes what is
//     left.
//
// A .replaced that outlives all this goes with the next enrolment on this machine that
// completes, or as the swap of the next --replace starts.
func replaceKey(dir runnerdir.Dir) error {
	if err := dir.KeepReplaced(); err != nil {
		return fmt.Errorf("the new key is enrolled, and %w; the old key is still in place: run the same command again", err)
	}
	if err := afterStep("replaced"); err != nil {
		return err
	}
	if err := dir.PutNewSecret(); err != nil {
		return fmt.Errorf("the new key is enrolled, and %w; the old key is still in place: run the same command again", err)
	}
	return afterStep("secret")
}

// oldKeyName is how the success of a replacement names the old key: its id, as the
// runner file named it before the enrolment, else the fingerprint of the secret
// access-key-secret.replaced holds, else nothing.
func oldKeyName(dir runnerdir.Dir, r *config.Runner, newID string) string {
	if r != nil && r.Server != nil && r.Server.AccessKeyID != "" && r.Server.AccessKeyID != newID {
		return r.Server.AccessKeyID
	}
	if k, err := dir.ReadReplaced(); err == nil {
		return k.Fingerprint()
	}
	return ""
}

// discardKey ends the pending enrolment and moves the secret of key, the one made for
// the code, aside, returning where it went: access-key-secret.new for --replace, else
// access-key-secret. A secret access-key-secret holds of any other key is this
// machine's key, and stays: then nothing is moved and it returns "".
func discardKey(dir runnerdir.Dir, key *accesskey.Key, now time.Time) (string, error) {
	// A key the kept answer names may be active on the server: it stays.
	if answeredFor(dir, key.PublicKey()) {
		return "", nil
	}
	if err := dir.RemovePending(); err != nil {
		return "", err
	}
	if dir.SameNewSecret(key) {
		return dir.MoveNewAside(now)
	}
	if !dir.SameSecret(key) {
		return "", nil
	}
	return dir.MoveAside(now)
}

// refuseFixturePin refuses a 201 whose pin lists the runner contract's published
// fixture key. --print wrote nothing; otherwise the runner file is left as it is, the
// secret made for the code is moved aside and the pending enrolment ends, and the
// message names where that secret went.
func refuseFixturePin(dir runnerdir.Dir, key *accesskey.Key, print bool, now time.Time) error {
	const text = "the server's answer lists the runner contract's published fixture key, whose secret anyone can read: it is no server to pin; "
	if print {
		return errors.New(text + "nothing was written")
	}
	msg := text + config.RunnerFileName + " was not changed"
	moved, err := discardKey(dir, key, now)
	if err != nil {
		return fmt.Errorf("%s; %w", msg, err)
	}
	if moved != "" {
		msg += "; the secret made for it was moved aside to " + moved
	}
	return errors.New(msg)
}

// enrolFailed acts on an enrolment that got no 201: on the refusal's code alone, never
// on an HTTP status. unauthorized and key_invalid move the secret made for the code
// aside and end the pending enrolment; key_limit, rate_limited, an unsigned answer and
// an answer that never came keep both, so the same command within 15 minutes retries
// with the same key. --print kept nothing, so a lost answer cannot be retried.
func enrolFailed(dir runnerdir.Dir, err error, key *accesskey.Key, print bool, now time.Time) error {
	fingerprint := key.Fingerprint()
	var ref *accesskey.Refusal
	if !errors.As(err, &ref) {
		if print {
			return fmt.Errorf("the enrolment did not complete: %w; a --print enrolment cannot be retried: get a new code, and have your administrator revoke the key %s should it have been enrolled", err, fingerprint)
		}
		return fmt.Errorf("the enrolment did not complete: %w; run the same command again within 15 minutes and it retries with the same key", err)
	}
	// discard moves the secret made for the code aside and ends the pending enrolment.
	discard := func() string {
		if print {
			return ""
		}
		moved, err := discardKey(dir, key, now)
		if err != nil || moved == "" {
			return ""
		}
		return "; the secret made for it was moved aside to " + moved
	}
	var text string
	switch ref.Code {
	case accesskey.CodeUnauthorized:
		text = "this code was used or has expired; if you did not use it, tell your administrator, who must revoke the key it enrolled. Enrolling needs a new code" + discard()
	case accesskey.CodeKeyInvalid:
		names := ""
		if len(ref.Names) > 0 {
			names = " (" + strings.Join(ref.Names, ", ") + ")"
		}
		text = "the server refused the key" + names + ". Enrolling needs a new code" + discard()
	case accesskey.CodeKeyLimit:
		text = "the node already holds two keys: once an owner or administrator has revoked one, the same command, run within the code's 15 minutes, succeeds"
	case accesskey.CodeRateLimited:
		const text = "the server refused the attempt: this code was tried too often; run the same command again later, within the code's 15 minutes"
		return &refusedError{text: text, err: &accesskey.Refusal{Code: ref.Code}}
	case accesskey.CodeAnswerUnsigned:
		text = fmt.Sprintf("the server did not enrol the key (HTTP %d, unsigned); try again later", ref.Status)
		if print {
			text += "; a --print enrolment cannot be retried: get a new code, and have your administrator revoke the key " + fingerprint + " should it have been enrolled"
		}
	default:
		return err
	}
	return &refusedError{text: text, err: err}
}

// yesNo is a flag as a person reads it.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
