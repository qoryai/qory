package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/qoryai/runner/receiver"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
)

// newReceive builds the receive verb, the reference receiver of the runner's webhook:
// it listens where the webhook configuration's URL says, verifies every delivery's
// signature with the configuration's secret, deduplicates on the event id and appends
// each new event to one JSON lines file. It is the other end of qory run's webhook on
// one machine, for a look at what a receiver gets and for recording fixtures.
func newReceive() *cobra.Command {
	var webhookPath, listen, out string
	c := &cobra.Command{
		Use:   "receive",
		Short: "Receive the runner's webhook deliveries and append the events to a file",
		Long: `Receive the deliveries qory run posts to its webhook and append every event to a file,
one JSON object per line, in the order they arrived, each once. The configuration is
the file qory run reads, ` + WebhookFile + ` in the configuration directory or the file
--webhook names: the receiver listens on the URL's host and port and answers on its
path, and verifies each delivery with the secret. --listen puts it on another address,
for a URL that names a host this machine is not. A delivery that does not verify is
refused with 401 and nothing of it is kept.

The file is --out, events.jsonl in the current directory when left out, and is appended
to when it exists; the ids in it are read first so a redelivery after a restart is not
stored twice. Ctrl-C stops the receiver.

--verbose adds nothing here.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if webhookPath == "" {
				webhookPath = present(filepath.Join(config.UserDir(), WebhookFile))
				if webhookPath == "" {
					return input(fmt.Errorf("no %s in %s; --webhook names one", WebhookFile, config.UserDir()))
				}
			}
			hook, err := receiver.LoadWebhook(webhookPath)
			if err != nil {
				return input(err)
			}
			u, err := url.Parse(hook.URL)
			if err != nil {
				return input(err)
			}
			if listen == "" {
				listen = u.Host
				if u.Port() == "" {
					listen = net.JoinHostPort(u.Hostname(), "80")
				}
			}
			path := u.Path
			if path == "" {
				path = "/"
			}
			store, err := receiver.OpenFile(out)
			if err != nil {
				return err
			}
			defer store.Close()
			stderr := cmd.ErrOrStderr()
			w := ui.New(stderr)
			mux := http.NewServeMux()
			mux.Handle(path, &receiver.Handler{Secret: hook.Secret, Store: store, Log: func(line string) { fmt.Fprintln(stderr, line) }})
			ln, err := net.Listen("tcp", listen)
			if err != nil {
				return err
			}
			w.Title("qory receive", ln.Addr().String())
			w.Fields([][2]string{{"path", path}, {"secret", "from " + webhookPath}, {"events", out}, {"stored", fmt.Sprint(store.Count())}})
			srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			done := make(chan error, 1)
			go func() { done <- srv.Serve(ln) }()
			select {
			case err := <-done:
				return err
			case <-ctx.Done():
			}
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				return err
			}
			if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			w.Success("stopped; %d events in %s", store.Count(), out)
			return nil
		},
	}
	c.Flags().StringVar(&webhookPath, "webhook", "", "the webhook configuration; "+WebhookFile+" in the configuration directory when left out")
	c.Flags().StringVar(&listen, "listen", "", "the address to listen on; the URL's host and port when left out")
	c.Flags().StringVar(&out, "out", "events.jsonl", "the file the events are appended to")
	return c
}
