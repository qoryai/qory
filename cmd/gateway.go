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
	"strings"
	"syscall"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/runcredential"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/ui"
)

// gatewayDocs is the page that describes qory gateway.
const gatewayDocs = "https://github.com/qoryai/qory/blob/main/docs/gateway.md"

// gatewayDirName is the gateway's directory under qory's state directory: where
// Forager keeps the gateway's own certificate authority, the run keys of its runs and
// the runs' records.
const gatewayDirName = "gateway"

// newGateway builds the gateway verb, which runs Forager's gateway on this machine as a
// service for the runs of other machines, until SIGINT or SIGTERM. It is the node
// toward the server, with this machine's access key, as qory run's own gateway is; it
// serves gateway.listen of forager.yaml, over TLS unless the address is loopback, and
// opens runs for the run credentials of the issuers gateway.run_credentials names.
func newGateway() *cobra.Command {
	var listen string
	var secretFD int
	c := &cobra.Command{
		Use:   "gateway",
		Short: "Run this machine's gateway for the runs of other machines",
		Long: `Run a gateway on this machine as a service, until it is stopped.

A gateway decides every connection of the runs that go through it by the run's policy,
records it, sets credentials on requests, and sends the runs' events to Qory Apiary. It is
the one part of a run that talks to Qory Apiary. qory run on this machine needs no service:
it starts a gateway of its own for each run.

The gateway section of ` + config.ForagerFileName + ` in ~/.config/qory sets it:

  listen        the address the other machines' runs reach, such as 0.0.0.0:8443
  tls           the certificate and key it serves the other machines with
  server        Qory Apiary, this machine's access key's id, and Qory Apiary's public key
  egress        the hosts a run may reach; it narrows Qory Apiary's policy
  credentials   secrets the gateway sets on requests; the agents never have them
  integrations  programs that supply such secrets
  run_credentials    the issuers whose signed run credentials open runs of clients with no session

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
			if r.TLS == nil && !loopbackListen(addr) {
				return input(fmt.Errorf("%s: gateway.tls is required with a gateway.listen other machines reach: a run's events carry its prompts and terminal output; set gateway.tls.certificate and gateway.tls.key", r.File))
			}
			if len(r.RunCredentials) == 0 {
				return input(fmt.Errorf("%s: gateway.run_credentials is required to serve other machines: their runs bring run credentials, and the gateway verifies each one; see %s", r.File, gatewayDocs))
			}
			if err := checkServiceSecrets(r); err != nil {
				return input(err)
			}
			stderr := cmd.ErrOrStderr()
			id, err := identify(r, stderr, "gateway", fdKey)
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
				Server:         gatewayServer(r.Server),
				Policy:         machinePolicy(r),
				Version:        build().title(),
				Dir:            dir,
				Listen:         addr,
				RunCredentials: issuersAt(r),
				Report:         func(line string) { fmt.Fprintln(stderr, "qory gateway:", line) },
			}
			gw.Server.AccessKey, gw.Server.InstanceID, gw.Server.InstanceName = id.key.key, id.instanceID, id.instanceName
			if r.TLS != nil {
				gw.TLS = &gateway.TLS{CertFile: r.Path(r.TLS.Certificate), KeyFile: r.Path(r.TLS.Key)}
			}
			// The gateway runs no agent on this machine, so no run of its is refused for
			// want of a wall here.
			gw.Discovered = discovered(machineDir(), id, "gateway", true, stderr)
			// The server supplies every run's policy, so every integration is described.
			shadowed := r.Shadowed()
			e := config.Expansion{Only: func(key string) bool { return !slices.Contains(shadowed, key) }}
			if gw.Credentials, err = machineCredentials(ctx, r, e, "gateway", stderr); err != nil {
				return err
			}
			g, err := gateway.Start(ctx, gw)
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

// foragerFileShown is forager.yaml's path as a person reads it, with the home
// directory as ~.
func foragerFileShown() string {
	dir := config.UserDir()
	if dir == "" {
		return "~/.config/qory/" + config.ForagerFileName
	}
	return ui.Short(filepath.Join(dir, config.ForagerFileName), "")
}

// loopbackListen reports whether a listen address, host:port, is loopback: localhost,
// or a loopback address. An empty host is every address of the machine, which is not.
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// issuersAt is gateway.run_credentials with every file it names resolved under
// forager.yaml's directory: the keys' files and the introspection clients' secrets'.
// Forager reads them; qory never does.
func issuersAt(r *config.Forager) runcredential.Issuers {
	out := make(runcredential.Issuers, len(r.RunCredentials))
	for i, is := range r.RunCredentials {
		is.Keys = append([]runcredential.Key(nil), is.Keys...)
		for k := range is.Keys {
			is.Keys[k].PublicKeyFile = r.Path(is.Keys[k].PublicKeyFile)
		}
		if is.Introspection != nil {
			in := *is.Introspection
			in.ClientSecretFile = r.Path(in.ClientSecretFile)
			is.Introspection = &in
		}
		out[i] = is
	}
	return out
}

// checkServiceSecrets checks the secret files gateway.tls and gateway.run_credentials
// name, before Forager reads them, by the rules of access-key-secret: the TLS key, which
// may be a link, such as a certificate tool keeps, to the file it checks, and may be
// root's as well; and each introspection client's secret.
func checkServiceSecrets(r *config.Forager) error {
	if r.TLS != nil {
		const setting = "gateway.tls.key"
		p := foragerdir.Private{Holds: setting, Replace: "the key", DirOf: setting, FollowLinks: true, RootOwned: true}
		if err := p.Check(r.Path(r.TLS.Key)); err != nil {
			return err
		}
	}
	for i, is := range r.RunCredentials {
		if is.Introspection == nil {
			continue
		}
		setting := fmt.Sprintf("gateway.run_credentials[%d].introspection.client_secret_file", i)
		p := foragerdir.Private{Holds: setting, Replace: "the secret", DirOf: setting}
		if err := p.Check(r.Path(is.Introspection.ClientSecretFile)); err != nil {
			return err
		}
	}
	return nil
}

// makePrivateDir makes dir under state and leaves both mode 0700: what is in them is
// this user's alone.
func makePrivateDir(state, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, d := range []string{state, dir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
