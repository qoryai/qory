package config

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/runcredential"
	"github.com/qoryai/forager/session"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/stack"
)

// ForagerFileName is the machine's forager.yaml, under [UserDir] alone: what qory run
// does on this machine. It has no counterpart in a repository, so a checkout cannot set
// the policy the agent runs under or where the run's events go.
const ForagerFileName = "forager.yaml"

// Forager is the machine's forager.yaml, read.
type Forager struct {
	// File is the path read.
	File string
	// Egress is the run policy's egress section, nil when the file has none: observe
	// everything, with no list to deny by.
	Egress *ForagerEgress
	// Server is the server every run reports to, nil when the file sets none: the
	// events go to files alone.
	Server *ForagerServer
	// InstanceName is session.instance.name, this instance's display name on the server, empty
	// when the file sets none: the host name, or its first label.
	InstanceName string
	// Wall is what the runtime is enclosed in, nil when the file sets none: the runtime
	// is a process of this machine.
	Wall *ForagerWall
	// Timeout is how long a runtime may run on this machine, zero for no limit, and
	// StopSignal the signal that requests it to stop when Forager stops it and
	// StopGrace how long it has between that and SIGKILL, empty and zero for Forager's
	// defaults. --timeout, --stop-signal and --stop-grace set others for one run.
	Timeout    time.Duration
	StopSignal string
	StopGrace  time.Duration
	// Credentials are the credentials this machine defines, in the file's order. A
	// run's policy selects among them by name; Forager keeps each outside the
	// container and its proxy sets it on the requests to the hosts it is for. The ones
	// an integration defines are added by [Forager.Expand].
	Credentials []ForagerCredential
	// Integrations are the integrations this machine declares, in the file's order:
	// programs that speak the integration contract, which [Forager.Expand] describes and
	// expands into the definitions they return.
	Integrations []ForagerIntegration
	// Gateway reports whether the file has a gateway section: qory gateway runs none
	// without one.
	Gateway bool
	// Listen is gateway.listen, the address qory gateway listens on for the runs of
	// other machines, host:port; empty when the file sets none. qory run does not use it.
	Listen string
	// TLS is gateway.tls, the certificate and key qory gateway serves Listen with; nil
	// when the file sets none.
	TLS *ForagerTLS
	// SessionGateway is session.gateway, the gateway on another machine, or a service on
	// this one, that this machine's runs go through; nil when the file sets none, and
	// qory run starts a gateway of its own for each run.
	SessionGateway *ForagerSessionGateway
	// RunCredentials is gateway.run_credentials, the issuers whose run credentials open
	// runs at qory gateway, in Forager's run-credentials.schema.json shape, with every
	// path as the file writes it; [Forager.Path] resolves one.
	RunCredentials runcredential.Issuers

	// runCredentialRows are the rows of gateway.run_credentials, each value as the file
	// writes it.
	runCredentialRows []Row
	expanded          bool
}

// ForagerTLS is gateway.tls: the files of the certificate and its key, as the file
// writes them; [Forager.Path] resolves one. qory never prints the key file's contents.
type ForagerTLS struct {
	Certificate string
	Key         string
}

// ForagerSessionGateway is session.gateway: the gateway's URL, the PEM file of the
// authorities its certificate chains to, the pin of its certificate's public key, and the
// file of the run's run credential, each as the file writes it; [Forager.Path] resolves a
// path. A setting the file does not write is empty.
type ForagerSessionGateway struct {
	URL               string
	CAFile            string
	CertificateSHA256 string
	RunCredentialFile string
}

// certificatePin is session.gateway.certificate_sha256's shape: 32 bytes in standard
// base64 with padding, as Forager's session takes it.
var certificatePin = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

