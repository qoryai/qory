package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/runner/session"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/stack"
)

// RunnerFileName is the machine's runner file, under [UserDir] alone: what qory run
// does on this machine. It has no counterpart in a repository, so a checkout cannot set
// the policy the agent runs under or where the run's events go.
const RunnerFileName = "runner.yaml"

// EnvServerSecret is the environment variable that holds the server's secret when the
// file does not.
const EnvServerSecret = "QORY_SERVER_SECRET"

// accessKey is the shape of a server's access key: ak_ and 16 characters of Crockford
// base32 in lower case.
var accessKey = regexp.MustCompile(`^ak_[0-9a-hjkmnp-tv-z]{16}$`)

// Runner is the machine's runner file, read.
type Runner struct {
	// File is the path read.
	File string
	// Egress is the run policy's egress section, nil when the file names none: observe
	// everything, deny nothing.
	Egress *RunnerEgress
	// Server is the server every run reports to, nil when the file names none: the
	// events go to files alone.
	Server *RunnerServer
	// Wall is what the runtime is enclosed in, nil when the file names none: the runtime
	// is a process of this machine.
	Wall *RunnerWall
	// Timeout is how long a runtime may run on this machine, zero for no limit, and
	// StopSignal the signal that asks it to leave when the runner stops it and StopGrace
	// how long it gets between that and SIGKILL, empty and zero for the runner's
	// defaults. --timeout, --stop-signal and --stop-grace name others for one run.
	Timeout    time.Duration
	StopSignal string
	StopGrace  time.Duration
	// Credentials are the credentials this machine defines, in the file's order. A
	// run's policy selects among them by name; the runner holds each outside the
	// container and its proxy sets it on the requests to the hosts it is for.
	Credentials []RunnerCredential
}

