package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/ui"
)

// gatewayDocs is the page that describes qory gateway.
const gatewayDocs = "https://github.com/qoryai/qory/blob/main/docs/gateway.md"

// gatewayDirName is the gateway's directory under qory's state directory: where
// Forager keeps the gateway's own certificate authority and the runs' records.
const gatewayDirName = "gateway"

// newGateway builds the gateway verb, which runs Forager's gateway on this machine as a
// service for the runs of other machines, until SIGINT or SIGTERM. It is the node
// toward the server, with a gateway's access key; it serves gateway.listen of
// forager.yaml, always over TLS, since it serves other machines, and opens the runs whose
// run keys name it.
func newGateway() *cobra.Command {
	var listen string
	var secretFD int
	c := &cobra.Command{
		Use:   "gateway",
		Short: "Run this machine's gateway for the runs of other machines",
		Long: `Run a gateway on this machine as a service, until it is stopped.

A gateway decides every connection of the runs that go through it by the run's policy,
records it, sets credentials on requests, and sends the runs' events to Qory Apiary. qory
run on this machine needs no service: it starts a gateway itself for each run.

The gateway section of ` + config.ForagerFileName + ` in ~/.config/qory sets it:

  listen        the address the other machines' runs reach, such as 0.0.0.0:8443
  tls           the certificate and key it serves the other machines with
  server        Qory Apiary, this machine's access key's id, and Qory Apiary's public key
  egress        the hosts a run may reach; it narrows Qory Apiary's policy
  credentials   secrets the gateway sets on requests; the agents never have them
  integrations  programs that supply such secrets
  selects       the credentials it selects for a walled run whose policy selects none

` + gatewayMachinesHelp + `

SIGINT or SIGTERM stops it: it takes no new run, sends what it holds, and exits 0.

More: ` + gatewayDocs,
		Example: `  qory gateway                          # on gateway.listen of ` + config.ForagerFileName + `
  qory gateway --listen 0.0.0.0:8443`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The descriptor is read and closed first, whatever comes next, so nothing
			// qory starts inherits it.
			var fdKey *accesskey.Key
			if cmd.Flags().Changed(secretFDFlag) {
				k, err := readSecretFD(secretFD)
				if err != nil {
					return err
				}
				fdKey = k
			}
			r, err := config.LoadForager()
			if err != nil {
				return input(err)
			}
			if r == nil || !r.Gateway {
				return input(fmt.Errorf("%s has no gateway section, so there is no gateway to run; see %s", foragerFileShown(), gatewayDocs))
			}
			if err := viaForGateway(r); err != nil {
				return err
			}
			addr := r.Listen
			if cmd.Flags().Changed("listen") {
				if !config.HostPort(listen) {
					return input(fmt.Errorf("--listen %q is not host:port, such as 0.0.0.0:8443", listen))
				}
				addr = listen
			}
			if addr == "" {
				return input(fmt.Errorf("%s: gateway.listen is required to run the gateway as a service: the address the other machines reach, such as 0.0.0.0:8443; or --listen", r.File))
			}
			if r.Server == nil {
				return input(fmt.Errorf("%s: gateway.server is required to run the gateway as a service: it reports every run to Qory Apiary and takes the runs' policies from it", r.File))
			}
			if r.TLS == nil {
				return input(errors.New("gateway.tls is required with gateway.listen: a gateway serves other machines"))
			}
			tlsKey, err := checkServiceSecrets(r)
			if err != nil {
				return input(err)
			}
			stderr := cmd.ErrOrStderr()
			id, err := identify(r, stderr, verbGateway, fdKey)
			if err != nil {
				return err
			}
			state, err := stateDir()
			if err != nil {
				return err
			}
			dir := filepath.Join(state, gatewayDirName)
			if err := makePrivateDir(state, dir); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			gw := gateway.Config{
				Server:  gatewayServer(r.Server),
				Policy:  machinePolicy(r),
				Version: build().title(),
				Dir:     dir,
				Listen:  addr,
				Report:  func(line string) { fmt.Fprintln(stderr, "qory gateway:", line) },
				// Nothing on this machine uses the local link: no socket for it.
				NoLinkSocket: true,
				Heartbeat:    runHeartbeat,
			}
			gw.Server.AccessKey, gw.Server.InstanceID, gw.Server.InstanceName = id.key.key, id.instanceID, id.instanceName
			if r.TLS != nil {
				// The key file checked, where a link led then: Forager reads that file.
				gw.TLS = &gateway.TLS{CertFile: r.Path(r.TLS.Certificate), KeyFile: tlsKey}
			}
			// The gateway runs no agent on this machine, so no run of its is refused for
			// want of a wall here.
			gw.Discovered = discovered(machineDir(), id, verbGateway, stderr)
			// The server supplies every run's policy, so every integration is described.
			shadowed := r.Shadowed()
			e := config.Expansion{Only: func(key string) bool { return !slices.Contains(shadowed, key) }}
			if gw.Credentials, err = machineCredentials(ctx, r, e, "gateway", stderr); err != nil {
				return err
			}
			if gw.Tools, err = machineTools(r); err != nil {
				return err
			}
			// The credentials gateway.selects names are checked against the ones the
			// integrations define too, so only once they are described.
			if gw.Selects, err = r.Selections(); err != nil {
				return input(err)
			}
			g, err := startGateway(ctx, gw)
			if err != nil {
				var op *net.OpError
				if errors.As(err, &op) && op.Op == "listen" {
					return fmt.Errorf("listen on %s: %w", addr, op.Err)
				}
				return explain(err, id)
			}
			fmt.Fprintln(stderr, "qory gateway: listening on", g.Addr())
			<-ctx.Done()
			// A second signal ends the process the default way.
			stop()
			fmt.Fprintln(stderr, "qory gateway: stopping")
			if _, err := g.Close(context.WithoutCancel(ctx)); err != nil {
				gw.Report(err.Error())
			}
			ui.New(stderr).Success("the gateway stopped")
			return nil
		},
	}
	c.Flags().StringVar(&listen, "listen", "", "the address to listen on, host:port; it wins over gateway.listen of "+config.ForagerFileName)
	c.Flags().IntVar(&secretFD, secretFDFlag, 0, secretFDUsage)
	return c
}

