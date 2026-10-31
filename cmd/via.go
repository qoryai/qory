package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/runkey"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/secretmask"
)

// A machine with its own access key whose forager.yaml sets gateway.server.via reaches
// the server only through that gateway, and its runs go through the gateway: qory
// registers each run itself, through via, keeps the run key the server answers with
// beside forager.yaml, and runs the session against the gateway the run key names, with
// no gateway of its own. Such a machine holds an access key, so its runs are walled
// ([keyNeedsWall]).

// runsThroughVia reports whether a run against r's server, id not nil, registers itself
// through gateway.server.via.
func runsThroughVia(r *config.Forager, id *serverIdentity) bool {
	return id != nil && r != nil && r.Server != nil && r.Server.Via != nil
}

// viaGatewayKeys refuses, as input, each key of r's gateway section other than
// gateway.server when r sets gateway.server.via: a run that goes through via's gateway
// starts no gateway of its own, so the egress policy, the credentials, the integrations
// and every other key only a gateway of this machine's own reads do nothing. One line
// per key, in the order [config.Forager.GatewayKeys] lists them. qory run checks it
// before anything starts or registers, unless --local, which runs a gateway of its own.
func viaGatewayKeys(r *config.Forager) error {
	if r == nil || r.Server == nil || r.Server.Via == nil || len(r.GatewayKeys) == 0 {
		return nil
	}
	lines := make([]string, len(r.GatewayKeys))
	for i, k := range r.GatewayKeys {
		lines[i] = fmt.Sprintf("%s: gateway.%s does nothing on this machine: gateway.server.via sends its runs through a gateway, which holds their policy, credentials, tools and integrations; remove gateway.%s, or remove gateway.server.via", r.File, accesskey.ShownName(k, false), accesskey.ShownName(k, false))
	}
	return input(errors.New(strings.Join(lines, "\n")))
}

// viaForGateway refuses, as input, gateway.server.via in r for qory gateway: a gateway
// reaches the server directly.
func viaForGateway(r *config.Forager) error {
	if r == nil || r.Server == nil || r.Server.Via == nil {
		return nil
	}
	return input(fmt.Errorf("%s: gateway.server.via does nothing for qory gateway: a gateway reaches the server directly; remove gateway.server.via", r.File))
}

// registerThroughVia registers the run runID of a machine with its own access key,
// signed as id, through r's gateway.server.via, with the run's labels and what it is
// about: it reads the server's discovery, prints the node line and refuses a gateway's
// access key as qory run does, sends the gateway shape of registration, and keeps the
// run key in the directory of forager.yaml before it gives it back. The server's
// refusals say "this machine's node" ([explainRegistration]).
func registerThroughVia(ctx context.Context, r *config.Forager, id *serverIdentity, runID string, labels map[string]string, about *session.About, report io.Writer) (*runKey, error) {
	client := serverClient(r, id.key.key, id.instanceID, id.instanceName)
	conf, _, err := client.Discover(ctx)
	if err != nil {
		return nil, explainRegistration(err, id, runID, true)
	}
	dir, err := machineDir()
	if err != nil {
		return nil, err
	}
	if err := onDiscovery(dir, id.key, id.instanceID, "run", report, gateway.Discovery{NodeID: conf.NodeID, Secrets: conf.Secrets != nil}); err != nil {
		return nil, err
	}
	body, err := server.GatewayRegistration{RunID: runID, Labels: labels, About: about}.Body()
	if err != nil {
		return nil, input(err)
	}
	ans, err := client.RegisterThroughGateway(ctx, conf.Run.URL, body)
	if err != nil {
		return nil, explainRegistration(err, id, runID, true)
	}
	k, err := runkey.Parse(ans.RunKey)
	if err != nil {
		// Forager read it as a run key of the run already; the reason holds no key.
		return nil, err
	}
	if err := dir.WriteRunKey(runID, ans.RunKey); err != nil {
		return nil, keptRunKeyNotWritten(runID, err)
	}
	return &runKey{value: ans.RunKey, key: k}, nil
}

// keptRunKeyNotWritten is the error of a run registered through gateway.server.via
// whose run key could not be kept: the run does not start. err holds no key.
func keptRunKeyNotWritten(runID string, err error) error {
	return fmt.Errorf("the run %s is registered, and its run key could not be kept beside %s, so the run does not start: %w", runID, config.ForagerFileName, err)
}

// keptRunKey is the run key qory kept for the run runID, registered through
// gateway.server.via on this machine; nil when none is kept, or there is no directory of
// forager.yaml. A kept key that is not a run key is said as qory says one in
// QORY_RUN_KEY, without the key. The kept value joins the process's mask list,
// [secretmask.Default].
func keptRunKey(runID string) (*runKey, error) {
	dir, err := machineDir()
	if err != nil || dir == "" {
		return nil, err
	}
	v, err := dir.ReadRunKey(runID)
	if errors.Is(err, foragerdir.ErrNoRunKey) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v = strings.TrimSuffix(v, "\n")
	secretmask.Default().Add(v)
	k, err := runkey.Parse(v)
	if err != nil {
		return nil, fmt.Errorf("%s is not a run key: %s", dir.RunKeyPath(runID), runKeyReason(err))
	}
	return &runKey{value: v, key: k}, nil
}

// dropKeptRunKey removes the run key kept for the run runID, once nothing of the run is
// left to send or its gateway has ended it; a key that cannot be removed is said on
// report, with its path.
func dropKeptRunKey(runID string, report func(string)) {
	dir, err := machineDir()
	if err != nil {
		report(err.Error())
		return
	}
	if dir == "" {
		return
	}
	if err := dir.RemoveRunKey(runID); err != nil {
		report(fmt.Sprintf("the run key kept in %s could not be removed: %v", dir.RunKeyPath(runID), err))
	}
}

// ownGatewayViaResend is the refusal of a resend of the run runID, which ran with a
// gateway of its own on this machine, while gateway.server.via is set: its record goes to
// the server, and Forager's resend to the server goes through no gateway.
func ownGatewayViaResend(runID string) error {
	return input(fmt.Errorf("the run %s started its gateway itself on this machine, so its record goes to the server, and qory sends it there through no gateway, while gateway.server.via is set: resend it with a %s without gateway.server.via", runID, config.ForagerFileName))
}
