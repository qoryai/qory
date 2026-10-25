package config

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/policy"
	"github.com/qoryai/forager/session"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// ForagerFileName is the machine's forager.yaml, under [UserDir] alone: what qory run
// does on this machine. It has no counterpart in a repository, so a checkout cannot set
// the policy the agent runs under or where the run's events go.
const ForagerFileName = "forager.yaml"

// Heartbeat is the heartbeat interval of every gateway qory starts, qory run's own and
// qory gateway, Forager's default.
const Heartbeat = 30 * time.Second

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
	// Tools are the tools this machine defines, in the file's order: programs Forager
	// starts for a run, outside the container, that receive the requests to the hosts
	// they serve. A run's policy selects among them by name.
	Tools []ForagerTool
	// Integrations are the integrations this machine declares, in the file's order:
	// programs that speak the integration contract, which [Forager.Expand] describes and
	// expands into the definitions they return.
	Integrations []ForagerIntegration
	// Gateway reports whether the file has a gateway section: qory gateway runs none
	// without one.
	Gateway bool
	// GatewayKeys are the keys the gateway section writes beside server, sorted: what
	// a gateway of this machine's own uses, and a run that goes through another gateway
	// does not.
	GatewayKeys []string
	// Listen is gateway.listen, the address qory gateway listens on for the runs of
	// other machines, host:port; empty when the file sets none. qory run does not use it.
	Listen string
	// TLS is gateway.tls, the certificate and key qory gateway serves Listen with; nil
	// when the file sets none.
	TLS *ForagerTLS
	// LockMemory is gateway.lock_memory: on Linux, the gateway locks every page of its
	// process in memory, so that no key it holds is written to swap. False when the file
	// does not set it.
	LockMemory bool
	// RunLimits is gateway.run_limits: the limits of each run on the gateway.
	RunLimits ForagerRunLimits
	// StackHosts is gateway.stack_hosts, in the file's order: the hosts the stack proxy
	// of each run on the gateway reaches, as Forager's gateway.Config.StackHosts. Each is
	// an entry Forager reads in a policy's egress.allow. Nil when the file sets none.
	StackHosts []string
	// Machine is machine, the adapter that made this machine, such as aws-ec2, or none;
	// empty when the file sets none. qory run passes it to Forager, which reports it in
	// dev.qory.run.started.
	Machine string
	// Pool is the pool section, nil when the file has none: what qory run pool runs by.
	// No other command reads it.
	Pool     *ForagerPool
	expanded bool
}

// ForagerRunLimits is gateway.run_limits, as Forager's gateway.RunLimits: the most
// connections a run has open at once, the requests it sends a second, and the requests
// it sends at once after a pause. A field the file does not set is zero, which is
// Forager's default: 256, 50 and 200.
type ForagerRunLimits struct {
	Connections       int
	RequestsPerSecond int
	Burst             int
}

// runLimits are the fields of gateway.run_limits: the key, the most the field takes,
// and Forager's default. The least is 1.
var runLimits = []struct {
	key          string
	most, normal int
}{
	{"connections", 65536, 256},
	{"requests_per_second", 10000, 50},
	{"burst", 100000, 200},
}

// ForagerTLS is gateway.tls: the files of the certificate and its key, as the file
// writes them; [Forager.Path] resolves one. qory never prints the key file's contents.
type ForagerTLS struct {
	Certificate string
	Key         string
}

// ForagerVia is gateway.server.via: the gateway this machine reaches the server through,
// by its URL, the PEM file of the authorities its certificate chains to, and the pin of
// its certificate's public key, each as the file writes it; [Forager.Path] resolves the
// file. A setting the file does not write is empty.
type ForagerVia struct {
	URL               string
	CAFile            string
	CertificateSHA256 string
}

// certificatePin is gateway.server.via.certificate_sha256's shape: 32 bytes in standard
// base64 with padding, as Forager takes it.
var certificatePin = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

