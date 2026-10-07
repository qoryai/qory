package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/runner/accesskey"
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

// Runner is the machine's runner file, read.
type Runner struct {
	// File is the path read.
	File string
	// Egress is the run policy's egress section, nil when the file has none: observe
	// everything, with no list to deny by.
	Egress *RunnerEgress
	// Server is the server every run reports to, nil when the file sets none: the
	// events go to files alone.
	Server *RunnerServer
	// InstanceName is instance.name, this instance's display name on the server, empty
	// when the file sets none: the host name, or its first label.
	InstanceName string
	// Wall is what the runtime is enclosed in, nil when the file sets none: the runtime
	// is a process of this machine.
	Wall *RunnerWall
	// Timeout is how long a runtime may run on this machine, zero for no limit, and
	// StopSignal the signal that requests it to stop when the runner stops it and
	// StopGrace how long it has between that and SIGKILL, empty and zero for the runner's
	// defaults. --timeout, --stop-signal and --stop-grace set others for one run.
	Timeout    time.Duration
	StopSignal string
	StopGrace  time.Duration
	// Credentials are the credentials this machine defines, in the file's order. A
	// run's policy selects among them by name; the runner keeps each outside the
	// container and its proxy sets it on the requests to the hosts it is for. The ones
	// an integration defines are added by [Runner.Expand].
	Credentials []RunnerCredential
	// Integrations are the integrations this machine declares, in the file's order:
	// programs that speak the integration contract, which [Runner.Expand] describes and
	// expands into the definitions they return.
	Integrations []RunnerIntegration

	expanded bool
}

// RunnerCredential is one entry of the credentials section. Exactly one of Env, File
// and Adapter defines where the token comes from; an adapter defines how its token is
// used, and for the other two Hosts, Scheme, Username, Header and Paths do.
type RunnerCredential struct {
	Name                     string
	Env, File                string
	Adapter                  []string
	Argument                 string
	Hosts                    []string
	Scheme, Username, Header string
	Paths                    []string
	Placeholders             []string
	// Integration is the key of the integration the definition was expanded from,
	// empty for one of the file's credentials section.
	Integration string
}

// WallDocker is the one wall adapter there is.
const WallDocker = "docker"

// RunnerWall is the wall section: the container every run on this machine starts the
// runtime in, with no route out except to the runner's proxy.
type RunnerWall struct {
	// Adapter selects what builds the wall: docker.
	Adapter string
	// Image is the agent's image when the run's policy selects none, the runtime and the
	// project's toolchain: the name of one of Images, or a reference. A name Images
	// defines is read as that image first. The --image flag sets another; it may be
	// empty here and set by the flag.
	Image string
	// Images are the images this machine defines, in the file's order; a run's policy
	// selects among them by name.
	Images []RunnerImage
	// Command is the program the adapter runs, such as podman; empty means docker.
	Command string
	// Helper is the path of a static Linux build of qory, mounted into the container as
	// the relay, the hook forwarder and what starts an image's own Docker; empty means
	// this binary, which only a Linux machine can use.
	Helper string
	// Env lists the variables of this environment that go into the container, such as the
	// model credential. Nothing else of the environment does.
	Env []string
	// User is the uid:gid the container runs as; empty means qory run's own, so the
	// checkout's files keep their owner. Root is refused, so a machine where qory runs
	// as root sets one.
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
	// CAEnv lists the variables that point a program in the container at the bundle of
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
// :ro after it for one the container cannot change. :rw sets the default explicitly.
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
	// Deny are the hosts the runtime may not reach, in the same grammar, in either
	// mode: the runner decides them before the mode and the allow list. Passed to the
	// runner as written.
	Deny []string
}

// RunnerServer is the server section: the runner contract's server document, where a
// run discovers what to post its events to and where its configuration comes from. The
// access key's secret is never in it: it is the file access-key-secret beside the
// runner file, or QORY_ACCESS_KEY_SECRET.
type RunnerServer struct {
	// URL is the server: https, or http to a loopback address, a scheme and a host
	// alone.
	URL string
	// AccessKeyID is the access key's id, ak_ and 16 characters, which the server
	// assigned when the key enrolled; empty until one is enrolled. AccessKeyIDFrom is
	// where it came from: the runner file's path, or [EnvOrigin] and the variable.
	AccessKeyID     string
	AccessKeyIDFrom string
	// Pin is apiary_public_key, the server's keys every answer is verified under; empty
	// when neither the file nor the environment pins one. PinFrom is where it came from.
	Pin     accesskey.Pin
	PinFrom string
}