// RunnerCredential is one entry of the credentials section. Exactly one of Env, File
// and Adapter says where the token comes from; an adapter says how its token is used,
// and for the other two Hosts, Scheme, Username, Header and Paths do.
type RunnerCredential struct {
	Name                     string
	Env, File                string
	Adapter                  []string
	Argument                 string
	Hosts                    []string
	Scheme, Username, Header string
	Paths                    []string
	Placeholders             []string
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
	// CAEnv names the variables that point a program in the container at the bundle of
	// authorities, when a run has one of its own; nil means the runner's defaults.
	CAEnv []string
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

// RunnerServer is the server section: the runner contract's server document, where a
// run discovers what to post its events to and where its configuration comes from.
type RunnerServer struct {
	// URL is the server: https, or http to a loopback address, a scheme and a host
	// alone.
	URL string
	// AccessKey names the key the server issued this machine: ak_ and 16 characters.
	AccessKey string
	// Secret signs every request; from the file, or from [EnvServerSecret]. It never
	// travels.
	Secret string
	// FromEnv is set when the secret came from the environment.
	FromEnv bool
}

// runnerFile is runner.yaml as written.
type runnerFile struct {
	APIVersion string `yaml:"apiVersion"`
	Egress     *struct {
		Mode  *string   `yaml:"mode"`
		Allow *[]string `yaml:"allow"`
	} `yaml:"egress,omitempty"`
	Server *struct {
		URL       *string `yaml:"url"`
		AccessKey *string `yaml:"access_key"`
		Secret    *string `yaml:"secret"`
	} `yaml:"server,omitempty"`
	// Webhook is the section qory 0.10.0 replaced with server, read only to refuse it
	// by name instead of as a key the file does not read.
	Webhook yaml.Node `yaml:"webhook,omitempty"`
	Run     *struct {
		Timeout    *string `yaml:"timeout"`
		StopSignal *string `yaml:"stop_signal"`
		StopGrace  *string `yaml:"stop_grace"`
	} `yaml:"run,omitempty"`
	Credentials yaml.Node `yaml:"credentials,omitempty"`
	Wall        *struct {
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
		CAEnv   *[]string `yaml:"ca_env"`
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
	if f.Webhook.Kind != 0 {
		return nil, fmt.Errorf("%s: webhook: qory 0.10.0 replaced this section with server; see the runner file docs", path)
	}
	if w := f.Server; w != nil {
		if w.URL == nil || *w.URL == "" {
			return nil, fmt.Errorf("%s: server.url is required", path)
		}
		u, err := url.Parse(*w.URL)
		if err != nil || u.Host == "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("%s: server.url %q is not an https URL, or an http URL to this machine", path, *w.URL)
		}
		if u.Scheme == "http" && !loopback(u.Hostname()) {
			return nil, fmt.Errorf("%s: server.url %q is http to a host that is not this machine; a server elsewhere is reached over https", path, *w.URL)
		}
		if u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil || strings.HasSuffix(*w.URL, "#") {
			return nil, fmt.Errorf("%s: server.url %q is more than a scheme and a host; the server names its own paths", path, *w.URL)
		}
		if w.AccessKey == nil || *w.AccessKey == "" {
			return nil, fmt.Errorf("%s: server.access_key is required", path)
		}
		if !accessKey.MatchString(*w.AccessKey) {
			return nil, fmt.Errorf("%s: server.access_key %q is not an access key: ak_ and 16 characters", path, *w.AccessKey)
		}
		r.Server = &RunnerServer{URL: *w.URL, AccessKey: *w.AccessKey}
		switch {
		case w.Secret != nil && *w.Secret != "":
			r.Server.Secret = *w.Secret
		case os.Getenv(EnvServerSecret) != "":
			r.Server.Secret, r.Server.FromEnv = os.Getenv(EnvServerSecret), true
		default:
			return nil, fmt.Errorf("%s: server.secret is missing; set it there or in %s", path, EnvServerSecret)
		}
		if len(r.Server.Secret) < 16 {
			return nil, fmt.Errorf("%s: server.secret is shorter than 16 characters", path)
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
		if run.StopSignal != nil {
			if *run.StopSignal == "" {
				return nil, fmt.Errorf("%s: run.stop_signal is empty", path)
			}
			if err := session.CheckStopSignal(*run.StopSignal); err != nil {
				return nil, fmt.Errorf("%s: run.stop_signal: %w", path, err)
			}
			r.StopSignal = *run.StopSignal
		}
	}
	if f.Credentials.Kind != 0 {
		if r.Credentials, err = readCredentials(path, &f.Credentials); err != nil {
			return nil, err
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
		if w.CAEnv != nil {
			for _, name := range *w.CAEnv {
				if !envName.MatchString(name) {
					return nil, fmt.Errorf("%s: wall.ca_env: %q is not a variable's name", path, name)
				}
				r.Wall.CAEnv = append(r.Wall.CAEnv, name)
			}
		}
		if w.Env != nil {
			for _, name := range *w.Env {
				if name == EnvServerSecret {
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

// credentialFile is one entry of the credentials section as written.
type credentialFile struct {
	Env      *string   `yaml:"env"`
	File     *string   `yaml:"file"`
	Adapter  *[]string `yaml:"adapter"`
	Argument *string   `yaml:"argument"`
	Hosts    *[]string `yaml:"hosts"`
	Paths    *[]string `yaml:"paths"`
	Auth     *struct {
		Scheme   *string `yaml:"scheme"`
		Username *string `yaml:"username"`
		Header   *string `yaml:"header"`
	} `yaml:"auth"`
	Placeholders *[]string `yaml:"placeholders"`
}

// readCredentials reads the credentials section, a mapping from name to entry, in the
// file's order. What an entry may hold together is the runner's to say, and qory run
// asks it before a run.
func readCredentials(path string, node *yaml.Node) ([]RunnerCredential, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: credentials is a mapping from a name to a credential", path)
	}
	var out []RunnerCredential
	for i := 0; i+1 < len(node.Content); i += 2 {
		c := RunnerCredential{Name: node.Content[i].Value}
		var f credentialFile
		// A node decodes loosely, so a key that is not one is refused here as the rest
		// of the file refuses it.
		if entry := node.Content[i+1]; entry.Kind == yaml.MappingNode {
			for k := 0; k+1 < len(entry.Content); k += 2 {
				switch key := entry.Content[k].Value; key {
				case "env", "file", "adapter", "argument", "hosts", "paths", "auth", "placeholders":
				default:
					return nil, fmt.Errorf("%s: credentials.%s: key %q is not one", path, c.Name, key)
				}
			}
		}
		if err := node.Content[i+1].Decode(&f); err != nil {
			return nil, fmt.Errorf("%s: credentials.%s: %w", path, c.Name, err)
		}
		if f.Env != nil {
			c.Env = *f.Env
		}
		if f.File != nil {
			if !filepath.IsAbs(*f.File) {
				return nil, fmt.Errorf("%s: credentials.%s.file %q is not an absolute path", path, c.Name, *f.File)
			}
			c.File = *f.File
		}
		if f.Adapter != nil {
			if len(*f.Adapter) == 0 || !filepath.IsAbs((*f.Adapter)[0]) {
				return nil, fmt.Errorf("%s: credentials.%s.adapter is a program by its absolute path, and its arguments", path, c.Name)
			}
			c.Adapter = *f.Adapter
		}
		if f.Argument != nil {
			c.Argument = *f.Argument
		}
		if f.Hosts != nil {
			c.Hosts = *f.Hosts
		}
		if f.Paths != nil {
			c.Paths = *f.Paths
		}
		if f.Auth != nil {
			if f.Auth.Scheme != nil {
				c.Scheme = *f.Auth.Scheme
			}
			if f.Auth.Username != nil {
				c.Username = *f.Auth.Username
			}
			if f.Auth.Header != nil {
				c.Header = *f.Auth.Header
			}
		}
		if f.Placeholders != nil {
			c.Placeholders = *f.Placeholders
		}
		out = append(out, c)
	}
	return out, nil
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
	if r != nil && r.Server != nil {
		rows = append(rows,
			Row{"runner.server.url", r.Server.URL, origin},
			Row{"runner.server.access_key", r.Server.AccessKey, origin},
		)
	} else {
		rows = append(rows, Row{"runner.server.url", "(none)", Default})
	}
	if r != nil && r.Timeout > 0 {
		rows = append(rows, Row{"runner.run.timeout", r.Timeout.String(), origin})
	} else {
		rows = append(rows, Row{"runner.run.timeout", "(none)", Default})
	}
	if r != nil && r.StopSignal != "" {
		rows = append(rows, Row{"runner.run.stop_signal", r.StopSignal, origin})
	} else {
		rows = append(rows, Row{"runner.run.stop_signal", "(the runtime's, else " + session.DefaultStopSignal + ")", Default})
	}
	if r != nil && r.StopGrace > 0 {
		rows = append(rows, Row{"runner.run.stop_grace", r.StopGrace.String(), origin})
	} else {
		rows = append(rows, Row{"runner.run.stop_grace", "(the runtime's, else 10s)", Default})
	}
	if r != nil {
		for _, c := range r.Credentials {
			from := "env " + c.Env
			switch {
			case c.File != "":
				from = "file " + c.File
			case len(c.Adapter) > 0:
				from = "adapter " + c.Adapter[0]
			}
			rows = append(rows, Row{"runner.credentials." + c.Name, from, origin})
		}
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
		if len(r.Wall.CAEnv) > 0 {
			rows = append(rows, Row{"runner.wall.ca_env", strings.Join(r.Wall.CAEnv, ", "), origin})
		}
		if r.Wall.PIDs > 0 {
			rows = append(rows, Row{"runner.wall.pids_limit", strconv.Itoa(r.Wall.PIDs), origin})
		}
	} else {
		rows = append(rows, Row{"runner.wall.adapter", "(none)", Default})
	}
	return rows
}