// startGateway starts the gateway qory gateway runs, with the configuration qory built
// from forager.yaml; a test replaces it to read that configuration.
var startGateway = gateway.Start

// foragerFileShown is forager.yaml's path as a person reads it, with the home
// directory as ~.
func foragerFileShown() string {
	dir := config.UserDir()
	if dir == "" {
		return "~/.config/qory/" + config.ForagerFileName
	}
	return ui.Short(filepath.Join(dir, config.ForagerFileName), "")
}

// checkServiceSecrets checks the secret file gateway.tls names, before Forager reads it,
// by the rules of access-key-secret: the TLS key, which may be a link, such as a
// certificate tool keeps, to the file it checks. It returns the TLS key's file it
// checked, empty without gateway.tls.
func checkServiceSecrets(r *config.Forager) (string, error) {
	var tlsKey string
	if r.TLS != nil {
		const setting = "gateway.tls.key"
		p := foragerdir.Private{Holds: setting, Replace: "the key", DirOf: setting, FollowLinks: true}
		var err error
		if tlsKey, err = p.Check(r.Path(r.TLS.Key)); err != nil {
			return "", err
		}
	}
	return tlsKey, nil
}

// gatewayMachinesHelp is the paragraph of qory gateway's help that says what the machines
// whose runs go through the gateway hold, and what it passes on for them.
const gatewayMachinesHelp = `A machine whose runs go through this gateway names no gateway: each run brings a run
key, which its starter got from Qory Apiary and which names this gateway. For machines
that reach Qory Apiary only through it, the gateway passes on their enrolment,
registrations and outcomes unchanged.`

// makePrivateDir makes dir under state, both mode 0700, refusing either when it is a
// link or another user's before anything is written there: what is in them is this
// user's alone.
func makePrivateDir(state, dir string) error {
	if err := foragerdir.MakePrivateDir(state, "qory's state directory"); err != nil {
		return err
	}
	return foragerdir.MakePrivateDir(dir, "the directory of qory gateway")
}
