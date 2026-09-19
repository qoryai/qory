package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	// Wall is what the runtime is enclosed in, nil when the file names none: the runtime
	// is a process of this machine.
	Wall *RunnerWall
	// Timeout is how long a runtime may run on this machine, zero for no limit, and
	// StopGrace how long it gets between SIGTERM and SIGKILL when the runner stops it,
	// zero for the runner's default. --timeout and --stop-grace name others for one run.
	Timeout   time.Duration
	StopGrace time.Duration
}

// WallDocker is the one wall adapter there is.
const WallDocker = "docker"

// RunnerWall is the wall section: the container every run on this machine starts the
// runtime in, with no route out except to the runner's proxy.
type RunnerWall struct {
	// Adapter names what builds the wall: docker.
	Adapter string
	// Image is the agent's image, the runtime and the project's toolchain; the --image
	// flag names another. It may be empty here and given by the flag.
	Image string
	// Command is the program the adapter runs, podman say; empty means docker.
	Command string
	// Helper is the path of a static Linux build of qory, mounted into the container as
	// the relay and the hook forwarder; empty means this binary, which only a Linux
	// machine can use.
	Helper string
	// Env names the variables of this environment that go into the container, the model
	// credential say. Nothing else of the environment does.
	Env []string
	// User is the uid:gid the container runs as; empty means qory run's own, so the
	// checkout's files keep their owner. Root is refused, so a machine where qory runs
	// as root names one.
	User string
	// Mounts are what the container sees of this machine beside the checkout and the
	// composed home, each at its own path; --mount adds to them.
	Mounts []RunnerMount
	// CPUs, Memory, PIDs and ShmSize limit what the container uses, as docker run's
	// --cpus, --memory, --pids-limit and --shm-size do; empty or zero is the engine's
	// default, and a flag of the same name sets another for one run.
	CPUs    string
	Memory  string
	PIDs    int
	ShmSize string
}

// RunnerMount is one file or directory of this machine a walled run sees, at the same
// path.
type RunnerMount struct {
	Path     string
	ReadOnly bool
}