// CertificatePin reports whether v is a certificate pin as Forager's session takes it:
// the SHA-256 of a public key, 32 bytes, in standard base64 with padding.
func CertificatePin(v string) bool {
	if !certificatePin.MatchString(v) {
		return false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(v)
	return err == nil && len(b) == sha256.Size
}

// GatewayURL reports whether v is a gateway's URL as Forager's session takes it: https,
// a host and an optional port, and no user information, path other than "/", query or
// fragment.
func GatewayURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Hostname() != "" && u.Opaque == "" && u.User == nil &&
		(u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

// Path is a path a setting of the file names, resolved: an absolute one as it is, a
// relative one under the file's directory.
func (r *Forager) Path(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(r.File), p)
}

// HostPort reports whether v is host:port, such as 0.0.0.0:8443: a port of 0 to 65535,
// and a host that may be empty, for every address of the machine.
func HostPort(v string) bool {
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 0 && n <= 65535
}

// ForagerCredential is one entry of the credentials section. Exactly one of Env, File
// and Adapter defines where the token comes from; an adapter defines how its token is
// used, and for the other two Hosts, Scheme, Username, Header and Paths do.
type ForagerCredential struct {
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

// ForagerWall is the wall section: the container every run on this machine starts the
// runtime in, with no route out except to the gateway's proxy.
type ForagerWall struct {
	// Adapter selects what builds the wall: docker.
	Adapter string
	// Image is the agent's image when the run's policy selects none, the runtime and the
	// project's toolchain: the name of one of Images, or a reference. A name Images
	// defines is read as that image first. The --image flag sets another; it may be
	// empty here and set by the flag.
	Image string
	// Images are the images this machine defines, in the file's order; a run's policy
	// selects among them by name.
	Images []ForagerImage
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
	Mounts []ForagerMount
	// CPUs, Memory, PIDs and ShmSize limit what the container uses, as docker run's
	// --cpus, --memory, --pids-limit and --shm-size do; empty or zero is the engine's
	// default, and a flag of the same name sets another for one run.
	CPUs    string
	Memory  string
	PIDs    int
	ShmSize string
	// CAEnv lists the variables that point a program in the container at the bundle of
	// authorities, when a run has one of its own; nil means Forager's defaults.
	CAEnv []string
}

// ForagerMount is one file or directory of this machine a walled run sees, at the same
// path.
type ForagerMount struct {
	Path     string
	ReadOnly bool
}

// ParseMount reads a mount as wall.mounts and --mount write it: an absolute path, and
// :ro after it for one the container cannot change. :rw sets the default explicitly.
func ParseMount(v string) (ForagerMount, error) {
	m := ForagerMount{Path: v}
	if p, ok := strings.CutSuffix(v, ":ro"); ok {
		m = ForagerMount{Path: p, ReadOnly: true}
	} else if p, ok := strings.CutSuffix(v, ":rw"); ok {
		m.Path = p
	}
	if !filepath.IsAbs(m.Path) {
		return m, fmt.Errorf("the mount %q is not an absolute path, with :ro after it for a read-only one", v)
	}
	m.Path = filepath.Clean(m.Path)
	return m, nil
}

// ForagerEgress is the egress section: the policy Forager pins for every run on this
// machine, in the Forager contract's grammar.
type ForagerEgress struct {
	// Mode is observe or enforce.
	Mode string
	// Allow are the hosts the runtime may reach, each a lower-case name or a *. suffix.
	Allow []string
	// Deny are the hosts the runtime may not reach, in the same grammar, in either
	// mode: Forager decides them before the mode and the allow list. Passed to
	// the gateway as written.
	Deny []string
}

// ForagerServer is the server section: the Forager contract's server document, where a
// run discovers what to post its events to and where its configuration comes from. The
// access key's secret is never in it: it is the file access-key-secret beside
// forager.yaml, or QORY_ACCESS_KEY_SECRET.
type ForagerServer struct {
	// URL is the server: https, or http to a loopback address, a scheme and a host
	// alone.
	URL string
	// AccessKeyID is the access key's id, ak_ and 16 characters, which the server
	// assigned when the key enrolled; empty until one is enrolled. AccessKeyIDFrom is
	// where it came from: forager.yaml's path, or [EnvOrigin] and the variable.
	AccessKeyID     string
	AccessKeyIDFrom string
	// Pin is apiary_public_key, the server's keys every answer is verified under; empty
	// when neither the file nor the environment pins one. PinFrom is where it came from.
	Pin     accesskey.Pin
	PinFrom string
}

// EnvOrigin starts the origin of a value read from a variable: "$" and its name.
const EnvOrigin = "$"

// pinEntry is one entry of gateway.server.apiary_public_key as written.
type pinEntry struct {
	Alg       *string `yaml:"alg"`
	PublicKey *string `yaml:"public_key"`
}

// foragerFile is forager.yaml as written: the gateway's sections, the session's and the
// wall's.
type foragerFile struct {
	APIVersion string `yaml:"apiVersion"`
	// Webhook is the section qory 0.10.0 replaced with server, read only to refuse it
	// by name instead of as a key the file does not read.
	Webhook yaml.Node       `yaml:"webhook,omitempty"`
	Gateway *gatewaySection `yaml:"gateway,omitempty"`
	Session *sessionSection `yaml:"session,omitempty"`
	Wall    *struct {
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

// gatewaySection is the gateway section as written: the egress policy, the server, the
// credentials and the integrations.
type gatewaySection struct {
	Egress *struct {
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
	Credentials  yaml.Node `yaml:"credentials,omitempty"`
	Integrations yaml.Node `yaml:"integrations,omitempty"`
	Listen       *string   `yaml:"listen"`
	TLS          *struct {
		Certificate *string `yaml:"certificate"`
		Key         *string `yaml:"key"`
	} `yaml:"tls,omitempty"`
	RunCredentials yaml.Node `yaml:"run_credentials,omitempty"`
}

// sessionSection is the session section as written: the gateway, the instance and the
// run.
type sessionSection struct {
	Gateway *struct {
		URL               *string `yaml:"url"`
		CAFile            *string `yaml:"ca_file"`
		CertificateSHA256 *string `yaml:"certificate_sha256"`
		RunCredentialFile *string `yaml:"run_credential_file"`
	} `yaml:"gateway,omitempty"`
	Instance *instanceSection `yaml:"instance,omitempty"`
	Run      *struct {
		Timeout    *string `yaml:"timeout"`
		StopSignal *string `yaml:"stop_signal"`
		StopGrace  *string `yaml:"stop_grace"`
	} `yaml:"run,omitempty"`
}

// instanceSection is session.instance as written.
type instanceSection struct {
	Name *string `yaml:"name"`
}

// LoadForager reads the machine's forager.yaml under [UserDir]. No file is no Forager
// configuration and returns nil; a file that does not read is an error that contains its
// path.
func LoadForager() (*Forager, error) {
	path, data, err := readForagerFile()
	if data == nil || err != nil {
		return nil, err
	}
	var f foragerFile
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
	r := &Forager{File: path, Gateway: f.Gateway != nil}
	g, ses := f.Gateway, f.Session
	if g == nil {
		g = &gatewaySection{}
	}
	if ses == nil {
		ses = &sessionSection{}
	}
	if e := g.Egress; e != nil {
		if e.Mode == nil {
			return nil, fmt.Errorf("%s: gateway.egress.mode is required: observe or enforce", path)
		}
		if *e.Mode != "observe" && *e.Mode != "enforce" {
			return nil, fmt.Errorf("%s: gateway.egress.mode %q is not observe or enforce", path, *e.Mode)
		}
		r.Egress = &ForagerEgress{Mode: *e.Mode}
		if e.Allow != nil {
			for _, host := range *e.Allow {
				if !module.EgressHost.MatchString(host) {
					return nil, fmt.Errorf("%s: gateway.egress.allow: %q is not a lower-case host name or a *. suffix; no port, path or scheme", path, host)
				}
				r.Egress.Allow = append(r.Egress.Allow, host)
			}
		}
		if e.Deny != nil {
			for _, host := range *e.Deny {
				if !module.EgressHost.MatchString(host) {
					return nil, fmt.Errorf("%s: gateway.egress.deny: %q is not a lower-case host name or a *. suffix; no port, path or scheme", path, host)
				}
				r.Egress.Deny = append(r.Egress.Deny, host)
			}
		}
	}
	if f.Webhook.Kind != 0 {
		return nil, fmt.Errorf("%s: webhook: qory 0.10.0 replaced this section with gateway.server; see the docs of %s", path, ForagerFileName)
	}
	if w := g.Server; w != nil {
		if r.Server, err = readServer(path, w.URL, w.AccessKeyID, w.ApiaryPublicKey, &w.Secret, &w.AccessKey); err != nil {
			return nil, err
		}
	}
	if sg := ses.Gateway; sg != nil {
		// A machine whose runs go through a gateway runs none: what a gateway section
		// holds belongs on the gateway's machine.
		if f.Gateway != nil {
			return nil, fmt.Errorf("%s: gateway: this machine's runs go through the gateway session.gateway.url names, so it runs no gateway, and its file holds none: Qory Apiary's access key and the credentials' secrets belong on the gateway's machine. Remove the gateway section, or remove session.gateway to run the gateway here", path)
		}
		if sg.URL == nil || *sg.URL == "" {
			return nil, fmt.Errorf("%s: session.gateway.url is required", path)
		}
		if !GatewayURL(*sg.URL) {
			return nil, fmt.Errorf("%s: session.gateway.url %q is not an https URL of a host and an optional port, with nothing after", path, *sg.URL)
		}
		r.SessionGateway = &ForagerSessionGateway{URL: *sg.URL}
		for _, v := range []struct {
			key string
			in  *string
			out *string
		}{{"ca_file", sg.CAFile, &r.SessionGateway.CAFile}, {"certificate_sha256", sg.CertificateSHA256, &r.SessionGateway.CertificateSHA256}, {"run_credential_file", sg.RunCredentialFile, &r.SessionGateway.RunCredentialFile}} {
			if v.in == nil {
				continue
			}
			// A key that is present holds a value: an empty pin would turn the pin off.
			if *v.in == "" {
				return nil, fmt.Errorf("%s: session.gateway.%s is empty", path, v.key)
			}
			*v.out = *v.in
		}
		if p := r.SessionGateway.CertificateSHA256; p != "" && !CertificatePin(p) {
			return nil, fmt.Errorf("%s: session.gateway.certificate_sha256 %q is not the SHA-256 of a public key in standard base64 with padding, 44 characters ending in =", path, p)
		}
	}
	if in := ses.Instance; in != nil && in.Name != nil {
		if err := accesskey.CheckName(*in.Name); err != nil {
			return nil, fmt.Errorf("%s: session.instance.name: %w", path, err)
		}
		r.InstanceName = *in.Name
	}
	if run := ses.Run; run != nil {
		for _, d := range []struct {
			key string
			in  *string
			out *time.Duration
		}{{"session.run.timeout", run.Timeout, &r.Timeout}, {"session.run.stop_grace", run.StopGrace, &r.StopGrace}} {
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
				return nil, fmt.Errorf("%s: session.run.stop_signal is empty", path)
			}
			if err := session.CheckStopSignal(*run.StopSignal); err != nil {
				return nil, fmt.Errorf("%s: session.run.stop_signal: %w", path, err)
			}
			r.StopSignal = *run.StopSignal
		}
	}
	if g.Credentials.Kind != 0 {
		if r.Credentials, err = readCredentials(path, &g.Credentials); err != nil {
			return nil, err
		}
	}
	if g.Integrations.Kind != 0 {
		if r.Integrations, err = readIntegrations(path, &g.Integrations); err != nil {
			return nil, err
		}
	}
	if g.Listen != nil {
		if !HostPort(*g.Listen) {
			return nil, fmt.Errorf("%s: gateway.listen %q is not host:port, such as 0.0.0.0:8443", path, *g.Listen)
		}
		r.Listen = *g.Listen
	}
	if t := g.TLS; t != nil {
		if t.Certificate == nil || *t.Certificate == "" || t.Key == nil || *t.Key == "" {
			return nil, fmt.Errorf("%s: gateway.tls needs both certificate and key", path)
		}
		r.TLS = &ForagerTLS{Certificate: *t.Certificate, Key: *t.Key}
	}
	if g.RunCredentials.Kind != 0 {
		if r.RunCredentials, r.runCredentialRows, err = readRunCredentials(path, &g.RunCredentials); err != nil {
			return nil, err
		}
	}
	if w := f.Wall; w != nil {
		if w.Adapter == nil || *w.Adapter != WallDocker {
			return nil, fmt.Errorf("%s: wall.adapter is required, and %s is the one there is", path, WallDocker)
		}
		r.Wall = &ForagerWall{Adapter: *w.Adapter}
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
				if ForagersOwn(name) {
					return nil, fmt.Errorf("%s: wall.env: %s is Forager's own and never the agent's", path, name)
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

// readForagerFile reads the machine's forager.yaml under [UserDir]: its path and its
// content, which is nil when there is no file.
func readForagerFile() (string, []byte, error) {
	dir := UserDir()
	if dir == "" {
		return "", nil, nil
	}
	path := filepath.Join(dir, ForagerFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil, nil
	}
	if err != nil {
		return path, nil, err
	}
	if data == nil {
		data = []byte{}
	}
	return path, data, nil
}

// LoadForagerInstance reads session.instance.name alone from the machine's forager.yaml under
// [UserDir], for a key made for another machine, whose server section does not apply:
// it is the [Forager] with File and InstanceName set, and nil when there is no file. The
// file must still be YAML, and session.instance.name a name; every other key is left unread.
func LoadForagerInstance() (*Forager, error) {
	path, data, err := readForagerFile()
	if data == nil || err != nil {
		return nil, err
	}
	var f struct {
		Session *struct {
			Instance *instanceSection `yaml:"instance,omitempty"`
		} `yaml:"session,omitempty"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, decodeError(path, err)
	}
	r := &Forager{File: path}
	if f.Session == nil {
		return r, nil
	}
	if in := f.Session.Instance; in != nil && in.Name != nil {
		if err := accesskey.CheckName(*in.Name); err != nil {
			return nil, fmt.Errorf("%s: session.instance.name: %w", path, err)
		}
		r.InstanceName = *in.Name
	}
	return r, nil
}

// DefaultInstanceName is the display name of an instance whose forager.yaml sets none:
// the host name, or its first label when the whole does not fit a name, or empty when
// neither does, and then the server is sent none.
func DefaultInstanceName() string {
	host, _ := os.Hostname()
	return accesskey.DefaultName(host)
}

// InstanceNameOrDefault is the instance's display name: session.instance.name, else
// [DefaultInstanceName].
func (r *Forager) InstanceNameOrDefault() string {
	if r != nil && r.InstanceName != "" {
		return r.InstanceName
	}
	return DefaultInstanceName()
}

// ForagersOwn reports whether a variable is Forager's own, never the session's: the
// access key's secret, its id and the pin, QORY_SERVER_SECRET, which held a workspace
// access key's secret, and QORY_RUN_CREDENTIAL_SECRET, the run credential.
func ForagersOwn(name string) bool {
	return slices.Contains(serverVariableNames, name)
}

// enrolAsNode ends the refusal of a workspace access key: what to do instead.
const enrolAsNode = "connect this machine as a node: run qory access-key enrol <server> <code>, or generate a key on the node's page in Qory Apiary and set the QORY_ variables it shows; see https://github.com/qoryai/qory/blob/main/docs/run.md#the-access-key-and-the-instance"

// removeThenEnrol ends the refusal of a workspace access key in forager.yaml: the
// keys go first, since qory access-key enrol reads the file and would refuse them too.
const removeThenEnrol = "remove gateway.server.access_key and gateway.server.secret from " + ForagerFileName + ", then " + enrolAsNode

// readServer reads the server section. The access key's id and the pin come from the
// file, else from QORY_ACCESS_KEY_ID and QORY_APIARY_PUBLIC_KEY as qory took them when
// it started, [TakenServerVariables]; both set is refused.
// Neither is required here: qory access-key enrol writes them, and a run without them is
// refused when it starts. A value that contains an access key secret is refused without
// being quoted. gateway.server.access_key, gateway.server.secret and QORY_SERVER_SECRET, a workspace
// access key's, are refused with what to do instead.
func readServer(path string, rawURL, id *string, pin *[]pinEntry, secret, key *yaml.Node) (*ForagerServer, error) {
	if key.Kind != 0 {
		return nil, fmt.Errorf("%s: gateway.server.access_key is a workspace access key, which servers no longer accept; %s", path, removeThenEnrol)
	}
	if secret.Kind != 0 {
		return nil, fmt.Errorf("%s: gateway.server.secret is a workspace access key's secret, which servers no longer accept; %s", path, removeThenEnrol)
	}
	env := TakenServerVariables()
	if env.WorkspaceSecret != "" {
		return nil, fmt.Errorf("%s holds a workspace access key's secret, which servers no longer accept; unset it, and %s", envWorkspaceSecret, enrolAsNode)
	}
	if rawURL == nil || *rawURL == "" {
		return nil, fmt.Errorf("%s: gateway.server.url is required", path)
	}
	if accesskey.ContainsSecret(*rawURL) {
		return nil, fmt.Errorf("%s: gateway.server.url: %w", path, accesskey.ErrSecretInDocument)
	}
	if err := CheckServerURL(*rawURL); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s := &ForagerServer{URL: *rawURL}
	envID, envPin := env.AccessKeyID, env.ApiaryPublicKey
	switch {
	case id != nil && envID != "":
		return nil, fmt.Errorf("%s: gateway.server.access_key_id is set, and so is %s; set one of them", path, accesskey.EnvID)
	case id != nil:
		if err := accesskey.CheckID(*id); err != nil {
			return nil, fmt.Errorf("%s: gateway.server.access_key_id: %w", path, err)
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
		return nil, fmt.Errorf("%s: gateway.server.apiary_public_key is set, and so is %s; set one of them", path, accesskey.EnvPin)
	case pin != nil:
		for i, e := range *pin {
			if e.Alg == nil || e.PublicKey == nil {
				return nil, fmt.Errorf("%s: gateway.server.apiary_public_key[%d] needs both alg and public_key", path, i)
			}
			if accesskey.ContainsSecret(*e.Alg) || accesskey.ContainsSecret(*e.PublicKey) {
				return nil, fmt.Errorf("%s: gateway.server.apiary_public_key: %w", path, accesskey.ErrSecretInDocument)
			}
			s.Pin = append(s.Pin, accesskey.ServerKey{Alg: *e.Alg, PublicKey: *e.PublicKey})
		}
		if err := checkPin(s.Pin); err != nil {
			return nil, fmt.Errorf("%s: gateway.server.apiary_public_key: %w", path, err)
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

// checkPin refuses a pin Forager cannot verify under, and one that lists a key of
// the Forager contract's published fixtures, whose secrets anyone can read.
func checkPin(p accesskey.Pin) error {
	if err := p.Check(); err != nil {
		return err
	}
	if p.Fixture() {
		return errors.New("it lists the Forager contract's published fixture key, whose secret anyone can read; pin your server's own key")
	}
	return nil
}

// CheckServerURL refuses a server URL that is not https, or http to this machine, with
// a scheme and a host alone.
func CheckServerURL(raw string) error {
	if accesskey.ContainsSecret(raw) {
		return fmt.Errorf("gateway.server.url: %w", accesskey.ErrSecretInDocument)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("gateway.server.url %q is not an https URL, or an http URL to this machine", raw)
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return fmt.Errorf("gateway.server.url %q is http to a host that is not this machine; a server elsewhere is reached over https", raw)
	}
	if u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil || strings.HasSuffix(raw, "#") {
		return fmt.Errorf("gateway.server.url %q is more than a scheme and a host; the server defines its own paths", raw)
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
// file's order. What an entry may hold together is Forager's to say, and qory run
// asks it before a run.
func readCredentials(path string, node *yaml.Node) ([]ForagerCredential, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: gateway.credentials is a mapping from a name to a credential", path)
	}
	var out []ForagerCredential
	for i := 0; i+1 < len(node.Content); i += 2 {
		c := ForagerCredential{Name: node.Content[i].Value}
		var f credentialFile
		// A node decodes loosely, so a key that is not one is refused here as the rest
		// of the file refuses it.
		if entry := node.Content[i+1]; entry.Kind == yaml.MappingNode {
			for k := 0; k+1 < len(entry.Content); k += 2 {
				switch key := entry.Content[k].Value; key {
				case "env", "file", "adapter", "argument", "hosts", "paths", "auth", "placeholders":
				default:
					return nil, fmt.Errorf("%s: gateway.credentials.%s: key %q is not one", path, c.Name, key)
				}
			}
		}
		if err := node.Content[i+1].Decode(&f); err != nil {
			return nil, fmt.Errorf("%s: gateway.credentials.%s: %w", path, c.Name, err)
		}
		if f.Env != nil {
			c.Env = *f.Env
		}
		if f.File != nil {
			if !filepath.IsAbs(*f.File) {
				return nil, fmt.Errorf("%s: gateway.credentials.%s.file %q is not an absolute path", path, c.Name, *f.File)
			}
			c.File = *f.File
		}
		if f.Adapter != nil {
			if len(*f.Adapter) == 0 || !filepath.IsAbs((*f.Adapter)[0]) {
				return nil, fmt.Errorf("%s: gateway.credentials.%s.adapter is a program by its absolute path, and its arguments", path, c.Name)
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

// sessionGateway is r's session.gateway, nil for none or for no file.
func (r *Forager) sessionGateway() *ForagerSessionGateway {
	if r == nil {
		return nil
	}
	return r.SessionGateway
}

// Rows lists forager.yaml's effective values with the file as origin, and the
// defaults with [Default] when there is no file or a section is absent.
func (r *Forager) Rows() []Row {
	origin := Default
	if r != nil {
		origin = r.File
	}
	rows := []Row{{"gateway.egress.mode", "observe", Default}, {"gateway.egress.allow", "(none)", Default}, {"gateway.egress.deny", "(none)", Default}}
	if r != nil && r.Egress != nil {
		rows[0] = Row{"gateway.egress.mode", r.Egress.Mode, origin}
		rows[1] = Row{"gateway.egress.allow", listOrNone(r.Egress.Allow), origin}
		rows[2] = Row{"gateway.egress.deny", listOrNone(r.Egress.Deny), origin}
	}
	if r != nil && r.Server != nil {
		rows = append(rows, Row{"gateway.server.url", r.Server.URL, origin})
		if r.Server.AccessKeyID != "" {
			rows = append(rows, Row{"gateway.server.access_key_id", r.Server.AccessKeyID, r.Server.AccessKeyIDFrom})
		} else {
			rows = append(rows, Row{"gateway.server.access_key_id", "(none: qory access-key enrol writes it)", Default})
		}
		if len(r.Server.Pin) > 0 {
			var fingerprints []string
			for _, k := range r.Server.Pin.Keys() {
				fingerprints = append(fingerprints, k.Fingerprint())
			}
			rows = append(rows, Row{"gateway.server.apiary_public_key", "fingerprint " + strings.Join(fingerprints, ", "), r.Server.PinFrom})
		} else {
			rows = append(rows, Row{"gateway.server.apiary_public_key", "(none: qory access-key enrol writes it)", Default})
		}
	} else {
		rows = append(rows, Row{"gateway.server.url", "(none)", Default})
	}
	if r != nil && r.Listen != "" {
		rows = append(rows, Row{"gateway.listen", r.Listen, origin})
	} else {
		rows = append(rows, Row{"gateway.listen", "(none)", Default})
	}
	// The paths as the file writes them; what the key file holds is never read here.
	if r != nil && r.TLS != nil {
		rows = append(rows, Row{"gateway.tls.certificate", r.TLS.Certificate, origin}, Row{"gateway.tls.key", r.TLS.Key, origin})
	} else {
		rows = append(rows, Row{"gateway.tls.certificate", "(none)", Default}, Row{"gateway.tls.key", "(none)", Default})
	}
	if r != nil && len(r.runCredentialRows) > 0 {
		rows = append(rows, r.runCredentialRows...)
	} else {
		rows = append(rows, Row{"gateway.run_credentials", "(none)", Default})
	}
	if g := r.sessionGateway(); g != nil {
		rows = append(rows, Row{"session.gateway.url", g.URL, origin})
		if g.CAFile != "" {
			rows = append(rows, Row{"session.gateway.ca_file", g.CAFile, origin})
		} else {
			rows = append(rows, Row{"session.gateway.ca_file", "(none: the system's roots)", Default})
		}
		for _, v := range [][2]string{{"session.gateway.certificate_sha256", g.CertificateSHA256}, {"session.gateway.run_credential_file", g.RunCredentialFile}} {
			if v[1] != "" {
				rows = append(rows, Row{v[0], v[1], origin})
			} else {
				rows = append(rows, Row{v[0], "(none)", Default})
			}
		}
	} else {
		rows = append(rows, Row{"session.gateway.url", "(none: qory run starts a gateway for each run)", Default})
	}
	if r != nil && r.InstanceName != "" {
		rows = append(rows, Row{"session.instance.name", r.InstanceName, origin})
	} else {
		rows = append(rows, Row{"session.instance.name", DefaultInstanceName(), Default})
	}
	if r != nil && r.Timeout > 0 {
		rows = append(rows, Row{"session.run.timeout", r.Timeout.String(), origin})
	} else {
		rows = append(rows, Row{"session.run.timeout", "(none)", Default})
	}
	if r != nil && r.StopSignal != "" {
		rows = append(rows, Row{"session.run.stop_signal", r.StopSignal, origin})
	} else {
		rows = append(rows, Row{"session.run.stop_signal", "(the runtime's, else " + session.DefaultStopSignal + ")", Default})
	}
	if r != nil && r.StopGrace > 0 {
		rows = append(rows, Row{"session.run.stop_grace", r.StopGrace.String(), origin})
	} else {
		rows = append(rows, Row{"session.run.stop_grace", "(the runtime's, else 10s)", Default})
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
			rows = append(rows, Row{"gateway.credentials." + c.Name, from, origin})
		}
		shadowed := r.Shadowed()
		for _, in := range r.Integrations {
			program := in.Program
			if in.Path != "" {
				program = in.Path + " " + in.Version
			}
			if slices.Contains(shadowed, in.Key) {
				program += ", shadowed by gateway.credentials." + in.Key
			}
			rows = append(rows, Row{"gateway.integrations." + in.Key, program, origin})
		}
	}
	if r != nil && r.Wall != nil {
		rows = append(rows,
			Row{"wall.adapter", r.Wall.Adapter, origin},
			Row{"wall.image", defaultRow(r.Wall), origin},
		)
		for _, i := range r.Wall.Images {
			rows = append(rows, Row{"wall.images." + i.Name, imageRow(i), origin})
		}
		rows = append(rows, Row{"wall.env", listOrNone(r.Wall.Env), origin})
		if r.Wall.Command != "" {
			rows = append(rows, Row{"wall.command", r.Wall.Command, origin})
		}
		if r.Wall.Helper != "" {
			rows = append(rows, Row{"wall.helper", r.Wall.Helper, origin})
		}
		if r.Wall.User != "" {
			rows = append(rows, Row{"wall.user", r.Wall.User, origin})
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
			rows = append(rows, Row{"wall.mounts", strings.Join(mounts, ", "), origin})
		}
		for _, l := range [][2]string{{"cpus", r.Wall.CPUs}, {"memory", r.Wall.Memory}, {"shm_size", r.Wall.ShmSize}} {
			if l[1] != "" {
				rows = append(rows, Row{"wall." + l[0], l[1], origin})
			}
		}
		if len(r.Wall.CAEnv) > 0 {
			rows = append(rows, Row{"wall.ca_env", strings.Join(r.Wall.CAEnv, ", "), origin})
		}
		if r.Wall.PIDs > 0 {
			rows = append(rows, Row{"wall.pids_limit", strconv.Itoa(r.Wall.PIDs), origin})
		}
	} else {
		rows = append(rows, Row{"wall.adapter", "(none)", Default})
	}
	return rows
}
