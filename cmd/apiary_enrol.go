package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/apiarydir"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
)

// kindDiagnostics is the node_kind of a diagnostics key, and the kind [accesskey.CodeKind]
// returns for a qecd_ code.
const kindDiagnostics = "diagnostics"

// useDiagnosticsCode is the fix of a code that is not a diagnostics enrolment code.
const useDiagnosticsCode = "Use a diagnostics enrolment code, qecd_..., from Instance settings > Diagnostics keys in Qory Apiary"

// getDiagnosticsCode is the fix of an enrolment whose code can no longer be used.
const getDiagnosticsCode = "Get a new diagnostics enrolment code from Instance settings > Diagnostics keys in Qory Apiary"

// codeKindWords is how the error for a code of another kind describes each kind.
var codeKindWords = map[string]string{"node": "a node code", "pool": "a node pool code", "gateway": "a gateway code", "adapter": "an integration code", "run": "a run code"}

// newApiaryEnrol builds qory apiary enrol.
func newApiaryEnrol() *cobra.Command {
	return &cobra.Command{
		Use:   "enrol <server> <code>",
		Short: "Enrol a diagnostics key with a code from Qory Apiary",
		Long: `Make a diagnostics key on this computer and enrol it with the Qory Apiary server at
<server>. <code> is a diagnostics enrolment code, qecd_..., which an instance admin
issues under Instance settings > Diagnostics keys in Qory Apiary. A code is valid for
15 minutes and works once.

qory makes the key in ~/.config/qory/apiary/<host> and sends Qory Apiary the public
key, with a proof that this computer has the secret. The response must be signed with
the server key whose fingerprint is in the code. qory then writes access-key-secret,
apiary.json with the server, the key id and that server key, and instance-id, which
qory apiary run diagnose reads. forager.yaml and its directory stay as they are.

If <host> has a diagnostics key already, enrol stops: remove the directory first.

If the command stops after Qory Apiary enrolled the key, run the same command again: qory
finishes the enrolment from the response it kept in enrolment-answer, without sending
the code again. If no response came, the same command within the code's 15 minutes
retries with the same key.

--verbose adds nothing here.

More: ` + apiaryDocs,
		Example: `  qory apiary enrol https://apiary.example qecd_F1XT-0RE0-0000-0000-0000-0000-00.uoES-kuj1vk0sq0qoGlmAg`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return input(errors.New("Pass the server and the code, such as qory apiary enrol https://apiary.example qecd_...: qory apiary enrol takes two arguments."))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return apiaryEnrol(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1])
		},
	}
}

