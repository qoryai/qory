package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/server"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/apiarydir"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
)

// apiaryDocs is the page that describes the qory apiary commands.
const apiaryDocs = "https://github.com/qoryai/qory/blob/main/docs/apiary.md"

// diagnosticsContractVersion is X-Qory-Contract-Version of a request for a run's
// diagnostics. Qory Apiary refuses the request with a 400 unsupported_contract_version
// for any other value.
const diagnosticsContractVersion = "1"

// maxDiagnostics is the largest diagnostics document qory prints, and maxDiagnosticsRead
// the most of a response body it reads: the document and 64 KiB more, the most of a refusal.
const (
	maxDiagnostics     = 8 << 20
	maxDiagnosticsRead = maxDiagnostics + accesskey.MaxAnswer
)

// diagnosticsTimeout is how long the request for a run's diagnostics may take, from its
// start to the last byte of the body.
const diagnosticsTimeout = 10 * time.Second

// diagnosticsClock is the time a request for diagnostics is signed at; a test replaces
// it.
var diagnosticsClock = time.Now

// runIDShape is a run id as Qory Apiary shows it on the run's page.
var runIDShape = regexp.MustCompile(`^rn_[0-9a-hjkmnp-tv-z]{16}$`)

// codeNotFound is Qory Apiary's signed 404 to a request for the diagnostics of a run it
// does not have, or of a run id it does not read.
const codeNotFound = accesskey.CodeNotFound

// codeUnsupportedContractVersion is the code of a refusal of the contract version the
// request sends.
const codeUnsupportedContractVersion = "unsupported_contract_version"

// newApiary builds the apiary group: work with a Qory Apiary server as a person who
// debugs its runs.
func newApiary() *cobra.Command {
	c := &cobra.Command{
		Use:   "apiary",
		Short: "Debug runs on a Qory Apiary server",
		Long: `Work with a Qory Apiary server as a person who debugs its runs.

  enrol          make a diagnostics key and enrol it with a code from Qory Apiary
  run diagnose   print a run's diagnostics document

The commands sign their requests with a diagnostics key: an access key of kind
diagnostics, made on this computer. Its secret stays in ~/.config/qory/apiary/<host>,
Qory Apiary keeps its public half, and it reads only the diagnostics of runs.

More: ` + apiaryDocs,
	}
	c.AddCommand(newApiaryEnrol(), newApiaryRun())
	return c
}

// newApiaryRun builds the run group of qory apiary.
func newApiaryRun() *cobra.Command {
	c := &cobra.Command{
		Use:   "run",
		Short: "Read what Qory Apiary recorded about a run",
		Long: `Read what Qory Apiary recorded about a run, by the run's id.

  diagnose   print a run's diagnostics document

More: ` + apiaryDocs,
	}
	c.AddCommand(newApiaryRunDiagnose())
	return c
}

// newApiaryRunDiagnose builds qory apiary run diagnose.
func newApiaryRunDiagnose() *cobra.Command {
	var serverURL string
	c := &cobra.Command{
		Use:   "diagnose <run id>",
		Short: "Print a run's diagnostics document from Qory Apiary",
		Long: `Print a run's diagnostics document: the run, its queue entry, each dispatch, the
preparation of its machine, its last events and the end of its log, as one JSON
document. The run id is the rn_ id on the run's page in Qory Apiary.

qory signs the request with the diagnostics key in ~/.config/qory/apiary/<host>, and
prints the document only after its signature verifies under the server key pinned
there. The document goes to stdout as Qory Apiary sent it, so a pipe or a file receives
JSON alone; errors go to stderr.

--server selects the server by its URL. Without it, qory takes the one server it has a
diagnostics key for.

--verbose adds nothing here.

More: ` + apiaryDocs,
		Example: `  qory apiary run diagnose rn_0123456789abcdef
  qory apiary run diagnose rn_0123456789abcdef --server https://apiary.example
  qory apiary run diagnose rn_0123456789abcdef > diagnostics.json`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return input(errors.New("Pass one run id, such as rn_0123456789abcdef: qory apiary run diagnose takes the id of one run."))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return diagnose(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], serverURL, cmd.Flags().Changed("server"))
		},
	}
	c.Flags().StringVar(&serverURL, "server", "", "the Qory Apiary server, by its URL; needed when diagnostics keys for several servers are on this computer")
	return c
}