// CertificatePin reports whether v is a certificate pin as Forager takes it: the SHA-256
// of a public key, 32 bytes, in standard base64 with padding.
func CertificatePin(v string) bool {
	if !certificatePin.MatchString(v) {
		return false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(v)
	return err == nil && len(b) == sha256.Size
}

// scanURL is what a URL holds that no refusal may print: a URL as written, or its host
// as Go decodes it, an IPv6 zone included, either form with its percent-encoding undone
// again and again until nothing changes, since a refusal names the host. secret is
// [accesskey.SecretInDocument]'s error of the first form that holds an access key secret
// or a run key within [urlUnescapeLimit] rounds, nil for none; tooDeep says a form is
// still encoded after them, so whatever it holds is not known.
func scanURL(raw string) (tooDeep bool, secret error) {
	forms := []string{raw}
	if u, err := url.Parse(raw); err == nil {
		forms = append(forms, u.Host)
	}
	for _, v := range forms {
		for n := 0; ; n++ {
			if err := accesskey.SecretInDocument(v); err != nil {
				return false, err
			}
			dec := unescapeEvery(v)
			if dec == v {
				break
			}
			if n == urlUnescapeLimit {
				tooDeep = true
				break
			}
			v = dec
		}
	}
	return tooDeep, nil
}

// urlUnescapeLimit is how many rounds [scanURL] undoes a URL's encoding.
const urlUnescapeLimit = 8

// unescapeEvery is v with each %XX of it undone, a % that starts none left as it is:
// unlike [url.PathUnescape], it undoes a round that holds a stray %, as Go's decoded
// IPv6 zone does.
func unescapeEvery(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '%' && i+2 < len(v) {
			if hi, lo := unhex(v[i+1]), unhex(v[i+2]); hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// unhex is the value of the hexadecimal digit c, or -1.
func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// urlOrigin is u's scheme, host and port: never its user information, path, query or
// fragment.
func urlOrigin(u *url.URL) string { return u.Scheme + "://" + u.Host }

// gatewayURLWrong says what is wrong with v as a gateway's URL, as Forager's via takes
// it: https, a host and an optional port, and no user information, path other than
// "/", query or fragment; empty when nothing is. What it says never holds more of
// v than its scheme, its host and its port, so user information or a query that holds a
// credential is never printed.
func gatewayURLWrong(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return "is not a URL"
	}
	if u.Hostname() == "" {
		return "has no host"
	}
	origin := urlOrigin(u)
	switch {
	case u.Scheme != "https":
		return "for " + origin + " is not https"
	case u.User != nil:
		return "for " + origin + " holds user information"
	case u.Path != "" && u.Path != "/":
		return "for " + origin + " has a path"
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "for " + origin + " has a query or a fragment"
	}
	return ""
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
// used, and for the other two Hosts, Scheme, Username, Header and Paths do. A model key
// has Format, Endpoint and the scheme in place of Hosts, Paths and Placeholders.
type ForagerCredential struct {
	Name                     string
	Env, File                string
	Adapter                  []string
	Argument                 string
	Hosts                    []string
	Scheme, Username, Header string
	Paths                    []string
	Placeholders             []string
	// Format is a model key's API format, such as anthropic-messages; Endpoint the base
	// URL of its API, whose host alone gets the key; OAuth marks a bearer as an OAuth
	// token; and KeyPrefixes are the prefixes the key must start with, nil for any.
	// Each is empty for any other credential.
	Format, Endpoint string
	OAuth            bool
	KeyPrefixes      []string
	// Integration is the key of the integration the definition was expanded from,
	// empty for one of the file's credentials section.
	Integration string
}

// ForagerTool is one entry of the tools section, as Forager's gateway.Tool: Command is
// the program, by its absolute path, and its arguments; Argument the pattern a policy's
// argument must match whole; Serves the hosts whose requests go to the tool; and
// Placeholders the variables the container gets with a placeholder value.
type ForagerTool struct {
	Name         string
	Command      []string
	Argument     string
	Serves       []string
	Placeholders []string
}

// WallDocker is the one wall adapter there is.
const WallDocker = "docker"

// Where a walled run's setup commands run, wall.setup: in a separate container of the
// run's image on the wall's network, the default, or in the agent's container before
// the agent starts.
const (
	SetupOutside = "outside"
	SetupInside  = "inside"
)

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
	// ImagesAllow are the prefixes of the references pinned by digest a run's policy may
	// name its image by, Forager's OpenSpec.ImagesAllow; nil allows no reference. A list
	// needs a default image, Image or --image.
	ImagesAllow []string
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
	// CAEnv lists the variables that point a program at the bundle of authorities when
	// the gateway makes an authority for the run: in the container, or on this machine
	// for a run without a wall. Nil means Forager's defaults.
	CAEnv []string
	// Setup is where the setup commands of a run run, [SetupOutside] or [SetupInside];
	// empty means outside.
	Setup string
	// EngineProxyFile is the absolute path of the file that has the address and the
	// password of the container engine's proxy setting; empty when the file sets none.
	// qory run reads it as a walled run starts. qory never prints the file's contents.
	EngineProxyFile string
}

// ForagerMount is one file or directory of this machine a walled run sees, at the same
// path.
type ForagerMount struct {
	Path     string
	ReadOnly bool
}

// ParseMount reads a mount as wall.mounts and --mount write it: an absolute path, and
// :ro after it for one the container cannot change. :rw sets the default explicitly. The
// refusal prints the mount as [exports.Value] prints it, quoted.
func ParseMount(v string) (ForagerMount, error) {
	m := ForagerMount{Path: v}
	if p, ok := strings.CutSuffix(v, ":ro"); ok {
		m = ForagerMount{Path: p, ReadOnly: true}
	} else if p, ok := strings.CutSuffix(v, ":rw"); ok {
		m.Path = p
	}
	if !filepath.IsAbs(m.Path) {
		return m, fmt.Errorf("the mount %s is not an absolute path, with :ro after it for a read-only one", exports.Value(v, true))
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
	// Paths are the paths a run may request on a host, by host, as a policy's
	// egress.paths, nil when the file sets none. They apply to every run on this
	// machine, beside the path rules of the run's policy, and any make every run need a
	// wall.
	Paths map[string][]string
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
	// Via is gateway.server.via, the gateway this machine reaches the server through;
	// nil when the file sets none, and the server is reached directly.
	Via *ForagerVia
	// EnrolmentCodeFile is gateway.server.enrolment_code_file as written, "" when the
	// file sets none: the file qory gateway reads its enrolment code from when it has no
	// access key. [Forager.Path] resolves it.
	EnrolmentCodeFile string
}

// machineShape is the form of machine, as Forager checks OpenSpec.Machine.
var machineShape = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// EnvOrigin starts the origin of a value read from a variable: "$" and its name.
const EnvOrigin = "$"

// pinEntry is one entry of gateway.server.apiary_public_key as written.
type pinEntry struct {
	Alg       *string `yaml:"alg"`
	PublicKey *string `yaml:"public_key"`
}

// foragerFile is forager.yaml as written: the gateway's sections, the session's, the
// wall's and the pool's.
type foragerFile struct {
	APIVersion string  `yaml:"apiVersion"`
	Machine    *string `yaml:"machine"`
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
		// Env, Mounts and CAEnv are read as nodes, so an entry that is not a string
		// is refused by its index.
		Env     yaml.Node `yaml:"env,omitempty"`
		User    *string   `yaml:"user"`
		Mounts  yaml.Node `yaml:"mounts,omitempty"`
		CPUs    *string   `yaml:"cpus"`
		Memory  *string   `yaml:"memory"`
		PIDs    *int      `yaml:"pids_limit"`
		ShmSize *string   `yaml:"shm_size"`
		CAEnv   yaml.Node `yaml:"ca_env,omitempty"`
		Setup   *string   `yaml:"setup"`
		// EngineProxyFile is read as a path alone; qory run reads the file.
		EngineProxyFile *string `yaml:"engine_proxy_file"`

		// ImagesAllow is read as a node, so an entry that is not a string is refused
		// by its index.
		ImagesAllow yaml.Node `yaml:"images_allow,omitempty"`
	} `yaml:"wall,omitempty"`
	Pool *poolSection `yaml:"pool,omitempty"`
}

// gatewaySection is the gateway section as written: the egress policy, the server, the
// credentials, the tools and the integrations.
type gatewaySection struct {
	Egress *egressSection `yaml:"egress,omitempty"`
	Server *struct {
		URL             *string     `yaml:"url"`
		AccessKeyID     *string     `yaml:"access_key_id"`
		ApiaryPublicKey *[]pinEntry `yaml:"apiary_public_key"`
		// Secret and AccessKey are read only to say where what they would hold belongs.
		Secret    yaml.Node   `yaml:"secret,omitempty"`
		AccessKey yaml.Node   `yaml:"access_key,omitempty"`
		Via       *viaSection `yaml:"via,omitempty"`
		// EnrolmentCodeFile is read by qory gateway alone.
		EnrolmentCodeFile *string `yaml:"enrolment_code_file"`
	} `yaml:"server,omitempty"`
	Credentials  yaml.Node `yaml:"credentials,omitempty"`
	Tools        yaml.Node `yaml:"tools,omitempty"`
	Integrations yaml.Node `yaml:"integrations,omitempty"`
	Listen       *string   `yaml:"listen"`
	TLS          *struct {
		Certificate *string `yaml:"certificate"`
		Key         *string `yaml:"key"`
	} `yaml:"tls,omitempty"`
	// Selects is read only to stop a file that still sets it.
	Selects yaml.Node `yaml:"selects,omitempty"`
	// LockMemory and RunLimits are given to Forager's gateway as they are.
	LockMemory *bool `yaml:"lock_memory"`
	RunLimits  *struct {
		Connections       *int `yaml:"connections"`
		RequestsPerSecond *int `yaml:"requests_per_second"`
		Burst             *int `yaml:"burst"`
	} `yaml:"run_limits,omitempty"`
	// StackHosts is read as a node, so an entry that is not a string stops the load
	// with its index.
	StackHosts yaml.Node `yaml:"stack_hosts,omitempty"`
}

// egressSection is gateway.egress as written.
type egressSection struct {
	Mode *string `yaml:"mode"`
	// Allow and Deny are read as nodes, so an entry that is not a string is refused by
	// its index.
	Allow yaml.Node `yaml:"allow,omitempty"`
	Deny  yaml.Node `yaml:"deny,omitempty"`
	// Paths is read as a node, so its shape is refused in qory's words.
	Paths yaml.Node `yaml:"paths,omitempty"`
}

// viaSection is gateway.server.via as written. Its keys are nodes, merges and alias keys
// resolved by the decoder, so a key written with a null value is told from an absent
// one.
type viaSection struct {
	URL               yaml.Node `yaml:"url"`
	CAFile            yaml.Node `yaml:"ca_file"`
	CertificateSHA256 yaml.Node `yaml:"certificate_sha256"`
}

// sessionSection is the session section as written: the instance and the run.
type sessionSection struct {
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
	dir, err := UserDir()
	if err != nil {
		return nil, err
	}
	return LoadForagerIn(dir)
}

// LoadForagerIn reads forager.yaml in dir, a configuration directory as [UserDir] is, as
// [LoadForager] reads the machine's: such as the one qory run pool gives the runs it
// starts. No file is nil.
func LoadForagerIn(dir string) (*Forager, error) {
	path, data, err := readForagerFile(dir)
	if data == nil || err != nil {
		return nil, err
	}
	if err := oneDocument(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var doc yaml.Node
	docErr := yaml.Unmarshal(data, &doc)
	if docErr == nil {
		if f := mergeKeyFault(&doc, reflect.TypeFor[foragerFile](), "", true); f != nil {
			return nil, f.error(path)
		}
	}
	var f foragerFile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := safeDecode(func() error { return dec.Decode(&f) }); err != nil && !errors.Is(err, io.EOF) {
		if docErr != nil {
			return nil, foragerSyntaxError(path, err)
		}
		return nil, foragerDecodeError(path, &doc, reflect.TypeFor[foragerFile](), "", true, err)
	}
	if f.APIVersion == "" {
		f.APIVersion = stack.APIVersion
	}
	if _, err := exports.ResolveAPIVersion(f.APIVersion); err != nil {
		return nil, fmt.Errorf("%s: apiVersion is not one this qory reads; versions: %s", path, exports.APIVersion)
	}
	r := &Forager{File: path, Gateway: f.Gateway != nil, GatewayKeys: gatewayKeys(data)}
	if f.Machine != nil {
		if !machineShape.MatchString(*f.Machine) {
			return nil, fmt.Errorf("%s: set machine to 1 to 64 of a-z, 0-9 and -, the first a letter, such as aws-ec2 or none, or remove it: it is %s", path, exports.Value(*f.Machine, true))
		}
		r.Machine = *f.Machine
	}
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
			return nil, fmt.Errorf("%s: gateway.egress.mode is not observe or enforce", path)
		}
		r.Egress = &ForagerEgress{Mode: *e.Mode}
		if !egressAtOnce(e, r.Egress) {
			if err := egressOneByOne(path, e, r.Egress); err != nil {
				return nil, err
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
		if err := viaNull(path, data); err != nil {
			return nil, err
		}
		if w.Via != nil {
			if r.Server.Via, err = readVia(path, w.Via); err != nil {
				return nil, err
			}
		}
		if f := w.EnrolmentCodeFile; f != nil {
			if *f == "" {
				return nil, fmt.Errorf("%s: set gateway.server.enrolment_code_file to the path of the gateway's enrolment code, or remove it: the value is empty", path)
			}
			r.Server.EnrolmentCodeFile = *f
		}
	}
	if in := ses.Instance; in != nil && in.Name != nil {
		if accesskey.CheckName(*in.Name) != nil {
			return nil, fmt.Errorf("%s: session.instance.name %s", path, notAName)
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
				return nil, fmt.Errorf("%s: %s is not a duration above zero, such as 5h30m or 30s", path, d.key)
			}
			*d.out = v
		}
		if run.StopSignal != nil {
			if *run.StopSignal == "" {
				return nil, fmt.Errorf("%s: session.run.stop_signal is empty", path)
			}
			if session.CheckStopSignal(*run.StopSignal) != nil {
				return nil, fmt.Errorf("%s: session.run.stop_signal is not one of SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1, SIGUSR2", path)
			}
			r.StopSignal = *run.StopSignal
		}
	}
	if g.Credentials.Kind != 0 {
		if r.Credentials, err = readCredentials(path, &g.Credentials); err != nil {
			return nil, err
		}
	}
	if g.Tools.Kind != 0 {
		if r.Tools, err = readTools(path, &g.Tools); err != nil {
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
			return nil, fmt.Errorf("%s: gateway.listen is not host:port, such as 0.0.0.0:8443", path)
		}
		r.Listen = *g.Listen
	}
	if t := g.TLS; t != nil {
		if t.Certificate == nil || *t.Certificate == "" || t.Key == nil || *t.Key == "" {
			return nil, fmt.Errorf("%s: gateway.tls needs both certificate and key", path)
		}
		r.TLS = &ForagerTLS{Certificate: *t.Certificate, Key: *t.Key}
	}
	if g.LockMemory != nil {
		r.LockMemory = *g.LockMemory
	}
	if l := g.RunLimits; l != nil {
		for i, f := range []struct {
			in  *int
			out *int
		}{{l.Connections, &r.RunLimits.Connections}, {l.RequestsPerSecond, &r.RunLimits.RequestsPerSecond}, {l.Burst, &r.RunLimits.Burst}} {
			if f.in == nil {
				continue
			}
			if lim := runLimits[i]; *f.in < 1 || *f.in > lim.most {
				return nil, fmt.Errorf("%s: set gateway.run_limits.%s to a number from 1 to %d, or remove it for the default of %d", path, lim.key, lim.most, lim.normal)
			}
			*f.out = *f.in
		}
	}
	if g.StackHosts.Kind != 0 {
		if r.StackHosts, err = readStackHosts(path, &g.StackHosts); err != nil {
			return nil, err
		}
	}
	if g.Selects.Kind != 0 {
		return nil, fmt.Errorf("remove gateway.selects from %s: a run's policy selects its credentials", path)
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
		if r.Wall.ImagesAllow, err = readImagesAllow(path, &w.ImagesAllow); err != nil {
			return nil, err
		}
		if w.Command != nil {
			r.Wall.Command = *w.Command
		}
		if w.Helper != nil {
			if !filepath.IsAbs(*w.Helper) {
				return nil, fmt.Errorf("%s: wall.helper is not an absolute path", path)
			}
			r.Wall.Helper = *w.Helper
		}
		if w.User != nil {
			r.Wall.User = *w.User
		}
		notMount := func(i, _ int) error {
			return fmt.Errorf("%s: wall.mounts[%d] is not an absolute path, with :ro after it for a read-only one", path, i)
		}
		var mounts, caEnv, env []string
		if mounts, err = stringList(path, "wall.mounts", &w.Mounts, notMount); err != nil {
			return nil, err
		}
		for i, v := range mounts {
			m, err := ParseMount(v)
			if err != nil {
				return nil, notMount(i, 0)
			}
			r.Wall.Mounts = append(r.Wall.Mounts, m)
		}
		if w.CPUs != nil {
			r.Wall.CPUs = *w.CPUs
		}
		if w.Memory != nil {
			r.Wall.Memory = *w.Memory
		}
		if w.PIDs != nil {
			if *w.PIDs < 1 {
				return nil, fmt.Errorf("%s: wall.pids_limit is below 1; a limit is at least 1", path)
			}
			r.Wall.PIDs = *w.PIDs
		}
		if w.ShmSize != nil {
			r.Wall.ShmSize = *w.ShmSize
		}
		if w.Setup != nil {
			if *w.Setup != SetupOutside && *w.Setup != SetupInside {
				return nil, fmt.Errorf("%s: set wall.setup to %s or %s, or remove it for %s: it is %s", path, SetupOutside, SetupInside, SetupOutside, exports.Value(*w.Setup, true))
			}
			r.Wall.Setup = *w.Setup
		}
		if p := w.EngineProxyFile; p != nil {
			if !filepath.IsAbs(*p) {
				return nil, fmt.Errorf("%s: set wall.engine_proxy_file to an absolute path, or remove it: it is %s", path, exports.Value(*p, true))
			}
			r.Wall.EngineProxyFile = *p
		}
		notCAEnv := func(i, _ int) error {
			return fmt.Errorf("%s: wall.ca_env[%d] is not a variable's name", path, i)
		}
		if caEnv, err = stringList(path, "wall.ca_env", &w.CAEnv, notCAEnv); err != nil {
			return nil, err
		}
		for i, name := range caEnv {
			if !envName.MatchString(name) {
				return nil, notCAEnv(i, 0)
			}
			r.Wall.CAEnv = append(r.Wall.CAEnv, name)
		}
		notEnv := func(i, _ int) error {
			return fmt.Errorf("%s: wall.env[%d] is not a variable's name; the value comes from the environment, never from this file", path, i)
		}
		if env, err = stringList(path, "wall.env", &w.Env, notEnv); err != nil {
			return nil, err
		}
		for i, name := range env {
			if ForagersOwn(name) {
				return nil, fmt.Errorf("%s: wall.env[%d] names a variable that is Forager's and never the agent's", path, i)
			}
			if !envName.MatchString(name) {
				return nil, notEnv(i, 0)
			}
			r.Wall.Env = append(r.Wall.Env, name)
		}
	}
	if f.Pool != nil {
		if r.Pool, err = readPool(path, f.Pool); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// egressAtOnce reads the hosts and paths of e into out as one policy document, through
// Forager's policy.Read, and reports whether it read them. Forager holds each entry of a
// policy's allow, deny and paths alone, so a document of them all that it reads is one
// whose every entry [egressOneByOne] reads; one it refuses, or one that is not lists of
// strings and a mapping of them, is left to [egressOneByOne], which finds the entry and
// says why. out is left as it is when they are not read.
func egressAtOnce(e *egressSection, out *ForagerEgress) bool {
	allow, okAllow := stringsAtOnce(&e.Allow)
	deny, okDeny := stringsAtOnce(&e.Deny)
	if !okAllow || !okDeny {
		return false
	}
	var paths map[string][]string
	if e.Paths.Kind != 0 {
		var ok bool
		if paths, ok = pathsAtOnce(&e.Paths); !ok {
			return false
		}
	}
	egress := map[string]any{"mode": "observe"}
	for list, v := range map[string][]string{"allow": allow, "deny": deny} {
		if v != nil {
			egress[list] = v
		}
	}
	if paths != nil {
		egress["paths"] = paths
	}
	b, err := json.Marshal(map[string]any{"version": 1, "egress": egress})
	if err != nil {
		return false
	}
	if _, err := policy.Read(ForagerFileName+" gateway.egress", b); err != nil {
		return false
	}
	// As [egressOneByOne] keeps them: an empty list as none, and the paths as read.
	out.Allow = append([]string(nil), allow...)
	out.Deny = append([]string(nil), deny...)
	out.Paths = paths
	return true
}

// stringsAtOnce is [stringList] of node without a refusal: the list, nil for one the
// file does not write or writes null, and false where stringList refuses it.
func stringsAtOnce(node *yaml.Node) ([]string, bool) {
	list, err := stringList("", "", node, func(int, int) error { return errNotRead })
	return list, err == nil
}

// errNotRead is the refusal of a reading that says nothing: the reading after it does.
var errNotRead = errors.New("not read")

// pathsAtOnce is gateway.egress.paths as [readPaths] reads it, without Forager's
// reading of its hosts and paths and without a refusal: false where readPaths refuses
// its shape, a key that is not a string or written twice, a value that is not a list of
// strings, or a path listed twice.
func pathsAtOnce(node *yaml.Node) (map[string][]string, bool) {
	node = unalias(node)
	if node.Kind != yaml.MappingNode {
		return nil, false
	}
	out := map[string][]string{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := unalias(node.Content[i])
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return nil, false
		}
		if _, twice := out[key.Value]; twice {
			return nil, false
		}
		v := unalias(node.Content[i+1])
		if v.Kind != yaml.SequenceNode {
			return nil, false
		}
		list, err := stringEntries(v, func(int, int) error { return errNotRead })
		if err != nil {
			return nil, false
		}
		for j, p := range list {
			if slices.Index(list, p) != j {
				return nil, false
			}
		}
		out[key.Value] = list
	}
	return out, true
}

// egressOneByOne reads the hosts and paths of e into out entry by entry, each host read
// as an entry of a policy's list of the same name, as Forager reads the lists of a run's
// policy, so the two never disagree. A refusal names the first entry Forager does not
// read, by its index, and holds no value.
func egressOneByOne(path string, e *egressSection, out *ForagerEgress) error {
	for _, l := range []struct {
		list string
		in   *yaml.Node
		out  *[]string
	}{{"allow", &e.Allow, &out.Allow}, {"deny", &e.Deny, &out.Deny}} {
		key := "gateway.egress." + l.list
		notHost := func(i, _ int) error {
			return fmt.Errorf("%s: %s[%d] %s", path, key, i, notAPolicyHost)
		}
		hosts, err := stringList(path, key, l.in, notHost)
		if err != nil {
			return err
		}
		for i, host := range hosts {
			if !policyHost(l.list, host) {
				return notHost(i, 0)
			}
			*l.out = append(*l.out, host)
		}
	}
	if e.Paths.Kind != 0 {
		var err error
		if out.Paths, err = readPaths(path, &e.Paths); err != nil {
			return err
		}
	}
	return nil
}

// viaNull refuses a gateway.server.via the file writes with a null value: decoded, it
// reads as absent, and the machine would reach the server directly. It is refused as its
// empty form is. The decoder resolves merges and alias keys.
func viaNull(path string, data []byte) error {
	var f struct {
		Gateway *struct {
			Server *struct {
				Via yaml.Node `yaml:"via"`
			} `yaml:"server"`
		} `yaml:"gateway"`
	}
	if err := safeDecode(func() error { return yaml.Unmarshal(data, &f) }); err != nil || f.Gateway == nil || f.Gateway.Server == nil {
		return nil
	}
	if v := followAliases(&f.Gateway.Server.Via); v.Kind != 0 && v.ShortTag() == "!!null" {
		return fmt.Errorf("%s: gateway.server.via is empty", path)
	}
	return nil
}

// gatewayKeys are the keys of the gateway section of data, a forager.yaml the decoder
// has read, other than server, sorted; a key written with a null value is one of them.
// Merges and alias keys are resolved as the decoder resolves them.
func gatewayKeys(data []byte) []string {
	var f struct {
		Gateway map[string]yaml.Node `yaml:"gateway"`
	}
	if err := safeDecode(func() error { return yaml.Unmarshal(data, &f) }); err != nil {
		return nil
	}
	var keys []string
	for k := range f.Gateway {
		if k != "server" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// viaShape ends a refusal of gateway.server.via.url: what it takes.
const viaShape = "an https URL of a host and an optional port, with nothing after"

// readVia reads gateway.server.via: url is required, an https URL of a host and an
// optional port alone, and ca_file and certificate_sha256 are optional, each one written
// holding a value. A URL that holds an access key secret or a run key is refused without
// being quoted, and so is one encoded too many times to tell. It reads no file:
// [Forager.CheckVia] reads ca_file when a run starts.
func readVia(path string, v *viaSection) (*ForagerVia, error) {
	var url, caFile, pin *string
	for _, k := range []struct {
		key string
		in  *yaml.Node
		out **string
	}{{"url", &v.URL, &url}, {"ca_file", &v.CAFile, &caFile}, {"certificate_sha256", &v.CertificateSHA256, &pin}} {
		var err error
		if *k.out, err = viaKey(k.in); err != nil {
			return nil, foragerDecodeError(path, k.in, reflect.TypeFor[string](), "gateway.server.via."+k.key, false, err)
		}
	}
	if url == nil || *url == "" {
		return nil, fmt.Errorf("%s: gateway.server.via.url is required", path)
	}
	// A URL encoded too many times to tell what it holds is refused for that, and the
	// refusal holds no part of it.
	switch tooDeep, secret := scanURL(*url); {
	case secret != nil:
		return nil, fmt.Errorf("%s: gateway.server.via.url: %w", path, secret)
	case tooDeep:
		return nil, fmt.Errorf("%s: gateway.server.via.url is percent-encoded more than %d times: %s", path, urlUnescapeLimit, viaShape)
	}
	if wrong := gatewayURLWrong(*url); wrong != "" {
		return nil, fmt.Errorf("%s: gateway.server.via.url %s: %s", path, wrong, viaShape)
	}
	out := &ForagerVia{URL: *url}
	for _, k := range []struct {
		key string
		in  *string
		out *string
	}{{"ca_file", caFile, &out.CAFile}, {"certificate_sha256", pin, &out.CertificateSHA256}} {
		if k.in == nil {
			continue
		}
		// A key that is present holds a value: an empty pin would turn the pin off.
		if *k.in == "" {
			return nil, fmt.Errorf("%s: gateway.server.via.%s is empty", path, k.key)
		}
		*k.out = *k.in
	}
	if p := out.CertificateSHA256; p != "" && !CertificatePin(p) {
		return nil, fmt.Errorf("%s: gateway.server.via.certificate_sha256 is not the SHA-256 of a public key in standard base64 with padding, 44 characters ending in =", path)
	}
	return out, nil
}

// CheckViaURL refuses a gateway's URL a flag, named by flag, gives as gateway.server.via.url,
// as [readVia] refuses one in the file: one that holds an access key secret or a run
// key, one encoded too many times to tell, and one that is not an https URL of a host and
// an optional port alone. No refusal quotes the value.
func CheckViaURL(flag, v string) error {
	switch tooDeep, secret := scanURL(v); {
	case secret != nil:
		return fmt.Errorf("%s: %w", flag, secret)
	case tooDeep:
		return fmt.Errorf("%s is percent-encoded more than %d times: %s", flag, urlUnescapeLimit, viaShape)
	}
	if wrong := gatewayURLWrong(v); wrong != "" {
		return fmt.Errorf("%s %s: %s", flag, wrong, viaShape)
	}
	return nil
}

// CheckVia reads the file gateway.server.via.ca_file names, when it names one: it must
// be there, open, and hold a PEM certificate. It says nothing of what the file holds.
// Without a via there is nothing to check. A refusal prints the file's path as
// [exports.Value] prints it.
func (r *Forager) CheckVia() error {
	if r == nil || r.Server == nil || r.Server.Via == nil || r.Server.Via.CAFile == "" {
		return nil
	}
	path := r.Path(r.Server.Via.CAFile)
	b, err := os.ReadFile(path)
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return fmt.Errorf("%s: gateway.server.via.ca_file %s: %w", r.File, exports.Value(path, false), err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(b) {
		return fmt.Errorf("%s: gateway.server.via.ca_file %s holds no PEM certificate", r.File, exports.Value(path, false))
	}
	return nil
}

// viaKey is a key of gateway.server.via as decoded: nil when the file does not write it,
// its string otherwise, and "" when the file writes it with a null value, so a null key
// is refused as its empty form is: a null certificate_sha256 would otherwise turn the
// pin off without a word.
func viaKey(n *yaml.Node) (*string, error) {
	n = followAliases(n)
	if n.Kind == 0 {
		return nil, nil
	}
	var s string
	if n.ShortTag() == "!!null" {
		return &s, nil
	}
	if err := safeDecode(func() error { return n.Decode(&s) }); err != nil {
		return nil, err
	}
	return &s, nil
}

// followAliases is n with its aliases followed.
func followAliases(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// errMoreThanOneDocument is the refusal of a forager.yaml that holds a second YAML
// document.
var errMoreThanOneDocument = errors.New("holds more than one YAML document; " + ForagerFileName + " is one document, and qory would read only the first")

// oneDocument refuses forager.yaml's content when it holds more than one YAML document,
// an empty one after a trailing --- among them: the decoder reads the first alone, and
// what the others say, a gateway.server.via say, would be ignored without a word. Content
// that does not parse is left to the reader, which says why.
func oneDocument(data []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var first, second yaml.Node
	if dec.Decode(&first) != nil {
		return nil
	}
	if err := dec.Decode(&second); errors.Is(err, io.EOF) {
		return nil
	}
	return errMoreThanOneDocument
}

// readForagerFile reads forager.yaml in dir, the machine's under [UserDir] or another
// configuration directory: its path and its content, which is nil when there is no file.
func readForagerFile(dir string) (string, []byte, error) {
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
	dir, err := UserDir()
	if err != nil {
		return nil, err
	}
	path, data, err := readForagerFile(dir)
	if data == nil || err != nil {
		return nil, err
	}
	if err := oneDocument(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var f struct {
		Session *struct {
			Instance *instanceSection `yaml:"instance,omitempty"`
		} `yaml:"session,omitempty"`
	}
	var doc yaml.Node
	docErr := yaml.Unmarshal(data, &doc)
	if docErr == nil {
		if fault := mergeKeyFault(&doc, reflect.TypeOf(f), "", false); fault != nil {
			return nil, fault.error(path)
		}
	}
	if err := safeDecode(func() error { return yaml.Unmarshal(data, &f) }); err != nil {
		if docErr != nil {
			return nil, foragerSyntaxError(path, err)
		}
		return nil, foragerDecodeError(path, &doc, reflect.TypeOf(f), "", false, err)
	}
	r := &Forager{File: path}
	if f.Session == nil {
		return r, nil
	}
	if in := f.Session.Instance; in != nil && in.Name != nil {
		if accesskey.CheckName(*in.Name) != nil {
			return nil, fmt.Errorf("%s: session.instance.name %s", path, notAName)
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
// access key's secret, and QORY_RUN_KEY, the run key.
func ForagersOwn(name string) bool {
	return slices.Contains(serverVariableNames, name)
}

// notAName is what a refusal of session.instance.name says is wrong with it.
const notAName = "is not 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash, starting with a letter or digit"

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
	if err := CheckServerURL(*rawURL); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s := &ForagerServer{URL: *rawURL}
	envID, envPin := env.AccessKeyID, env.ApiaryPublicKey
	switch {
	case id != nil && envID != "":
		return nil, fmt.Errorf("%s: gateway.server.access_key_id is set, and so is %s; set one of them", path, accesskey.EnvID)
	case id != nil:
		if accesskey.CheckID(*id) != nil {
			return nil, fmt.Errorf("%s: gateway.server.access_key_id is not ak_ and 16 lower-case Crockford base32 characters", path)
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
		seen := map[accesskey.PublicKey]bool{}
		for i, e := range *pin {
			if e.Alg == nil || e.PublicKey == nil {
				return nil, fmt.Errorf("%s: gateway.server.apiary_public_key[%d] needs both alg and public_key", path, i)
			}
			if err := accesskey.SecretInPinEntry(*e.Alg, *e.PublicKey); err != nil {
				return nil, fmt.Errorf("%s: gateway.server.apiary_public_key: %w", path, err)
			}
			// Each entry is checked here as [accesskey.Pin.Check] checks it, so the
			// refusal names the entry and never quotes it.
			at := fmt.Sprintf("gateway.server.apiary_public_key[%d]", i)
			if *e.Alg != "ed25519" {
				return nil, fmt.Errorf("%s: %s.alg is not ed25519, the one there is", path, at)
			}
			pub, err := accesskey.ParsePublicKey(*e.PublicKey)
			if err != nil {
				return nil, fmt.Errorf("%s: %s.public_key is not 32 bytes in base64url without padding", path, at)
			}
			if err := pub.Check(); err != nil {
				return nil, fmt.Errorf("%s: %s.public_key: %w", path, at, err)
			}
			if seen[pub] {
				return nil, fmt.Errorf("%s: %s.public_key is listed twice", path, at)
			}
			seen[pub] = true
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
		return errors.New("it lists the Forager contract's published fixture key, whose secret anyone can read; pin your server's key")
	}
	return nil
}

// serverURLShape ends a refusal of gateway.server.url: what it takes.
const serverURLShape = "an https URL of a host and an optional port, or an http one to this machine, with nothing after"

// CheckServerURL refuses a server URL that is not https, or http to this machine, with
// a scheme and a host alone. An access key secret in it, as written, percent-encoded or
// in its host, is refused first, and then a URL still encoded after [urlUnescapeLimit]
// rounds, whose secret, if any, is not known, with no part of it; any other refusal
// names the part that is wrong and holds no more of the URL than its scheme, its host
// and its port.
func CheckServerURL(raw string) error {
	switch tooDeep, secret := scanURL(raw); {
	case secret != nil:
		return fmt.Errorf("gateway.server.url: %w", secret)
	case tooDeep:
		return fmt.Errorf("gateway.server.url is percent-encoded more than %d times: %s", urlUnescapeLimit, serverURLShape)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("gateway.server.url is not a URL: %s", serverURLShape)
	}
	var wrong string
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		wrong = "is not https or http"
	case u.Scheme == "http" && !loopback(u.Hostname()):
		wrong = "is http to a host that is not this machine"
	case u.User != nil:
		wrong = "holds user information"
	case u.Path != "":
		wrong = "has a path"
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(raw, "#"):
		wrong = "has a query or a fragment"
	default:
		return nil
	}
	return fmt.Errorf("gateway.server.url for %s %s: %s", urlOrigin(u), wrong, serverURLShape)
}

// credentialFile is one entry of the credentials section as written. Its lists are read
// as nodes, so an entry that is not a string is refused by its index.
type credentialFile struct {
	Env      *string   `yaml:"env"`
	File     *string   `yaml:"file"`
	Adapter  yaml.Node `yaml:"adapter,omitempty"`
	Argument *string   `yaml:"argument"`
	Hosts    yaml.Node `yaml:"hosts,omitempty"`
	Paths    yaml.Node `yaml:"paths,omitempty"`
	Auth     *struct {
		Scheme   *string `yaml:"scheme"`
		Username *string `yaml:"username"`
		Header   *string `yaml:"header"`
		OAuth    *bool   `yaml:"oauth"`
	} `yaml:"auth"`
	Placeholders yaml.Node `yaml:"placeholders,omitempty"`
	Format       *string   `yaml:"format"`
	Endpoint     *string   `yaml:"endpoint"`
	KeyPrefixes  yaml.Node `yaml:"key_prefixes,omitempty"`
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
		at := entryAt("gateway.credentials", node.Content[i])
		var f credentialFile
		// A node decodes loosely, so a key that is not one is refused here as the rest
		// of the file refuses it.
		if entry := node.Content[i+1]; entry.Kind == yaml.MappingNode {
			for k := 0; k+1 < len(entry.Content); k += 2 {
				switch key := entry.Content[k].Value; key {
				case "env", "file", "adapter", "argument", "hosts", "paths", "auth", "placeholders", "format", "endpoint", "key_prefixes":
				default:
					return nil, keyNotOne(path, at, entry.Content[k], "")
				}
			}
		}
		if fault := mergeKeyFault(node.Content[i+1], reflect.TypeOf(f), at, false); fault != nil {
			return nil, fault.error(path)
		}
		if err := safeDecode(func() error { return node.Content[i+1].Decode(&f) }); err != nil {
			return nil, foragerDecodeError(path, node.Content[i+1], reflect.TypeOf(f), at, false, err)
		}
		var adapter []string
		for _, l := range []struct {
			key string
			in  *yaml.Node
			out *[]string
		}{{"adapter", &f.Adapter, &adapter}, {"hosts", &f.Hosts, &c.Hosts}, {"paths", &f.Paths, &c.Paths}, {"placeholders", &f.Placeholders, &c.Placeholders}, {"key_prefixes", &f.KeyPrefixes, &c.KeyPrefixes}} {
			key := at + "." + l.key
			var err error
			if *l.out, err = stringList(path, key, l.in, notAString(path, key)); err != nil {
				return nil, err
			}
		}
		if f.Env != nil {
			c.Env = *f.Env
		}
		if f.File != nil {
			if !filepath.IsAbs(*f.File) {
				return nil, fmt.Errorf("%s: %s.file is not an absolute path", path, at)
			}
			c.File = *f.File
		}
		if adapter != nil {
			if len(adapter) == 0 || !filepath.IsAbs(adapter[0]) {
				return nil, fmt.Errorf("%s: %s.adapter is a program by its absolute path, and its arguments", path, at)
			}
			c.Adapter = adapter
		}
		if f.Argument != nil {
			c.Argument = *f.Argument
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
			if f.Auth.OAuth != nil {
				c.OAuth = *f.Auth.OAuth
			}
		}
		if f.Format != nil {
			c.Format = *f.Format
		}
		if f.Endpoint != nil {
			c.Endpoint = *f.Endpoint
		}
		out = append(out, c)
	}
	return out, nil
}

// readPaths reads gateway.egress.paths, a mapping from a host to a list of paths. What
// a host and a path may be is Forager's to say: each host, then each path, is read as
// the egress.paths of a policy document, as Forager reads the path rules it keeps
// beside a run's policy, so the two never disagree. A refusal is qory's, in words that
// hold no value.
func readPaths(path string, node *yaml.Node) (map[string][]string, error) {
	node = unalias(node)
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: gateway.egress.paths is a mapping from a host to a list of paths", path)
	}
	values := map[string]*yaml.Node{}
	// twice is a host written twice, by the lines of its first and its second key.
	twice := map[string][2]int{}
	first := map[string]int{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		// A key is a host as the value it stands for: an alias key is the anchor's
		// value, not its name. A key that is not a string is not a host.
		line, key := node.Content[i].Line, unalias(node.Content[i])
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return nil, fmt.Errorf("%s: gateway.egress.paths: a key is not a lower-case host name or a *. suffix, or it ends in a number and is not an IPv4 address in canonical form; no port, path or scheme", path)
		}
		if at, ok := first[key.Value]; ok {
			if _, noted := twice[key.Value]; !noted {
				twice[key.Value] = [2]int{at, line}
			}
			continue
		}
		first[key.Value] = line
		values[key.Value] = unalias(node.Content[i+1])
	}
	// Each host is read alone before its value, so a key that is not a host is never
	// printed; a host is, as a tool's name is. A path is never printed.
	out := map[string][]string{}
	for _, host := range slices.Sorted(maps.Keys(values)) {
		if !policyPaths(map[string][]string{host: {}}) {
			return nil, fmt.Errorf("%s: gateway.egress.paths: a key is not a lower-case host name or a *. suffix, or it ends in a number and is not an IPv4 address in canonical form; no port, path or scheme", path)
		}
		if lines, ok := twice[host]; ok {
			return nil, fmt.Errorf("%s: line %d: gateway.egress.paths.%s is written twice; it was first written at line %d", path, lines[1], host, lines[0])
		}
		v := values[host]
		if v.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("%s: gateway.egress.paths: %s is a list of paths", path, host)
		}
		notPath := func(i, _ int) error {
			return fmt.Errorf("%s: gateway.egress.paths.%s[%d] is not a path from /, matched whole or up to a final *", path, host, i)
		}
		list, err := stringEntries(v, notPath)
		if err != nil {
			return nil, err
		}
		for i, p := range list {
			if slices.Index(list, p) != i {
				return nil, fmt.Errorf("%s: gateway.egress.paths.%s lists a path twice", path, host)
			}
		}
		for i, p := range list {
			if !policyPaths(map[string][]string{host: {p}}) {
				return nil, notPath(i, 0)
			}
		}
		out[host] = list
	}
	if !policyPaths(out) {
		return nil, fmt.Errorf("%s: gateway.egress.paths is not a policy's egress.paths", path)
	}
	return out, nil
}

// readStackHosts reads gateway.stack_hosts, a list of hosts, each read as an entry of a
// policy's egress.allow, as Forager's gateway.Start reads an entry of StackHosts: a
// lower-case host name, an IPv4 address in canonical form or a *. suffix. An empty list
// is none. An entry that is not a string stops the load with its line and its index; a
// string that Forager does not take, with its index. Neither error contains the value.
func readStackHosts(path string, node *yaml.Node) ([]string, error) {
	fix := func(i int) string {
		return fmt.Sprintf("set gateway.stack_hosts[%d] to a lower-case host name, an IPv4 address or a *. suffix, such as git.example.com; no port, path or scheme", i)
	}
	hosts, err := stringList(path, "gateway.stack_hosts", node, func(i, line int) error {
		return (&fault{line, fix(i)}).error(path)
	})
	if err != nil {
		return nil, err
	}
	for i, host := range hosts {
		if !policyHost("allow", host) {
			return nil, (&fault{0, fix(i)}).error(path)
		}
	}
	if len(hosts) == 0 {
		return nil, nil
	}
	return hosts, nil
}

// notAPolicyHost is what a refusal of a host of gateway.egress.allow or deny says is
// wrong with it.
const notAPolicyHost = "is not a lower-case host name or a *. suffix, or it ends in a number and is not an IPv4 address in canonical form; no port, path or scheme"

// policyHost reports whether Forager reads host as an entry of list, allow or deny, of
// a policy document's egress, as it reads the lists of a run's policy. What it says of
// a host it refuses holds the value, so it is never printed.
func policyHost(list, host string) bool {
	b, err := json.Marshal(map[string]any{"version": 1, "egress": map[string]any{"mode": "observe", list: []string{host}}})
	if err != nil {
		return false
	}
	_, err = policy.Read(ForagerFileName+" gateway.egress", b)
	return err == nil
}

// policyPaths reports whether Forager reads paths as the egress.paths of a policy
// document, as it reads the path rules it keeps beside a run's policy. What it says of
// paths it refuses holds the value, so it is never printed.
func policyPaths(paths map[string][]string) bool {
	type egress struct {
		Mode  string              `json:"mode"`
		Paths map[string][]string `json:"paths"`
	}
	b, err := json.Marshal(struct {
		Version int    `json:"version"`
		Egress  egress `json:"egress"`
	}{1, egress{"observe", paths}})
	if err != nil {
		return false
	}
	_, err = policy.Read(ForagerFileName+" gateway.egress", b)
	return err == nil
}

// pathsOrNone is path rules as a row shows them, by host in order: the host, then its
// paths, each as [exports.Value] prints it, "; " between hosts; a host with an empty list
// is no path of it. No rules is "(none)". A host is printed as it is: Forager's policy
// reads no underscore in one, so it never contains qak_ or qrk_.
func pathsOrNone(paths map[string][]string) string {
	if len(paths) == 0 {
		return "(none)"
	}
	var hosts []string
	for _, host := range slices.Sorted(maps.Keys(paths)) {
		if len(paths[host]) == 0 {
			hosts = append(hosts, host+" (no path)")
			continue
		}
		hosts = append(hosts, host+" "+strings.Join(exports.Values(paths[host]), ", "))
	}
	return strings.Join(hosts, "; ")
}

// stringList is the list of strings at key, written as node, read as the value it stands
// for when it is an alias: nil when the file does not write it or writes it null, and the
// list, empty when the file writes it empty, otherwise. A value that is not a list is
// refused, by its line, and an entry that is not a string as [stringEntries] refuses it.
func stringList(path, key string, node *yaml.Node, notString func(i, line int) error) ([]string, error) {
	line, n := node.Line, unalias(node)
	if n.Kind == 0 {
		return nil, nil
	}
	if f := tagFault(n, line, key); f != nil {
		return nil, f.error(path)
	}
	if null(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, keyFault(line, key, "is not a list of strings").error(path)
	}
	return stringEntries(n, notString)
}

// stringEntries are the entries of the list seq, each read as the value it stands for,
// an alias's included. An entry that is not a string, a null among them, is refused by
// notString, with its index and its line, and the refusal holds no value.
func stringEntries(seq *yaml.Node, notString func(i, line int) error) ([]string, error) {
	list := []string{}
	for i, entry := range seq.Content {
		line := entry.Line
		if entry = unalias(entry); entry.Kind != yaml.ScalarNode || entry.ShortTag() != "!!str" {
			return nil, notString(i, line)
		}
		list = append(list, entry.Value)
	}
	return list, nil
}

// notAString is the refusal of an entry of the list at key that is not a string, by its
// line and its index.
func notAString(path, key string) func(i, line int) error {
	return func(i, line int) error {
		return keyFault(line, fmt.Sprintf("%s[%d]", key, i), "is not a string").error(path)
	}
}

// unalias is the node an alias stands for, and any other node itself.
func unalias(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	return node
}

// toolFile is one entry of the tools section as written. Its lists are read as nodes, so
// an entry that is not a string is refused by its index.
type toolFile struct {
	Command      yaml.Node `yaml:"command,omitempty"`
	Argument     *string   `yaml:"argument"`
	Serves       yaml.Node `yaml:"serves,omitempty"`
	Placeholders yaml.Node `yaml:"placeholders,omitempty"`
}

// readTools reads the tools section, a mapping from name to entry, in the file's order.
// What an entry may hold together is Forager's to say, and qory run asks it before a
// run, as for the credentials.
func readTools(path string, node *yaml.Node) ([]ForagerTool, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: gateway.tools is a mapping from a name to a tool", path)
	}
	var out []ForagerTool
	for i := 0; i+1 < len(node.Content); i += 2 {
		t := ForagerTool{Name: node.Content[i].Value}
		at := entryAt("gateway.tools", node.Content[i])
		var f toolFile
		// A node decodes loosely, so a key that is not one is refused here as the rest
		// of the file refuses it.
		if entry := node.Content[i+1]; entry.Kind == yaml.MappingNode {
			for k := 0; k+1 < len(entry.Content); k += 2 {
				switch key := entry.Content[k].Value; key {
				case "command", "argument", "serves", "placeholders":
				default:
					return nil, keyNotOne(path, at, entry.Content[k], "")
				}
			}
		}
		if fault := mergeKeyFault(node.Content[i+1], reflect.TypeOf(f), at, false); fault != nil {
			return nil, fault.error(path)
		}
		if err := safeDecode(func() error { return node.Content[i+1].Decode(&f) }); err != nil {
			return nil, foragerDecodeError(path, node.Content[i+1], reflect.TypeOf(f), at, false, err)
		}
		for _, l := range []struct {
			key string
			in  *yaml.Node
			out *[]string
		}{{"command", &f.Command, &t.Command}, {"serves", &f.Serves, &t.Serves}, {"placeholders", &f.Placeholders, &t.Placeholders}} {
			key := at + "." + l.key
			var err error
			if *l.out, err = stringList(path, key, l.in, notAString(path, key)); err != nil {
				return nil, err
			}
		}
		// A tool is its program, so a tool without one is refused as one not by its
		// absolute path is.
		if len(t.Command) == 0 || !filepath.IsAbs(t.Command[0]) {
			return nil, fmt.Errorf("%s: %s.command is a program by its absolute path, and its arguments", path, at)
		}
		if f.Argument != nil {
			t.Argument = *f.Argument
		}
		out = append(out, t)
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

// Rows lists forager.yaml's effective values with the file as origin, and the
// defaults with [Default] when there is no file or a section is absent. A name that
// contains an access key secret or a run key is printed as [accesskey.ShownName]
// prints it, and a value the file sets as [exports.Value] prints it. The egress hosts,
// the stack hosts, the server's and the via's URLs, the access key id, the instance
// name and the certificate's SHA-256 are printed as they are: each is refused when it
// could contain qak_ or qrk_.
func (r *Forager) Rows() []Row {
	origin := Default
	if r != nil {
		origin = r.File
	}
	rows := []Row{{"gateway.egress.mode", "observe", Default}, {"gateway.egress.allow", "(none)", Default}, {"gateway.egress.deny", "(none)", Default}, {"gateway.egress.paths", "(none)", Default}}
	if r != nil && r.Egress != nil {
		rows[0] = Row{"gateway.egress.mode", r.Egress.Mode, origin}
		rows[1] = Row{"gateway.egress.allow", listOrNone(r.Egress.Allow), origin}
		rows[2] = Row{"gateway.egress.deny", listOrNone(r.Egress.Deny), origin}
		rows[3] = Row{"gateway.egress.paths", pathsOrNone(r.Egress.Paths), origin}
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
		rows = append(rows, viaRows(r.Server.Via, origin)...)
		if r.Server.EnrolmentCodeFile != "" {
			rows = append(rows, Row{"gateway.server.enrolment_code_file", exports.Value(r.Server.EnrolmentCodeFile, false), origin})
		}
	} else {
		rows = append(rows, Row{"gateway.server.url", "(none)", Default})
	}
	if r != nil && r.Listen != "" {
		rows = append(rows, Row{"gateway.listen", exports.Value(r.Listen, false), origin})
	} else {
		rows = append(rows, Row{"gateway.listen", "(none)", Default})
	}
	// The paths as the file writes them; what the key file holds is never read here.
	if r != nil && r.TLS != nil {
		rows = append(rows, Row{"gateway.tls.certificate", exports.Value(r.TLS.Certificate, false), origin}, Row{"gateway.tls.key", exports.Value(r.TLS.Key, false), origin})
	} else {
		rows = append(rows, Row{"gateway.tls.certificate", "(none)", Default}, Row{"gateway.tls.key", "(none)", Default})
	}
	if r != nil && r.LockMemory {
		rows = append(rows, Row{"gateway.lock_memory", "true", origin})
	} else {
		rows = append(rows, Row{"gateway.lock_memory", "false", Default})
	}
	for i, lim := range runLimits {
		var v int
		if r != nil {
			v = []int{r.RunLimits.Connections, r.RunLimits.RequestsPerSecond, r.RunLimits.Burst}[i]
		}
		if v > 0 {
			rows = append(rows, Row{"gateway.run_limits." + lim.key, strconv.Itoa(v), origin})
		} else {
			rows = append(rows, Row{"gateway.run_limits." + lim.key, strconv.Itoa(lim.normal), Default})
		}
	}
	if r != nil && len(r.StackHosts) > 0 {
		rows = append(rows, Row{"gateway.stack_hosts", listOrNone(r.StackHosts), origin})
	} else {
		rows = append(rows, Row{"gateway.stack_hosts", "(none)", Default})
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
			from := "env " + accesskey.ShownName(c.Env, false)
			switch {
			case c.File != "":
				from = "file " + exports.Value(c.File, false)
			case c.Integration != "":
				from = "integration " + accesskey.ShownName(c.Integration, false)
			case len(c.Adapter) > 0:
				from = "adapter " + exports.Value(c.Adapter[0], false)
			}
			rows = append(rows, Row{"gateway.credentials." + accesskey.ShownName(c.Name, false), from, origin})
		}
		for _, t := range r.Tools {
			rows = append(rows, Row{"gateway.tools." + accesskey.ShownName(t.Name, false), exports.Value(t.Command[0], false) + ", serves " + listOrNone(accesskey.ShownNames(t.Serves)), origin})
		}
		shadowed := r.Shadowed()
		for _, in := range r.Integrations {
			// The program's name and path contain the key, qory-<key>, unless the file
			// sets another.
			program := accesskey.ShownName(in.Program, false)
			if in.Path != "" {
				program = accesskey.ShownName(in.Path, false) + " " + exports.Value(in.Version, false)
			}
			if slices.Contains(shadowed, in.Key) {
				program += ", shadowed by gateway.credentials." + accesskey.ShownName(in.Key, false)
			}
			rows = append(rows, Row{"gateway.integrations." + accesskey.ShownName(in.Key, false), program, origin})
		}
	}
	if r != nil && r.Machine != "" {
		rows = append(rows, Row{"machine", r.Machine, origin})
	}
	if r != nil && r.Wall != nil {
		rows = append(rows,
			Row{"wall.adapter", r.Wall.Adapter, origin},
			Row{"wall.image", defaultRow(r.Wall), origin},
		)
		for _, i := range r.Wall.Images {
			rows = append(rows, Row{"wall.images." + accesskey.ShownName(i.Name, false), imageRow(i), origin})
		}
		if len(r.Wall.ImagesAllow) > 0 {
			rows = append(rows, Row{"wall.images_allow", strings.Join(exports.Values(r.Wall.ImagesAllow), ", "), origin})
		}
		rows = append(rows, Row{"wall.env", listOrNone(shownNames(r.Wall.Env)), origin})
		if r.Wall.Command != "" {
			rows = append(rows, Row{"wall.command", exports.Value(r.Wall.Command, false), origin})
		}
		if r.Wall.Helper != "" {
			rows = append(rows, Row{"wall.helper", exports.Value(r.Wall.Helper, false), origin})
		}
		if r.Wall.User != "" {
			rows = append(rows, Row{"wall.user", exports.Value(r.Wall.User, false), origin})
		}
		if len(r.Wall.Mounts) > 0 {
			var mounts []string
			for _, m := range r.Wall.Mounts {
				if m.ReadOnly {
					mounts = append(mounts, exports.Value(m.Path, false)+":ro")
				} else {
					mounts = append(mounts, exports.Value(m.Path, false))
				}
			}
			rows = append(rows, Row{"wall.mounts", strings.Join(mounts, ", "), origin})
		}
		for _, l := range [][2]string{{"cpus", r.Wall.CPUs}, {"memory", r.Wall.Memory}, {"shm_size", r.Wall.ShmSize}} {
			if l[1] != "" {
				rows = append(rows, Row{"wall." + l[0], exports.Value(l[1], false), origin})
			}
		}
		if len(r.Wall.CAEnv) > 0 {
			rows = append(rows, Row{"wall.ca_env", strings.Join(shownNames(r.Wall.CAEnv), ", "), origin})
		}
		if r.Wall.PIDs > 0 {
			rows = append(rows, Row{"wall.pids_limit", strconv.Itoa(r.Wall.PIDs), origin})
		}
		if r.Wall.Setup != "" {
			rows = append(rows, Row{"wall.setup", r.Wall.Setup, origin})
		}
		// The path alone: the file's contents are never read here.
		if r.Wall.EngineProxyFile != "" {
			rows = append(rows, Row{"wall.engine_proxy_file", exports.Value(r.Wall.EngineProxyFile, false), origin})
		}
	} else {
		rows = append(rows, Row{"wall.adapter", "(none)", Default})
	}
	if r != nil && r.Pool != nil {
		rows = append(rows, r.Pool.rows(origin)...)
	}
	return rows
}

// shownNames are the names of a list as the rows print them, each as
// [accesskey.ShownName] prints it; nil for none.
func shownNames(list []string) []string {
	var out []string
	for _, n := range list {
		out = append(out, accesskey.ShownName(n, false))
	}
	return out
}

// viaRows are the rows of gateway.server.via: its url, or that the server is reached
// directly; with a via, its ca_file as [exports.Value] prints it and certificate_sha256
// as the file writes it, or their defaults. What the CA file holds is never read here.
func viaRows(v *ForagerVia, origin string) []Row {
	if v == nil {
		return []Row{{"gateway.server.via.url", "(none: the server is reached directly)", Default}}
	}
	rows := []Row{{"gateway.server.via.url", v.URL, origin}}
	if v.CAFile != "" {
		rows = append(rows, Row{"gateway.server.via.ca_file", exports.Value(v.CAFile, false), origin})
	} else {
		rows = append(rows, Row{"gateway.server.via.ca_file", "(none: the system's roots)", Default})
	}
	if v.CertificateSHA256 != "" {
		rows = append(rows, Row{"gateway.server.via.certificate_sha256", v.CertificateSHA256, origin})
	} else {
		rows = append(rows, Row{"gateway.server.via.certificate_sha256", "(none)", Default})
	}
	return rows
}