// apiaryEnrol is qory apiary enrol: it makes a diagnostics key in the directory of the
// server at rawServer, enrols it with the code rawCode and stores it, as qory access-key
// enrol does for a node's key, with the same files for the steps between:
//
//  1. The new secret goes to access-key-secret.new, and enrolment-pending records the
//     SHA-256 of the code, the key's fingerprint and the time, before the request.
//  2. The verified 201 goes to enrolment-answer, with the request it answers, before
//     anything else changes.
//  3. access-key-secret.new is renamed to access-key-secret, apiary.json is written,
//     instance-id is made, and enrolment-pending and enrolment-answer are removed, in
//     that order.
//
// Should the command stop after step 2, the same command finishes from enrolment-answer
// without a request, since Qory Apiary refuses a used code. Should no response come, the
// same command within the code's 15 minutes retries with the key of step 1. If Qory
// Apiary refuses the code or the key, qory moves the secret of step 1 aside. Nothing
// goes to out but the result line, and nothing prints the code, the secret, the proof or
// a signature.
func apiaryEnrol(ctx context.Context, out, errOut io.Writer, rawServer, rawCode string) error {
	serverURL := strings.TrimSuffix(rawServer, "/")
	host, err := apiarydir.HostName(serverURL)
	if err != nil {
		return input(err)
	}
	code, err := accesskey.NormaliseCode(rawCode)
	if err != nil {
		return input(fmt.Errorf("%s: %w.", useDiagnosticsCode, err))
	}
	if kind, _ := accesskey.CodeKind(code); kind != kindDiagnostics {
		words, ok := codeKindWords[kind]
		if !ok {
			words = "a code"
		}
		prefix, _, _ := strings.Cut(code, "_")
		return input(fmt.Errorf("%s: this is %s (%s_), for another kind of key.", useDiagnosticsCode, words, prefix))
	}
	name := config.DefaultInstanceName()
	if accesskey.CheckName(name) != nil {
		return input(errors.New("Give this computer a host name of letters, digits, dots, underscores and dashes: Qory Apiary shows a diagnostics key by the host name of the computer that made it, and this host name does not fit."))
	}
	root, err := apiarydir.Root()
	if err != nil {
		return input(err)
	}
	dir, _, lock, err := apiarydir.Prepare(root, serverURL)
	if err != nil {
		return err
	}
	defer lock.Release()
	key, ans, err := keptDiagnosticsAnswer(dir, serverURL, code)
	if err != nil {
		return err
	}
	if key != nil {
		return finishDiagnosticsEnrolment(dir, out, serverURL, host, key, ans)
	}
	if apiarydir.HasKey(dir) {
		return input(fmt.Errorf("Remove %s to enrol a new diagnostics key for %s: the directory contains a diagnostics key already.\nThe old key stays active until an instance admin revokes it under Instance settings > Diagnostics keys.", dir, host))
	}
	now := keyClock()
	key, err = stageDiagnosticsKey(dir, errOut, code, now)
	if err != nil {
		return err
	}
	req, err := accesskey.NewEnrolmentRequest(key, code, name, now)
	if err != nil {
		return input(err)
	}
	client, recorder, err := recordingClient(nil)
	if err != nil {
		return err
	}
	ans, err = req.Post(ctx, client, serverURL, userAgent())
	var kindErr *accesskey.KindError
	if errors.As(err, &kindErr) {
		return discardOtherKind(dir, key, host, kindErr.Kind, kindErr.AccessKeyID)
	}
	if err != nil {
		return diagnosticsEnrolFailed(dir, err, key, serverURL, host, now)
	}
	if ans.NodeKind != kindDiagnostics {
		return discardOtherKind(dir, key, host, ans.NodeKind, ans.AccessKeyID)
	}
	if ans.Pin.Fixture() {
		msg := fmt.Sprintf("Check that %s is a Qory Apiary server: its response lists the published fixture key of the Forager contract, whose secret anyone can read, so qory stored no key.", serverURL)
		if moved, err := discardKey(dir, key, now); err != nil {
			return fmt.Errorf("%s\n%w", msg, err)
		} else if moved != "" {
			msg += "\nqory moved the secret made for the code aside to " + moved + "."
		}
		return errors.New(msg)
	}
	// The answer is kept before anything here changes, so the same command finishes the
	// enrolment from it after a stop. An answer that cannot be kept stops nothing: the
	// steps that follow then run without it, as they do for qory access-key enrol.
	if record, err := recorder.record(serverURL, req); err == nil && keepAnswer(dir, record) == nil {
		if err := afterStep("answer"); err != nil {
			return err
		}
	}
	return finishDiagnosticsEnrolment(dir, out, serverURL, host, key, ans)
}

// stageDiagnosticsKey returns the key to enrol with code: the one in
// access-key-secret.new when enrolment-pending records it for code within the last 15
// minutes, else a new key, written to access-key-secret.new with a new
// enrolment-pending. A secret in access-key-secret.new for another code, or from longer
// ago, is moved aside first, as qory access-key enrol moves it.
func stageDiagnosticsKey(dir foragerdir.Dir, errOut io.Writer, code string, now time.Time) (*accesskey.Key, error) {
	if staged, err := dir.ReadNewSecret(); err == nil && dir.Pending(code, staged.PublicKey(), now) {
		fmt.Fprintln(errOut, "qory apiary enrol: this code was tried within the last 15 minutes: retrying with the key made for it")
		return staged, nil
	}
	if dir.HasNewSecret() {
		moved, err := dir.MoveNewAside(now)
		if err != nil {
			return nil, fmt.Errorf("Check that %s can be written: %w; no key was made.", dir, err)
		}
		fmt.Fprintf(errOut, "qory apiary enrol: moved the secret of an earlier enrolment, for another code or more than 15 minutes ago, aside to %s\n", moved)
	}
	key, err := makeKey()
	if err != nil {
		return nil, err
	}
	if err := dir.WriteNewSecret(key); err != nil {
		return nil, fmt.Errorf("Check that %s can be written: %w.", dir, err)
	}
	if err := dir.WritePending(code, key.PublicKey(), now); err != nil {
		return nil, fmt.Errorf("Check that %s can be written: %w.", dir, err)
	}
	return key, nil
}