// diagnose prints the diagnostics document of the run runID to out: it reads the
// diagnostics key of the server --server selects, or of the one server with a key,
// sends the signed GET and writes the body of a verified 200 as it came, with a line
// ending after it when it has none. Nothing else goes to out. A response that does not
// verify under the pinned key prints nothing.
func diagnose(ctx context.Context, out, report io.Writer, runID, serverURL string, serverGiven bool) error {
	if !runIDShape.MatchString(runID) {
		return input(fmt.Errorf("Pass the run id from the run's page in Qory Apiary, rn_ and 16 characters, such as rn_0123456789abcdef: %s is not one.", exports.Value(runID, true)))
	}
	root, err := apiarydir.Root()
	if err != nil {
		return input(err)
	}
	host, err := diagnosticsHost(root, serverURL, serverGiven)
	if err != nil {
		return err
	}
	key, err := apiarydir.Read(root, host)
	if errors.Is(err, apiarydir.ErrNoKey) {
		return input(noDiagnosticsKey(serverURL, filepath.Join(root, host)))
	}
	if err != nil {
		return input(err)
	}
	instance, kept, err := key.Dir.InstanceID(foragerdir.MachineID())
	if err != nil {
		return fmt.Errorf("Check that %s can be written: the instance id: %w", key.Dir, err)
	}
	if !kept {
		fmt.Fprintf(report, "qory apiary run diagnose: instance %s, this process's alone: %s cannot be written\n", instance, key.Dir.Path(foragerdir.InstanceFile))
	}
	resp, err := fetchDiagnostics(ctx, key, instance, runID)
	if err != nil {
		return err
	}
	body := resp.body
	if len(body) == 0 || body[len(body)-1] != '\n' {
		body = append(body, '\n')
	}
	_, err = out.Write(body)
	return err
}

// diagnosticsHost returns the name of the server directory under root to read the key
// from: the one serverURL selects when serverGiven, else the one directory there. No
// directory, and several, are input errors whose first line is the fix.
func diagnosticsHost(root, serverURL string, serverGiven bool) (string, error) {
	if serverGiven {
		host, err := apiarydir.HostName(serverURL)
		if err != nil {
			return "", input(err)
		}
		return host, nil
	}
	hosts, err := apiarydir.Hosts(root)
	if err != nil {
		return "", fmt.Errorf("Check that %s can be read: %w", root, err)
	}
	switch len(hosts) {
	case 0:
		return "", input(noDiagnosticsKey("", root))
	case 1:
		return hosts[0], nil
	}
	return "", input(fmt.Errorf("Pass --server <url>: diagnostics keys for several servers are in %s: %s.", root, strings.Join(hosts, ", ")))
}

// noDiagnosticsKey is the error of a command that finds no diagnostics key in dir, for
// the server at serverURL, or for any server when it is empty.
func noDiagnosticsKey(serverURL, dir string) error {
	if serverURL == "" {
		serverURL = "<server>"
	}
	return fmt.Errorf("Run `qory apiary enrol %s <code>` first: no diagnostics key in %s.", serverURL, dir)
}

// diagnosticsResponse is a verified 200 to a request for diagnostics: its body as it
// came.
type diagnosticsResponse struct {
	body []byte
}