// ParseMount reads a mount as wall.mounts and --mount write it: an absolute path, and
// :ro after it for one the container cannot change. :rw says the default aloud.
func ParseMount(v string) (RunnerMount, error) {
	m := RunnerMount{Path: v}
	if p, ok := strings.CutSuffix(v, ":ro"); ok {
		m = RunnerMount{Path: p, ReadOnly: true}
	} else if p, ok := strings.CutSuffix(v, ":rw"); ok {
		m.Path = p
	}
	if !filepath.IsAbs(m.Path) {
		return m, fmt.Errorf("the mount %q is not an absolute path, with :ro after it for a read-only one", v)
	}
	m.Path = filepath.Clean(m.Path)
	return m, nil
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
	Run *struct {
		Timeout   *string `yaml:"timeout"`
		StopGrace *string `yaml:"stop_grace"`
	} `yaml:"run,omitempty"`
	Wall *struct {
		Adapter *string   `yaml:"adapter"`
		Image   *string   `yaml:"image"`
		Command *string   `yaml:"command"`
		Helper  *string   `yaml:"helper"`
		Env     *[]string `yaml:"env"`
		User    *string   `yaml:"user"`
		Mounts  *[]string `yaml:"mounts"`
		CPUs    *string   `yaml:"cpus"`
		Memory  *string   `yaml:"memory"`
		PIDs    *int      `yaml:"pids_limit"`
		ShmSize *string   `yaml:"shm_size"`
	} `yaml:"wall,omitempty"`
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
	if run := f.Run; run != nil {
		for _, d := range []struct {
			key string
			in  *string
			out *time.Duration
		}{{"run.timeout", run.Timeout, &r.Timeout}, {"run.stop_grace", run.StopGrace, &r.StopGrace}} {
			if d.in == nil {
				continue
			}
			v, err := time.ParseDuration(*d.in)
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("%s: %s %q is not a duration above zero, 5h30m or 30s say", path, d.key, *d.in)
			}
			*d.out = v
		}
	}
	if w := f.Wall; w != nil {
		if w.Adapter == nil || *w.Adapter != WallDocker {
			return nil, fmt.Errorf("%s: wall.adapter is required, and %s is the one there is", path, WallDocker)
		}
		r.Wall = &RunnerWall{Adapter: *w.Adapter}
		if w.Image != nil {
			r.Wall.Image = *w.Image
		}
		if w.Command != nil {
			r.Wall.Command = *w.Command
		}
		if w.Helper != nil {
			if !filepath.IsAbs(*w.Helper) {
				return nil, fmt.Errorf("%s: wall.helper %q is not an absolute path", path, *w.Helper)
			}
			r.Wall.Helper = *w.Helper
		}
		if w.User != nil {
			r.Wall.User = *w.User
		}
		if w.Mounts != nil {
			for _, v := range *w.Mounts {
				m, err := ParseMount(v)
				if err != nil {
					return nil, fmt.Errorf("%s: wall.mounts: %w", path, err)
				}
				r.Wall.Mounts = append(r.Wall.Mounts, m)
			}
		}
		if w.CPUs != nil {
			r.Wall.CPUs = *w.CPUs
		}
		if w.Memory != nil {
			r.Wall.Memory = *w.Memory
		}
		if w.PIDs != nil {
			if *w.PIDs < 1 {
				return nil, fmt.Errorf("%s: wall.pids_limit is %d; a limit is at least 1", path, *w.PIDs)
			}
			r.Wall.PIDs = *w.PIDs
		}
		if w.ShmSize != nil {
			r.Wall.ShmSize = *w.ShmSize
		}
		if w.Env != nil {
			for _, name := range *w.Env {
				if name == EnvWebhookSecret {
					return nil, fmt.Errorf("%s: wall.env: %s is the runner's own and never the session's", path, name)
				}
				if !envName.MatchString(name) {
					return nil, fmt.Errorf("%s: wall.env: %q is not a variable's name; the value comes from the environment, never from this file", path, name)
				}
				r.Wall.Env = append(r.Wall.Env, name)
			}
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
	if r != nil && r.Timeout > 0 {
		rows = append(rows, Row{"runner.run.timeout", r.Timeout.String(), origin})
	} else {
		rows = append(rows, Row{"runner.run.timeout", "(none)", Default})
	}
	if r != nil && r.StopGrace > 0 {
		rows = append(rows, Row{"runner.run.stop_grace", r.StopGrace.String(), origin})
	} else {
		rows = append(rows, Row{"runner.run.stop_grace", "10s", Default})
	}
	if r != nil && r.Wall != nil {
		rows = append(rows,
			Row{"runner.wall.adapter", r.Wall.Adapter, origin},
			Row{"runner.wall.image", listOrNone(strings.Fields(r.Wall.Image)), origin},
			Row{"runner.wall.env", listOrNone(r.Wall.Env), origin},
		)
		if r.Wall.Command != "" {
			rows = append(rows, Row{"runner.wall.command", r.Wall.Command, origin})
		}
		if r.Wall.Helper != "" {
			rows = append(rows, Row{"runner.wall.helper", r.Wall.Helper, origin})
		}
		if r.Wall.User != "" {
			rows = append(rows, Row{"runner.wall.user", r.Wall.User, origin})
		}
		if len(r.Wall.Mounts) > 0 {
			var mounts []string
			for _, m := range r.Wall.Mounts {
				if m.ReadOnly {
					mounts = append(mounts, m.Path+":ro")
				} else {
					mounts = append(mounts, m.Path)
				}
			}
			rows = append(rows, Row{"runner.wall.mounts", strings.Join(mounts, ", "), origin})
		}
		for _, l := range [][2]string{{"cpus", r.Wall.CPUs}, {"memory", r.Wall.Memory}, {"shm_size", r.Wall.ShmSize}} {
			if l[1] != "" {
				rows = append(rows, Row{"runner.wall." + l[0], l[1], origin})
			}
		}
		if r.Wall.PIDs > 0 {
			rows = append(rows, Row{"runner.wall.pids_limit", strconv.Itoa(r.Wall.PIDs), origin})
		}
	} else {
		rows = append(rows, Row{"runner.wall.adapter", "(none)", Default})
	}
	return rows
}