// keptDiagnosticsAnswer returns the key and the verified 201 of an enrolment of code with
// the server at serverURL to which Qory Apiary responded already, from enrolment-answer
// of dir and the secret in access-key-secret.new or access-key-secret. With no
// enrolment-answer it returns nil and no error. Every other case is an error whose first
// line is the fix, and nothing may then post the enrolment again: a response for another
// server URL or another code, a response whose key is in neither file, and a file that
// does not verify as a response.
func keptDiagnosticsAnswer(dir foragerdir.Dir, serverURL, code string) (*accesskey.Key, *accesskey.EnrolmentAnswer, error) {
	b, err := dir.ReadAnswer()
	if errors.Is(err, foragerdir.ErrNoAnswer) {
		return nil, nil, nil
	}
	path := dir.Path(foragerdir.AnswerFile)
	cannotFinish := func(why string) error {
		return fmt.Errorf("Remove %s, and enrol with a new code: qory cannot finish the enrolment from %s, because %s.\nThe key it enrolled stays active until an instance admin revokes it under Instance settings > Diagnostics keys.", dir, path, why)
	}
	if err != nil {
		return nil, nil, cannotFinish(fmt.Sprintf("the file does not read (%v)", err))
	}
	var s storedAnswer
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, nil, cannotFinish("the file is not a response of Qory Apiary")
	}
	if s.Server != serverURL {
		return nil, nil, fmt.Errorf("Run the earlier enrolment again with its server URL, %s, to finish it, or remove %s: %s is the response to an enrolment with that server URL.", exports.Value(s.Server, false), dir, path)
	}
	if s.Request.Code != code {
		return nil, nil, fmt.Errorf("Run the earlier enrolment again with its code to finish it, or remove %s: %s is the response to an enrolment with another code.", dir, path)
	}
	var key *accesskey.Key
	for _, read := range []func() (*accesskey.Key, error){dir.ReadNewSecret, dir.ReadSecret} {
		if k, err := read(); err == nil && k.PublicKey().String() == s.Request.PublicKey {
			key = k
			break
		}
	}
	if key == nil {
		return nil, nil, cannotFinish("the secret of the key it enrolled is in neither " + foragerdir.NewSecretFile + " nor " + foragerdir.SecretFile)
	}
	ans, err := s.verify()
	if err != nil {
		return nil, nil, cannotFinish(fmt.Sprintf("the response does not verify (%v)", err))
	}
	if ans.NodeKind != kindDiagnostics {
		return nil, nil, cannotFinish("the response enrolled another kind of key")
	}
	return key, ans, nil
}

// finishDiagnosticsEnrolment is what follows the verified 201 for key, the first time or
// from enrolment-answer: access-key-secret.new takes the place of access-key-secret when
// it has key, apiary.json is written with the server, the key id and the pin, instance-id
// is made, and enrolment-pending and enrolment-answer are removed. Each step can run
// again after a stop, so the same command finishes from any of them. It prints the result
// line to out.
func finishDiagnosticsEnrolment(dir foragerdir.Dir, out io.Writer, serverURL, host string, key *accesskey.Key, ans *accesskey.EnrolmentAnswer) error {
	again := func(err error) error {
		return fmt.Errorf("Run the same command again to finish the enrolment: Qory Apiary enrolled the key %s, and %w.", ans.AccessKeyID, err)
	}
	switch {
	case dir.SameNewSecret(key) && dir.HasSecret():
		return fmt.Errorf("Remove %s, and enrol with a new code: the file contains another key than the key %s that Qory Apiary enrolled.", dir.Path(foragerdir.SecretFile), ans.AccessKeyID)
	case dir.SameNewSecret(key):
		if err := dir.PutNewSecret(); err != nil {
			return again(err)
		}
		if err := afterStep("secret"); err != nil {
			return err
		}
	case !dir.SameSecret(key):
		return fmt.Errorf("Remove %s, and enrol with a new code: the secret of the key %s is not in %s.", dir, ans.AccessKeyID, dir.Path(foragerdir.SecretFile))
	}
	pin, err := json.Marshal(ans.Pin)
	if err != nil {
		return err
	}
	if err := apiarydir.WriteSettings(dir, apiarydir.Settings{Server: serverURL, AccessKeyID: ans.AccessKeyID, ApiaryPublicKey: pin}); err != nil {
		return again(err)
	}
	if err := afterStep("settings"); err != nil {
		return err
	}
	if _, _, err := dir.InstanceID(foragerdir.MachineID()); err != nil {
		return again(fmt.Errorf("the instance id: %w", err))
	}
	if err := dir.RemovePending(); err != nil {
		return again(err)
	}
	if err := afterStep("pending"); err != nil {
		return err
	}
	if err := dir.RemoveAnswer(); err != nil {
		return again(err)
	}
	fmt.Fprintf(out, "Enrolled diagnostics key %s for %s.\n", ans.AccessKeyID, host)
	return nil
}