// EnvOrigin starts the origin of a value read from a variable: "$" and its name.
const EnvOrigin = "$"

// pinEntry is one entry of server.apiary_public_key as written.
type pinEntry struct {
	Alg       *string `yaml:"alg"`
	PublicKey *string `yaml:"public_key"`
}

// runnerFile is runner.yaml as written.
type runnerFile struct {
	APIVersion string `yaml:"apiVersion"`
	Egress     *struct {
		Mode  *string   `yaml:"mode"`
		Allow *[]string `yaml:"allow"`
		Deny  *[]string `yaml:"deny"`
	} `yaml:"egress,omitempty"`
	Server *struct {
		URL             *string     `yaml:"url"`
		AccessKeyID     *string     `yaml:"access_key_id"`
		ApiaryPublicKey *[]pinEntry `yaml:"apiary_public_key"`
		// Secret and AccessKey are read only to say where what they would hold belongs.
		Secret    yaml.Node `yaml:"secret,omitempty"`
		AccessKey yaml.Node `yaml:"access_key,omitempty"`
	} `yaml:"server,omitempty"`
	Instance *struct {
		Name *string `yaml:"name"`
	} `yaml:"instance,omitempty"`
	// Webhook is the section qory 0.10.0 replaced with server, read only to refuse it
	// by name instead of as a key the file does not read.
	Webhook yaml.Node `yaml:"webhook,omitempty"`
	Run     *struct {
		Timeout    *string `yaml:"timeout"`
		StopSignal *string `yaml:"stop_signal"`
		StopGrace  *string `yaml:"stop_grace"`
	} `yaml:"run,omitempty"`
	Credentials  yaml.Node `yaml:"credentials,omitempty"`
	Integrations yaml.Node `yaml:"integrations,omitempty"`
	Wall         *struct {
		Adapter *string   `yaml:"adapter"`
		Image   *string   `yaml:"image"`
		Images  yaml.Node `yaml:"images,omitempty"`
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
// configuration and returns nil; a file that does not read is an error that contains its
// path.
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
		if e.Deny != nil {
			for _, host := range *e.Deny {
				if !module.EgressHost.MatchString(host) {
					return nil, fmt.Errorf("%s: egress.deny: %q is not a lower-case host name or a *. suffix; no port, path or scheme", path, host)
				}
				r.Egress.Deny = append(r.Egress.Deny, host)
			}
		}
	}
	if f.Webhook.Kind != 0 {
		return nil, fmt.Errorf("%s: webhook: qory 0.10.0 replaced this section with server; see the runner file docs", path)
	}
	if w := f.Server; w != nil {
		if r.Server, err = readServer(path, w.URL, w.AccessKeyID, w.ApiaryPublicKey, &w.Secret, &w.AccessKey); err != nil {
			return nil, err
		}
	}
	if in := f.Instance; in != nil && in.Name != nil {
		if err := accesskey.CheckName(*in.Name); err != nil {
			return nil, fmt.Errorf("%s: instance.name: %w", path, err)
		}
		r.InstanceName = *in.Name
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
				return nil, fmt.Errorf("%s: %s %q is not a duration above zero, such as 5h30m or 30s", path, d.key, *d.in)
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
	if f.Integrations.Kind != 0 {
		if r.Integrations, err = readIntegrations(path, &f.Integrations); err != nil {
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
		if w.Images.Kind != 0 {
			if r.Wall.Images, err = readImages(path, &w.Images); err != nil {
				return nil, err
			}
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
				if RunnersOwn(name) {
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

// DefaultInstanceName is the display name of an instance whose runner file sets none:
// the host name, or its first label when the whole does not fit a name, or empty when
// neither does, and then the server is sent none.
func DefaultInstanceName() string {
	host, _ := os.Hostname()
	return accesskey.DefaultName(host)
}

// InstanceNameOrDefault is the instance's display name: instance.name, else
// [DefaultInstanceName].
func (r *Runner) InstanceNameOrDefault() string {
	if r != nil && r.InstanceName != "" {
		return r.InstanceName
	}
	return DefaultInstanceName()
}

// RunnersOwn reports whether a variable is the runner's own, never the session's: the
// access key's secret, its id and the pin.
func RunnersOwn(name string) bool {
	return name == accesskey.EnvSecret || name == accesskey.EnvID || name == accesskey.EnvPin
}

// enrolAsNode ends the refusal of a workspace access key: what to do instead.
const enrolAsNode = "enrol this machine as a node: qory access-key enrol <server> <code>, or qory access-key create and add its public key to the node; see https://github.com/qoryai/qory/blob/main/docs/run.md#the-access-key-and-the-instance"

// readServer reads the server section. The access key's id and the pin come from the
// file, else from QORY_ACCESS_KEY_ID and QORY_APIARY_PUBLIC_KEY as qory took them when
// it started, [TakenServerVariables]; both set is refused.
// Neither is required here: qory access-key enrol writes them, and a run without them is
// refused when it starts. A value that contains an access key secret is refused without
// being quoted. server.access_key, server.secret and QORY_SERVER_SECRET, a workspace
// access key's, are refused with what to do instead.
func readServer(path string, rawURL, id *string, pin *[]pinEntry, secret, key *yaml.Node) (*RunnerServer, error) {
	if key.Kind != 0 {
		return nil, fmt.Errorf("%s: server.access_key is a workspace access key, which servers no longer accept; %s", path, enrolAsNode)
	}
	if secret.Kind != 0 {
		return nil, fmt.Errorf("%s: server.secret is a workspace access key's secret, which servers no longer accept; %s", path, enrolAsNode)
	}
	env := TakenServerVariables()
	if env.WorkspaceSecret != "" {
		return nil, fmt.Errorf("%s holds a workspace access key's secret, which servers no longer accept; unset it, and %s", envWorkspaceSecret, enrolAsNode)
	}
	if rawURL == nil || *rawURL == "" {
		return nil, fmt.Errorf("%s: server.url is required", path)
	}
	if accesskey.ContainsSecret(*rawURL) {
		return nil, fmt.Errorf("%s: server.url: %w", path, accesskey.ErrSecretInDocument)
	}
	if err := CheckServerURL(*rawURL); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s := &RunnerServer{URL: *rawURL}
	envID, envPin := env.AccessKeyID, env.ApiaryPublicKey
	switch {
	case id != nil && envID != "":
		return nil, fmt.Errorf("%s: server.access_key_id is set, and so is %s; set one of them", path, accesskey.EnvID)
	case id != nil:
		if err := accesskey.CheckID(*id); err != nil {
			return nil, fmt.Errorf("%s: server.access_key_id: %w", path, err)
		}
		s.AccessKeyID, s.AccessKeyIDFrom = *id, path
	case envID != "":
		if err := accesskey.CheckID(envID); err != nil {
			return nil, fmt.Errorf("%s: %w", accesskey.EnvID, err)
		}
		s.AccessKeyID, s.AccessKeyIDFrom = envID, EnvOrigin+accesskey.EnvID
	}
	switch {
	case pin != nil && envPin != "":
		return nil, fmt.Errorf("%s: server.apiary_public_key is set, and so is %s; set one of them", path, accesskey.EnvPin)
	case pin != nil:
		for i, e := range *pin {
			if e.Alg == nil || e.PublicKey == nil {
				return nil, fmt.Errorf("%s: server.apiary_public_key[%d] has alg and public_key", path, i)
			}
			if accesskey.ContainsSecret(*e.Alg) || accesskey.ContainsSecret(*e.PublicKey) {
				return nil, fmt.Errorf("%s: server.apiary_public_key: %w", path, accesskey.ErrSecretInDocument)
			}
			s.Pin = append(s.Pin, accesskey.ServerKey{Alg: *e.Alg, PublicKey: *e.PublicKey})
		}
		if err := checkPin(s.Pin); err != nil {
			return nil, fmt.Errorf("%s: server.apiary_public_key: %w", path, err)
		}
		s.PinFrom = path
	case envPin != "":
		p, err := accesskey.ParsePin([]byte(envPin))
		if err == nil {
			err = checkPin(p)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", accesskey.EnvPin, err)
		}
		s.Pin, s.PinFrom = p, EnvOrigin+accesskey.EnvPin
	}
	return s, nil
}

// checkPin refuses a pin the runner cannot verify under, and one that lists a key of
// the runner contract's published fixtures, whose secrets anyone can read.
func checkPin(p accesskey.Pin) error {
	if err := p.Check(); err != nil {
		return err
	}
	if p.Fixture() {
		return errors.New("it lists the runner contract's published fixture key, whose secret anyone can read; pin your server's own key")
	}
	return nil
}

// CheckServerURL refuses a server URL that is not https, or http to this machine, with
// a scheme and a host alone.
func CheckServerURL(raw string) error {
	if accesskey.ContainsSecret(raw) {
		return fmt.Errorf("server.url: %w", accesskey.ErrSecretInDocument)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("server.url %q is not an https URL, or an http URL to this machine", raw)
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return fmt.Errorf("server.url %q is http to a host that is not this machine; a server elsewhere is reached over https", raw)
	}
	if u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil || strings.HasSuffix(raw, "#") {
		return fmt.Errorf("server.url %q is more than a scheme and a host; the server defines its own paths", raw)
	}
	return nil
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
	rows := []Row{{"runner.egress.mode", "observe", Default}, {"runner.egress.allow", "(none)", Default}, {"runner.egress.deny", "(none)", Default}}
	if r != nil && r.Egress != nil {
		rows[0] = Row{"runner.egress.mode", r.Egress.Mode, origin}
		rows[1] = Row{"runner.egress.allow", listOrNone(r.Egress.Allow), origin}
		rows[2] = Row{"runner.egress.deny", listOrNone(r.Egress.Deny), origin}
	}
	if r != nil && r.Server != nil {
		rows = append(rows, Row{"runner.server.url", r.Server.URL, origin})
		if r.Server.AccessKeyID != "" {
			rows = append(rows, Row{"runner.server.access_key_id", r.Server.AccessKeyID, r.Server.AccessKeyIDFrom})
		} else {
			rows = append(rows, Row{"runner.server.access_key_id", "(none: qory access-key enrol writes it)", Default})
		}
		if len(r.Server.Pin) > 0 {
			var fingerprints []string
			for _, k := range r.Server.Pin.Keys() {
				fingerprints = append(fingerprints, k.Fingerprint())
			}
			rows = append(rows, Row{"runner.server.apiary_public_key", "fingerprint " + strings.Join(fingerprints, ", "), r.Server.PinFrom})
		} else {
			rows = append(rows, Row{"runner.server.apiary_public_key", "(none: qory access-key enrol writes it)", Default})
		}
	} else {
		rows = append(rows, Row{"runner.server.url", "(none)", Default})
	}
	if r != nil && r.InstanceName != "" {
		rows = append(rows, Row{"runner.instance.name", r.InstanceName, origin})
	} else {
		rows = append(rows, Row{"runner.instance.name", DefaultInstanceName(), Default})
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
			case c.Integration != "":
				from = "integration " + c.Integration
			case len(c.Adapter) > 0:
				from = "adapter " + c.Adapter[0]
			}
			rows = append(rows, Row{"runner.credentials." + c.Name, from, origin})
		}
		shadowed := r.Shadowed()
		for _, in := range r.Integrations {
			program := in.Program
			if in.Path != "" {
				program = in.Path + " " + in.Version
			}
			if slices.Contains(shadowed, in.Key) {
				program += ", shadowed by credentials." + in.Key
			}
			rows = append(rows, Row{"runner.integrations." + in.Key, program, origin})
		}
	}
	if r != nil && r.Wall != nil {
		rows = append(rows,
			Row{"runner.wall.adapter", r.Wall.Adapter, origin},
			Row{"runner.wall.image", defaultRow(r.Wall), origin},
		)
		for _, i := range r.Wall.Images {
			rows = append(rows, Row{"runner.wall.images." + i.Name, imageRow(i), origin})
		}
		rows = append(rows, Row{"runner.wall.env", listOrNone(r.Wall.Env), origin})
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