// fetchDiagnostics sends the GET for the diagnostics of runID, signed with key as the
// instance instance, as Forager's server client signs a GET: the timestamp in place of
// a body. It follows no redirect and keeps no cookie, since Go copies a request's
// headers to wherever a redirect points. It returns the body of a 200 whose signature
// verifies under the pinned key, bound to this request's signature; every other result
// is an error whose first line is the fix. No error contains the secret, the signature
// or a header of the request.
func fetchDiagnostics(ctx context.Context, key *apiarydir.Key, instance, runID string) (*diagnosticsResponse, error) {
	serverURL := strings.TrimSuffix(key.Server, "/")
	hc := &http.Client{
		Timeout:       diagnosticsTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/v1/runs/"+runID+"/diagnostics", nil)
	if err != nil {
		return nil, accesskey.ShownError(err)
	}
	signed := accesskey.Request{
		AccessKeyID: key.AccessKeyID,
		InstanceID:  instance,
		Method:      http.MethodGet,
		Target:      req.URL.RequestURI(),
		Timestamp:   accesskey.Timestamp(diagnosticsClock()),
	}
	sig, err := key.Secret.SignRequest(signed)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set(server.HeaderAccessKeyID, key.AccessKeyID)
	req.Header.Set(server.HeaderInstanceID, instance)
	req.Header.Set(server.HeaderInstanceName, config.DefaultInstanceName())
	req.Header.Set(server.HeaderContractVersion, diagnosticsContractVersion)
	req.Header.Set(server.HeaderTimestamp, signed.Timestamp)
	req.Header.Set(server.HeaderSignature, sig)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Check that %s is reachable: %w.", serverURL, accesskey.ShownError(unwrapURLError(err)))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDiagnosticsRead+1))
	if err != nil {
		return nil, fmt.Errorf("Check that %s is reachable: reading the response: %w.", serverURL, accesskey.ShownError(unwrapURLError(err)))
	}
	status := resp.StatusCode
	if status == http.StatusUnauthorized {
		return nil, fmt.Errorf("%s: %s did not accept the diagnostics key %s (401).", apiarydir.EnrolAgain(key.Dir, key.Server), key.Host, key.AccessKeyID)
	}
	if len(b) > maxDiagnosticsRead || (status != http.StatusOK && len(b) > server.MaxRefusal) || !verified(key.Pin, resp.Header, status, sig, b) {
		return nil, fmt.Errorf("Check that %s is the server the diagnostics key in %s was enrolled with: its response (%d) is not signed with the pinned server key, so qory printed nothing.", serverURL, key.Dir, status)
	}
	if status == http.StatusOK {
		if len(b) > maxDiagnostics {
			return nil, fmt.Errorf("Ask the instance admin of %s to check the run %s: its diagnostics document is larger than 8 MiB, the most qory prints.", key.Host, runID)
		}
		return &diagnosticsResponse{body: b}, nil
	}
	ref := accesskey.ReadRefusal(status, b)
	switch {
	case ref == nil:
		return nil, fmt.Errorf("Check the run with the instance admin of %s: the request was not accepted (%d).", key.Host, status)
	case ref.Code == codeNotFound && status == http.StatusNotFound:
		return nil, fmt.Errorf("Check the run id: %s has no run %s (%s).", key.Host, runID, codeNotFound)
	case ref.Code == codeUnsupportedContractVersion:
		return nil, fmt.Errorf("Update qory to a release that works with %s: the request was not accepted: %s (%d).", key.Host, ref.Code, status)
	case len(ref.Names) > 0:
		return nil, fmt.Errorf("Check the run with the instance admin of %s: the request was not accepted: %s (%d): %s.", key.Host, ref.Code, status, strings.Join(accesskey.ShownNames(ref.Names), ", "))
	}
	return nil, fmt.Errorf("Check the run with the instance admin of %s: the request was not accepted: %s (%d).", key.Host, ref.Code, status)
}

// verified reports whether a response verifies under pin, bound to the request's
// signature sig: X-Qory-Signature-Ed25519 over its status, its body and its two digest
// headers, each header given at most once.
func verified(pin accesskey.Pin, h http.Header, status int, sig string, body []byte) bool {
	one := func(name string) (string, bool) {
		v := h.Values(name)
		return strings.Join(v, ""), len(v) <= 1
	}
	signature, ok1 := one(server.HeaderSignature)
	conf, ok2 := one(server.HeaderConfiguration)
	run, ok3 := one(server.HeaderRunConfiguration)
	return ok1 && ok2 && ok3 && signature != "" && pin.VerifyAnswer(accesskey.Answer{Status: status, RequestSignature: sig, Body: body, Configuration: conf, RunConfiguration: run}, signature)
}

// unwrapURLError is the cause of an error of http.Client, without the method and URL
// it adds, which the message before it already contains.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