// discardOtherKind ends an enrolment whose verified 201 is for another kind than
// diagnostics, with an error whose fix is to revoke the key. The key is active on Qory
// Apiary as id, so it is kept nowhere, not even aside: access-key-secret.new is deleted
// when it has key, and enrolment-pending is removed.
func discardOtherKind(dir foragerdir.Dir, key *accesskey.Key, host, kind, id string) error {
	what, ok := kindWords[kind]
	if !ok {
		what = "something else"
	}
	msg := fmt.Sprintf("Ask an instance admin of %s to revoke the access key %s: Qory Apiary enrolled it for %s, not as a diagnostics key, so qory kept nothing.", host, id, what)
	if dir.SameNewSecret(key) {
		if err := dir.DeleteNewSecret(); err != nil {
			return fmt.Errorf("%s\n%w", msg, err)
		}
	}
	if err := dir.RemovePending(); err != nil {
		return fmt.Errorf("%s\n%w", msg, err)
	}
	return errors.New(msg)
}

// diagnosticsEnrolFailed is the error of an enrolment that got no verified 201, by the
// refusal's code, never by an HTTP status, as [enrolFailed] decides for qory access-key
// enrol. unauthorized and key_invalid move the secret made for the code aside, see
// [discardKey], and end the pending enrolment. key_limit, rate_limited, an unsigned
// answer and an answer that never came keep both, so the same command within 15 minutes
// retries with the same key.
func diagnosticsEnrolFailed(dir foragerdir.Dir, err error, key *accesskey.Key, serverURL, host string, now time.Time) error {
	var ref *accesskey.Refusal
	if !errors.As(err, &ref) {
		return fmt.Errorf("Check that %s is reachable, then run the same command again within 15 minutes to retry with the same key: %w.", serverURL, err)
	}
	discard := func(msg string) error {
		if moved, err := discardKey(dir, key, now); err == nil && moved != "" {
			msg += "\nqory moved the secret made for the code aside to " + moved + "."
		}
		return errors.New(msg)
	}
	switch ref.Code {
	case accesskey.CodeUnauthorized:
		return discard(fmt.Sprintf("%s: %s refused the code as used, expired or cancelled (%s, %d).\nIf you did not use the code, ask an instance admin to revoke the key it enrolled.", getDiagnosticsCode, host, ref.Code, ref.Status))
	case accesskey.CodeKeyInvalid:
		names := ""
		if len(ref.Names) > 0 {
			names = ": " + strings.Join(accesskey.ShownNames(ref.Names), ", ")
		}
		return discard(fmt.Sprintf("%s: %s refused the key (%s%s).", getDiagnosticsCode, host, ref.Code, names))
	case accesskey.CodeKeyLimit:
		return fmt.Errorf("Ask an instance admin of %s to revoke a diagnostics key, then run the same command again within the code's 15 minutes: Qory Apiary refused the enrolment (%s).", host, ref.Code)
	case accesskey.CodeRateLimited:
		return fmt.Errorf("Run the same command again later, within the code's 15 minutes: %s refused the attempt, because the code was tried too often (%s).", host, ref.Code)
	case accesskey.CodeAnswerUnsigned:
		return fmt.Errorf("Check that %s is the Qory Apiary server that issued the code, then run the same command again within 15 minutes: its response (%d) is not signed with the server key whose fingerprint is in the code, so qory stored no key.", serverURL, ref.Status)
	}
	return fmt.Errorf("%s: %s refused the enrolment (%s, %d).", getDiagnosticsCode, host, ref.Code, ref.Status)
}
