package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/stack"
)

// RunnerFileName is the machine's runner file, under [UserDir] alone: what qory run
// does on this machine. It has no counterpart in a repository, so a checkout cannot set
// the policy the agent runs under or where the run's events go.
const RunnerFileName = "runner.yaml"

// EnvWebhookSecret is the environment variable that holds the webhook secret when the
// file does not.
const EnvWebhookSecret = "QORY_WEBHOOK_SECRET"

// Runner is the machine's runner file, read.
type Runner struct {
	// File is the path read.
	File string
	// Egress is the run policy's egress section, nil when the file names none: observe
	// everything, deny nothing.
	Egress *RunnerEgress
	// Webhook is where every event is posted as well, nil when the file names none.
	Webhook *RunnerWebhook
}

// RunnerEgress is the egress section: the policy the runner pins for every run on this
// machine, in the runner contract's grammar.
type RunnerEgress struct {
	// Mode is observe or enforce.
	Mode string
	// Allow are the hosts the runtime may reach, each a lower-case name or a *. suffix.
	Allow []string
}

// RunnerWebhook is the webhook section.
type RunnerWebhook struct {
	// URL is https, or http to a loopback address.
	URL string
	// Secret signs every delivery; from the file, or from [EnvWebhookSecret].
	Secret string
	// FromEnv is set when the secret came from the environment.
	FromEnv bool
	// Events are the types to post, nil for every type.
	Events []string
}

// runnerFile is runner.yaml as written.
type runnerFile struct {
	APIVersion string `yaml:"apiVersion"`
	Egress     *struct {
		Mode  *string   `yaml:"mode"`
		Allow *[]string `yaml:"allow"`
	} `yaml:"egress,omitempty"`
	Webhook *struct {
		URL    *string   `yaml:"url"`
		Secret *string   `yaml:"secret"`
		Events *[]string `yaml:"events"`
	} `yaml:"webhook,omitempty"`
}

// LoadRunner reads the machine's runner file under [UserDir]. No file is no runner
// configuration and returns nil; a file that does not read is an error naming it.
func LoadRunner() (*Runner, error) {
	dir := UserDir()
	if dir == "" {
		return nil, nil
	}
	path := filepath.Join(dir, RunnerFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f runnerFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, decodeError(path, err)
	}
	if f.APIVersion == "" {
		f.APIVersion = stack.APIVersion
	}
	if _, err := exports.ResolveAPIVersion(f.APIVersion); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	r := &Runner{File: path}
	if e := f.Egress; e != nil {
		if e.Mode == nil {
			return nil, fmt.Errorf("%s: egress.mode is required: observe or enforce", path)
		}
		if *e.Mode != "observe" && *e.Mode != "enforce" {
			return nil, fmt.Errorf("%s: egress.mode %q is not observe or enforce", path, *e.Mode)
		}
		r.Egress = &RunnerEgress{Mode: *e.Mode}
		if e.Allow != nil {
			for _, host := range *e.Allow {
				if !module.EgressHost.MatchString(host) {
					return nil, fmt.Errorf("%s: egress.allow: %q is not a lower-case host name or a *. suffix; no port, path or scheme", path, host)
				}
				r.Egress.Allow = append(r.Egress.Allow, host)
			}
		}
	}
	if w := f.Webhook; w != nil {
		if w.URL == nil || *w.URL == "" {
			return nil, fmt.Errorf("%s: webhook.url is required", path)
		}
		u, err := url.Parse(*w.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("%s: webhook.url %q is not an https URL, or an http URL to this machine", path, *w.URL)
		}
		if u.Scheme == "http" && !loopback(u.Hostname()) {
			return nil, fmt.Errorf("%s: webhook.url %q is http to a host that is not this machine; a receiver elsewhere is reached over https", path, *w.URL)
		}
		r.Webhook = &RunnerWebhook{URL: *w.URL}
		switch {
		case w.Secret != nil && *w.Secret != "":
			r.Webhook.Secret = *w.Secret
		case os.Getenv(EnvWebhookSecret) != "":
			r.Webhook.Secret, r.Webhook.FromEnv = os.Getenv(EnvWebhookSecret), true
		default:
			return nil, fmt.Errorf("%s: webhook.secret is missing; set it there or in %s", path, EnvWebhookSecret)
		}
		if len(r.Webhook.Secret) < 16 {
			return nil, fmt.Errorf("%s: webhook.secret is shorter than 16 characters", path)
		}
		if w.Events != nil {
			for _, typ := range *w.Events {
				if typ == "" {
					return nil, fmt.Errorf("%s: webhook.events names an empty type", path)
				}
			}
			r.Webhook.Events = append([]string{}, (*w.Events)...)
		}
	}
	return r, nil
}

// loopback reports whether host is this machine: localhost or a loopback address.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Rows lists the runner file's effective values with the file as origin, and the
// defaults with [Default] when there is no file or a section is absent.
func (r *Runner) Rows() []Row {
	origin := Default
	if r != nil {
		origin = r.File
	}
	rows := []Row{{"runner.egress.mode", "observe", Default}, {"runner.egress.allow", "(none)", Default}}
	if r != nil && r.Egress != nil {
		rows[0] = Row{"runner.egress.mode", r.Egress.Mode, origin}
		rows[1] = Row{"runner.egress.allow", listOrNone(r.Egress.Allow), origin}
	}
	if r != nil && r.Webhook != nil {
		secret := "(set)"
		if r.Webhook.FromEnv {
			secret = "(from " + EnvWebhookSecret + ")"
		}
		events := "*"
		if len(r.Webhook.Events) > 0 {
			events = strings.Join(r.Webhook.Events, ", ")
		}
		rows = append(rows,
			Row{"runner.webhook.url", r.Webhook.URL, origin},
			Row{"runner.webhook.secret", secret, origin},
			Row{"runner.webhook.events", events, origin},
		)
	} else {
		rows = append(rows, Row{"runner.webhook.url", "(none)", Default})
	}
	return rows
}
