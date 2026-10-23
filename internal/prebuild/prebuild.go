// Package prebuild builds a repository's development container into the image a walled
// run starts from: what a pool's prebuild runs, in the pool's process, on a repository
// it fetched. It builds local images and never pushes one or logs in.
//
// [Build] reads the configuration with internal/devcontainer and refuses, before
// anything is built or run, what the image cannot be built from, and a repository whose
// configuration of git, which the image keeps, such as .git/config, holds a credential
// for git. A configuration that has initializeCommand, which runs on the host, reaches
// the devcontainer command as a copy without it, and a line says it is not run.
//
// It builds in two steps. The environment step builds the definition, image or the
// Dockerfile and the features, with the reference devcontainer command at
// [CLIVersion], into the environment image. With [Options.Registry] set, the command
// reads a copy of the definition in which each image on one of [PublicHosts] with no
// credential in the build's run configuration is the registry's copy,
// <registry>/upstream/<host>/<path>: devcontainer.json's image, and FROM and COPY
// --from in a copy of the Dockerfile. Feature IDs stay as written. The step is skipped when [Options.EnvironmentImage] is
// set, and when the definition is a standard image, <registry>/shared/<name>, with no
// feature and no Dockerfile. The build keeps the environment image apart, for other
// targets with the same definition, only when it depends on the definition's bytes
// alone, as [build.chooseSource] has it. The target step adds the agent image's
// [AgentPath] on top, copies the target's files to [Workspace], runs onCreateCommand and
// updateContentCommand there in a container of the image as the wall user, moves the
// tree to [SeedPath] and commits the container as the target image. The programs it
// runs see only [AllowedEnv] of this process's environment, and the commands only the
// image's environment and the configuration's.
//
// At run time Forager starts the image as the wall user, mounts the run's working
// folder at [Workspace], the hook socket's folder at [HooksPath] and the qory helper at
// [wall.HelperPath]; qory run prepare copies [SeedPath] into the working folder.
package prebuild

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/qory/internal/agentenv"
	"github.com/qoryai/qory/internal/devcontainer"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/image"
	"github.com/qoryai/qory/internal/integration"
)

// CLIVersion is the version of the reference devcontainer command, @devcontainers/cli,
// the build runs: 0.89.0, the newest release two weeks before this was written,
// https://github.com/devcontainers/cli/tree/v0.89.0. The devcontainer command on the
// PATH must report exactly it.
const CLIVersion = "0.89.0"

// Where the image keeps what a run uses.
const (
	// Workspace is where a run mounts its working folder, and where the commands run.
	Workspace = "/workspace"
	// SeedPath is where the image keeps the repository's tree, as the commands left it.
	SeedPath = "/qory/seed"
	// AgentPath is the agent image's folder the image takes whole.
	AgentPath = "/qory/agent"
	// AgentHome is the image's HOME when the image sets none.
	AgentHome = "/home/agent"
	// HooksPath is where a run mounts its hook socket's folder; the image has nothing
	// there.
	HooksPath = "/qory/hooks"
)

// MaxSize is the largest image the build keeps, as docker image inspect reports its
// size: 4 GiB.
const MaxSize = 4 << 30

// Limit is how long the whole build may take.
const Limit = 30 * time.Minute

// Prefix starts every line of the build, and every line its seed step prints.
const Prefix = "prebuild: "

// prefix is [Prefix], as the texts below use it.
const prefix = Prefix

// SeedArgs are the arguments of the qory helper that select its seed step, the hidden
// verb qory run seed, which [Seed] is.
var SeedArgs = []string{"run", "seed"}

// The words of the build. Each is one format string.
const (
	textRead         = prefix + "read %s in %s"
	textProxyUser    = prefix + "%s holds an @, which can put a user name or a password in a URL, a secret; the build runs without it"
	textNone         = prefix + "the repository has no development container configuration, .devcontainer/devcontainer.json or .devcontainer.json; no image is built"
	textFolder       = prefix + "workspaceFolder is %s; the image is built at " + Workspace + ", where a run mounts its working folder, and the lifecycle commands see ${containerWorkspaceFolder} as " + Workspace
	textInitialize   = prefix + initializeMember + " is not run; the image is built without it"
	textMerged       = prefix + "read the configuration merged with its features and base image in %s"
	textBuilt        = prefix + "built the development container image in %s"
	textAdded        = prefix + "added the agent image's " + AgentPath + " in %s"
	textCopied       = prefix + "copied the repository to " + Workspace + " in %s"
	textCommitted    = prefix + "committed the image in %s"
	textDone         = prefix + "built %s in %s: %s"
	textNotDigest    = "pin wall.image by its digest, name@sha256: and 64 hexadecimal digits: the build takes the agent image by digest alone"
	textNotUser      = "set wall.user to uid:gid, two numbers, neither of them 0"
	textNotTag       = "set pool.registry to an image name with no tag and no digest: the name of the image to make is not one"
	textNoRepo       = "the repository folder %s is not a folder"
	textNoRepoFlag   = "give the build the folder of the repository: it is not set"
	textNoHelper     = "qory's Linux build %s: %s"
	textHelperArch   = "qory's Linux build %s is for %s, and the engine runs %s; give the Linux build for %s"
	textNoEngine     = "the docker command reaches no engine: %s"
	textLegacy       = "%s sets %s, an earlier form of build.dockerfile and build.context; no image is built from it: write them under build"
	textCompose      = "%s sets dockerComposeFile; an image is built only from image or build, and Docker Compose can give the container access to the host"
	textOptions      = "%s sets build.options, options of docker build such as --network=host; no image is built from a configuration that sets them"
	textNoImage      = "%s sets neither image nor build; an image is built only from one of them"
	textCLIMissing   = "no devcontainer command on the PATH; the build runs @devcontainers/cli " + CLIVersion
	textCLIVersion   = "the devcontainer command is %s, and the build runs @devcontainers/cli " + CLIVersion + " exactly"
	textMergedUnread = "devcontainer read-configuration printed what does not read as its result"
	textFeaturesAsk  = "the features or the base image of %s ask for %s, which give the container access to the host; no image is built from them"
	textLabelAsks    = "the built image's devcontainer.metadata asks for %s, which give the container access to the host; the image is removed"
	textLabelUnread  = "the built image's devcontainer.metadata does not read as JSON; the image is removed"
	textBuildFailed  = "devcontainer build did not build the image: %s"
	textCommandsExit = "the container of the lifecycle commands exited with status %d"
	textEnvConfig    = "%s sets %s in %s, which change what the agent does; no image is built from it"
	textEnvImage     = "the image sets %s in its environment, which change what the agent does; the image is removed"
	textProxyBaked   = "the final image sets %s in its environment to a value the image before the commands did not; the image is removed"
	textTooBig       = "the image is %s, more than the limit of 4 GiB; it is removed"
	textSizeUnread   = "docker image inspect printed a size that does not read: %s"
	textNotCopied    = "the repository could not be copied to " + Workspace + ": %s"
	textConfigUnread = "docker image inspect printed a configuration that does not read"
	textLimit        = "the build ran past its limit of 30 minutes and was stopped; what it started is removed"
	textStopped      = "the build was stopped before it finished; what it started is removed"
)

// The names of the labels and keys the build reads.
const (
	metadataLabel    = "devcontainer.metadata"
	initKey          = "init"
	initializeMember = "initializeCommand"
)

// refusedMembers are the members the build refuses when they are set: dockerFile and
// context, an earlier form of build.dockerfile and build.context, which the reference
// implementation builds from in place of image; dockerComposeFile, a Docker Compose
// configuration, which can set privileged, the host's network and mounts; and
// build.options, options of docker build, such as --network=host. dockerComposeFile
// is refused whenever the configuration has it, [build.checkConfig].
var refusedMembers = []string{"dockerFile", "context", "dockerComposeFile", "build.options"}

// buildMembers are the members [devcontainer.LoadMembers] reads for the build: the
// [refusedMembers], build.cacheFrom, which [checkCacheFrom] checks, and build and
// features, which the sharing rule reads, [build.chooseSource].
var buildMembers = append(slices.Clone(refusedMembers), "build.cacheFrom", "build", "features")

// featureAccess are the members of the merged configuration, and of an entry of the
// devcontainer.metadata label, by which a feature or the base image asks for access to
// the host: the reference implementation merges entrypoint into entrypoints. init is
// one when the configuration does not set it.
var featureAccess = []string{"privileged", "capAdd", "securityOpt", "mounts", "entrypoint", "entrypoints"}

var (
	digestRef = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	userShape = regexp.MustCompile(`^([0-9]+):([0-9]+)$`)
)

// ErrNoConfiguration is a repository with no configuration: nothing is built, and a
// line says so.
var ErrNoConfiguration = errors.New("no development container configuration")

// ErrLimit is a build that ran past [Limit]; ErrStopped one whose context ended
// otherwise, by a signal. Either way, what it started is ended and removed.
var (
	ErrLimit   = errors.New(textLimit)
	ErrStopped = errors.New(textStopped)
)

// ErrImageMissing is an environment image, [Options.EnvironmentImage], that its
// registry does not have: docker pull found no manifest or no repository for it. The
// build's error matches it with errors.Is, and its text is the pull's.
var ErrImageMissing = errors.New("the environment image is not in its registry")

// RefusedError is a refusal: a flag, the repository or its configuration that no image
// is built from, before anything is built or run, or what the build made that is not
// kept.
type RefusedError struct{ Err error }

// Error is the refusal's text.
func (e *RefusedError) Error() string { return e.Err.Error() }

// Unwrap returns the refusal.
func (e *RefusedError) Unwrap() error { return e.Err }

// refused wraps err as a [RefusedError].
func refused(err error) error { return &RefusedError{err} }

// ShownError is a failure the seed step printed already, in the container: its line,
// without [prefix].
type ShownError struct{ Line string }

// Error is the line.
func (e *ShownError) Error() string { return e.Line }

// Options are what [Build] builds, and how.
type Options struct {
	// Repository is the folder of the repository, fetched at a commit.
	Repository string
	// AgentImage is the agent image, name@sha256:digest.
	AgentImage string
	// User is the wall user, uid:gid, neither of them 0.
	User string
	// Tag is the name of the image to make.
	Tag string
	// Helper is qory's static Linux build for the engine's architecture, which runs the
	// seed step in the container, and SeedArgs the arguments that select it.
	Helper   string
	SeedArgs []string
	// Env is this process's environment; only [AllowedEnv] of it reaches a program.
	Env []string
	// Out receives the lines, and Log what the programs print.
	Out, Log io.Writer
	// Limit is how long the build may take; 0 is [Limit]. A test shortens it.
	Limit time.Duration
	// Name is what the temporary containers and images are named after; empty is a
	// random one. A test sets it.
	Name string

	// Registry is the address of the instance's registry, a host and a port or none,
	// under which upstream/<host>/<path> are the copies of public images and
	// shared/<name> the standard images. "" rewrites no reference and takes no image as
	// a standard one.
	Registry string
	// CredentialHosts are the hosts that have a credential in the build's run
	// configuration. A reference on one of them is not rewritten: the registry makes
	// its copies with no credential.
	CredentialHosts []string
	// EnvironmentImage, when it is set, is the environment image the target image
	// starts from, name@sha256:digest: the environment step is skipped.
	EnvironmentImage string
	// EnvironmentTag is the name of the environment image the build keeps apart; ""
	// is <Name>:environment.
	EnvironmentTag string
}

// Result is what [Build] made.
type Result struct {
	// Environment is the environment image the build kept apart, a local reference:
	// [Options.EnvironmentTag], or <Name>:environment. It is "" when the build kept
	// none: it started from [Options.EnvironmentImage] or a standard image, or the
	// environment image depends on more than the definition's bytes. The caller pushes
	// it or removes it.
	Environment string
	// Target is the target image, a local reference: [Options.Tag].
	Target string
	// Base is the image the definition starts from, name@sha256:digest: the standard
	// image, or the image of devcontainer.json or of the Dockerfile's last stage, or of
	// build.target. It is "" when the build started from [Options.EnvironmentImage], or
	// from scratch, or from an image it cannot resolve before it builds.
	Base string
}

// user is a checked uid:gid.
type user struct{ uid, gid int }

// String is uid:gid.
func (u user) String() string { return strconv.Itoa(u.uid) + ":" + strconv.Itoa(u.gid) }

// parseUser reads uid:gid: two numbers of at most 32 bits, neither of them 0, as
// Forager's wall takes the user of a run in an image by reference.
func parseUser(s string) (user, error) {
	m := userShape.FindStringSubmatch(s)
	if m == nil {
		return user{}, refused(errors.New(textNotUser))
	}
	uid, err1 := strconv.ParseUint(m[1], 10, 31)
	gid, err2 := strconv.ParseUint(m[2], 10, 31)
	if err1 != nil || err2 != nil || uid == 0 || gid == 0 {
		return user{}, refused(errors.New(textNotUser))
	}
	return user{int(uid), int(gid)}, nil
}

// build is one run of [Build]: the options, checked, and what it made, for the clean-up.
type build struct {
	o    Options
	u    user
	repo string
	cfg  *devcontainer.Config
	set  []devcontainer.Member
	// config is the configuration the devcontainer command reads: the repository's, or
	// a copy of it without initializeCommand, [build.writeConfig].
	config string
	// initialize says the line that initializeCommand is not run was printed.
	initialize bool
	// top is the configuration of the image the commands run in, as built.
	top imageConfig
	// homeSet says the image devcontainer build made sets HOME in its environment.
	homeSet bool
	e       engine
	name    string
	tmp     string
	images  []string
	// created says the container may exist: docker create ran.
	created bool

	// up rewrites the definition's images, [upstream].
	up upstream
	// source is what the target step starts from.
	source source
	// share is whether the build keeps the environment image apart; notShared is why
	// not.
	share     bool
	notShared string
	// dockerfile is the Dockerfile's path relative to the repository, with slashes, as
	// [confine] resolved it, "" for none; dockerfileData is its copy as rewritten.
	dockerfile     string
	dockerfileData []byte
	// engine is the platform of the Docker engine, os/arch, which the build runs on.
	engine string
	// edit is the definition's copy as [build.rewrite] set it, and rewritten is whether
	// it changes a reference.
	edit      devcontainer.Edit
	rewritten bool
	// environment is the environment image the build tagged, removed unless the build
	// succeeds.
	environment string
	// res is what the build made.
	res Result
}

// Build builds the image o names, as the package says, and prints a line for each step
// on o.Out with how long it took, and the image's size at the end. A repository with no
// configuration prints a line and returns [ErrNoConfiguration]. Every refusal is a
// [RefusedError], and comes before anything is built or run, but for what only the
// built image shows. Whatever the outcome, every temporary container, folder and image
// is removed, and so are o.Tag and the environment image when the build made them and
// then failed.
func Build(ctx context.Context, o Options) (Result, error) {
	limit := o.Limit
	if limit == 0 {
		limit = Limit
	}
	ctx, cancel := context.WithTimeoutCause(ctx, limit, ErrLimit)
	defer cancel()
	env, withheld := allowed(o.Env)
	name, value, _ := strings.Cut(BuildKit, "=")
	env = withValue(env, name, value)
	b := &build{o: o, e: engine{env: env, log: o.Log}}
	for _, name := range withheld {
		b.line(textProxyUser, name)
	}
	err := b.run(ctx)
	b.clean(ctx)
	if err != nil && ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), ErrLimit) {
			return Result{}, ErrLimit
		}
		return Result{}, ErrStopped
	}
	if err != nil {
		return Result{}, err
	}
	return b.res, nil
}

// run is [Build] without its limit and its clean-up.
func (b *build) run(ctx context.Context) error {
	start := time.Now()
	if err := b.checkOptions(); err != nil {
		return err
	}
	if err := b.checkSteps(); err != nil {
		return err
	}
	cfg, set, err := devcontainer.LoadMembers(b.repo, buildMembers)
	if err != nil {
		return refused(err)
	}
	if cfg == nil {
		b.line(textNone)
		return ErrNoConfiguration
	}
	b.cfg, b.set = cfg, set
	b.line(textRead, cfg.Path, seconds(time.Since(start)))
	if err := b.checkConfig(); err != nil {
		return err
	}
	if err := checkTree(b.repo); err != nil {
		return err
	}
	b.name = b.o.Name
	if b.name == "" {
		var r [6]byte
		_, _ = rand.Read(r[:])
		b.name = "qory-prebuild-" + hex.EncodeToString(r[:])
	}
	if b.tmp, err = os.MkdirTemp("", b.name+"-"); err != nil {
		return err
	}
	if err := b.checkGitConfig(ctx); err != nil {
		return err
	}
	b.chooseSource()
	if b.source == fromBuild {
		if err := b.rewrite(); err != nil {
			return err
		}
	}
	if err := b.writeConfig(); err != nil {
		return err
	}
	config, original, err := dockerConfig(b.tmp, b.o.Env)
	if err != nil {
		return err
	}
	b.e.env = withValue(b.e.env, "DOCKER_CONFIG", config)
	// docker reads a TLS engine's ca.pem, cert.pem and key.pem from DOCKER_CERT_PATH,
	// else from the folder DOCKER_CONFIG names, which is now the build's: they stay
	// where they are, in the original.
	if lastValue(b.e.env, "DOCKER_CERT_PATH") == "" && original != "" {
		b.e.env = withValue(b.e.env, "DOCKER_CERT_PATH", original)
	}
	if err := b.checkPrograms(ctx); err != nil {
		return err
	}
	from, top := b.name+":base", b.name+":top"
	switch b.source {
	case fromEnvironment:
		if _, err := b.takePulled(ctx, b.o.EnvironmentImage, textFromEnv); err != nil {
			if nf, ok := errors.AsType[*notFoundError](err); ok {
				nf.env = true
			}
			return err
		}
		from = b.o.EnvironmentImage
	case fromStandard:
		if from, err = b.takePulled(ctx, b.cfg.Image, textStandard); err != nil {
			return err
		}
		b.res.Base = from
	default:
		if err := b.checkMerged(ctx); err != nil {
			return err
		}
		if err := b.resolveBase(ctx); err != nil {
			return err
		}
		if err := b.buildBase(ctx, from); err != nil {
			return err
		}
		if err := b.keepEnvironment(ctx, from); err != nil {
			return err
		}
	}
	if err := b.addAgent(ctx, from, top); err != nil {
		return err
	}
	if err := b.runCommands(ctx, top); err != nil {
		return err
	}
	return b.finish(ctx, start)
}

// line prints one line on the options' Out.
func (b *build) line(format string, args ...any) {
	fmt.Fprintf(b.o.Out, format+"\n", args...)
}

// checkOptions refuses options no image is built from.
func (b *build) checkOptions() error {
	if !digestRef.MatchString(b.o.AgentImage) {
		return refused(errors.New(textNotDigest))
	}
	u, err := parseUser(b.o.User)
	if err != nil {
		return err
	}
	b.u = u
	if image.CheckRef(b.o.Tag) != nil || strings.Contains(b.o.Tag, "@") {
		return refused(errors.New(textNotTag))
	}
	if b.o.Repository == "" {
		return refused(errors.New(textNoRepoFlag))
	}
	repo, err := filepath.Abs(b.o.Repository)
	if err != nil {
		return refused(fmt.Errorf(textNoRepo, integration.Printable(exports.Value(b.o.Repository, false))))
	}
	if fi, err := os.Stat(repo); err != nil || !fi.IsDir() {
		return refused(fmt.Errorf(textNoRepo, integration.Printable(exports.Value(b.o.Repository, false))))
	}
	b.repo = repo
	return nil
}

// checkConfig refuses a configuration the image is not built from: the earlier form of
// build, neither image nor build, a variable of the host in what the build reads, and
// a member that gives the container access to the host. It prints a line for a
// workspaceFolder other than [Workspace].
func (b *build) checkConfig() error {
	c := b.cfg
	// The reference implementation builds with Docker Compose whenever the
	// configuration has dockerComposeFile and no Dockerfile, set or not: [], "" and
	// null fall back to the default Compose files (spec-node/devContainersSpecCLI.ts,
	// doBuild, 'dockerComposeFile' in config).
	if c.Has("dockerComposeFile") {
		return refused(fmt.Errorf(textCompose, c.Path))
	}
	var legacy []string
	for _, m := range b.set {
		switch m.Name {
		case "dockerFile", "context":
			legacy = append(legacy, m.Name)
		case "dockerComposeFile":
			return refused(fmt.Errorf(textCompose, c.Path))
		case "build.options":
			return refused(fmt.Errorf(textOptions, c.Path))
		}
	}
	if len(legacy) > 0 {
		return refused(fmt.Errorf(textLegacy, c.Path, list(legacy)))
	}
	for _, m := range b.set {
		if m.Name == "build.cacheFrom" {
			if err := checkCacheFrom(c.Path, m.Value); err != nil {
				return err
			}
		}
	}
	if c.Image == "" && c.Build == nil {
		return refused(fmt.Errorf(textNoImage, c.Path))
	}
	vars := devcontainer.Vars{WorkspaceFolder: Workspace, WorkspaceFolderBasename: path.Base(Workspace)}
	values := [][2]string{{"image", c.Image}, {"workspaceFolder", c.WorkspaceFolder}}
	if c.Build != nil {
		values = append(values, [2]string{"build.dockerfile", c.Build.Dockerfile}, [2]string{"build.context", c.Build.Context}, [2]string{"build.target", c.Build.Target})
		values = append(values, sorted("build.args", c.Build.Args)...)
	}
	values = append(values, sorted("containerEnv", c.ContainerEnv)...)
	values = append(values, sorted("remoteEnv", c.RemoteEnv)...)
	for _, v := range values {
		if _, err := vars.Expand(v[0], v[1]); err != nil {
			return refused(err)
		}
	}
	if err := c.CheckHostAccess(); err != nil {
		return refused(err)
	}
	dockerfile, err := confine(b.repo, c)
	if err != nil {
		return err
	}
	if err := checkFrontend(b.repo, c, dockerfile); err != nil {
		return err
	}
	b.dockerfile = dockerfile
	for _, m := range []struct {
		member string
		values map[string]string
	}{{"containerEnv", c.ContainerEnv}, {"remoteEnv", c.RemoteEnv}} {
		if names := agentenv.Widening(slices.Sorted(mapKeys(m.values))); len(names) > 0 {
			return refused(fmt.Errorf(textEnvConfig, c.Path, printables(names), m.member))
		}
	}
	if f := c.WorkspaceFolder; f != "" && path.Clean(f) != Workspace {
		b.line(textFolder, integration.Printable(exports.Value(f, false)))
	}
	return nil
}

// sorted is an object's members as where.name and value, in the order of their names.
func sorted(where string, m map[string]string) [][2]string {
	var out [][2]string
	for _, name := range slices.Sorted(mapKeys(m)) {
		out = append(out, [2]string{where + "." + integration.Printable(accesskey.ShownName(name, false)), m[name]})
	}
	return out
}

// mapKeys is the names of m.
func mapKeys(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// list is names as a sentence has them: a, a and b, a, b and c.
func list(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// checkTree refuses a repository that holds a file of a kind [writeTree] does not copy,
// before anything is built.
func checkTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return refused(fmt.Errorf(textNotCopied, integration.Printable(reason(err))))
		}
		t := d.Type()
		if t.IsDir() || t.IsRegular() || t&fs.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		return refused(fmt.Errorf(textOddFile, integration.Printable(exports.HidePath(filepath.ToSlash(rel)))))
	})
}

// checkPrograms refuses a devcontainer command of another version than [CLIVersion], a
// helper that is no static Linux build, and one for another architecture than the
// engine's.
func (b *build) checkPrograms(ctx context.Context) error {
	out, err := b.e.output(ctx, "devcontainer", "--version")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		var f *failed
		if errors.As(err, &f) {
			return refused(fmt.Errorf(textCLIVersion, "one that does not report its version"))
		}
		return refused(errors.New(textCLIMissing))
	}
	if v := strings.TrimSpace(string(out)); v != CLIVersion {
		return refused(fmt.Errorf(textCLIVersion, integration.Printable(v)))
	}
	arch, err := image.HelperArch(b.o.Helper)
	if err != nil {
		return refused(fmt.Errorf(textNoHelper, integration.Printable(exports.Value(b.o.Helper, false)), exports.HideValueError(err, b.o.Helper)))
	}
	out, err = b.e.output(ctx, "docker", "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	platform := strings.TrimSpace(string(out))
	if err != nil || !strings.Contains(platform, "/") {
		return fmt.Errorf(textNoEngine, integration.Printable(fmt.Sprint(err)))
	}
	if want := image.Arch(platform); arch != want {
		return refused(fmt.Errorf(textHelperArch, integration.Printable(exports.Value(b.o.Helper, false)), arch, integration.Printable(platform), want))
	}
	b.engine = platform
	return nil
}

// configPath is the path of the configuration the devcontainer command reads: the
// repository's, or the copy [build.writeConfig] wrote.
func (b *build) configPath() string {
	if b.config != "" {
		return b.config
	}
	return filepath.Join(b.repo, filepath.FromSlash(b.cfg.Path))
}

// writeConfig writes, when the configuration has initializeCommand, whatever its value,
// or [build.rewrite] rewrote a reference, a copy of it to a folder of the temporary
// folder, under the same name, for the devcontainer command to read in its place: without
// initializeCommand, which prints a line, and with the references rewritten. The
// command is given the repository as its workspace folder still, and the copy's paths
// name what the configuration's do: [devcontainer.Config.RelocateEdited].
// initializeCommand runs on the host. devcontainer build at [CLIVersion] does not run
// it: only up does (spec-node/configContainer.ts, resolveWithLocalFolder), and build
// reads the configuration and builds (spec-node/devContainersSpecCLI.ts, doBuild). With
// the copy, no version of the command can.
func (b *build) writeConfig() error {
	initialize := b.cfg.Has(initializeMember)
	if !initialize && !b.rewritten {
		return nil
	}
	dir := filepath.Join(b.tmp, "devcontainer")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	data, err := b.cfg.RelocateEdited(b.repo, dir, b.edits())
	if err != nil {
		return err
	}
	copied := filepath.Join(dir, path.Base(b.cfg.Path))
	if err := os.WriteFile(copied, data, 0o600); err != nil {
		return err
	}
	b.config = copied
	if initialize {
		b.initializeLine()
	}
	return nil
}

// initializeLine prints, once, that initializeCommand is not run.
func (b *build) initializeLine() {
	if !b.initialize {
		b.line(textInitialize)
		b.initialize = true
	}
}

// checkMerged reads the configuration merged with its features' metadata and its base
// image's devcontainer.metadata, as devcontainer read-configuration
// --include-merged-configuration has it, and refuses one whose features or base image
// ask for access to the host. It runs no lifecycle command and builds nothing; it
// fetches the features' metadata, and inspects the base image, pulling it when the
// registry does not answer. For a Dockerfile it reads the image the Dockerfile starts
// from, not what the Dockerfile adds, which the built image's label shows:
// [build.checkLabel].
func (b *build) checkMerged(ctx context.Context) error {
	start := time.Now()
	out, err := b.e.output(ctx, "devcontainer", "read-configuration",
		"--workspace-folder", b.repo, "--config", b.configPath(),
		"--include-merged-configuration", "--log-format", "text")
	if err != nil {
		return err
	}
	var result struct {
		MergedConfiguration map[string]json.RawMessage `json:"mergedConfiguration"`
	}
	if json.Unmarshal(lastJSON(out), &result) != nil || result.MergedConfiguration == nil {
		return errors.New(textMergedUnread)
	}
	// The copy has no initializeCommand; the reference implementation takes none from
	// a feature or the base image either (spec-node/imageMetadata.ts, mergeConfiguration).
	// One the merged configuration has still is never run by devcontainer build.
	if _, ok := result.MergedConfiguration[initializeMember]; ok {
		b.initializeLine()
	}
	if asked := b.asks([]map[string]json.RawMessage{result.MergedConfiguration}); len(asked) > 0 {
		return refused(fmt.Errorf(textFeaturesAsk, b.cfg.Path, list(asked)))
	}
	b.line(textMerged, seconds(time.Since(start)))
	return nil
}

// asks is the members of [featureAccess] the entries set, and init when one sets it and
// the configuration does not, in that order, each once.
func (b *build) asks(entries []map[string]json.RawMessage) []string {
	var out []string
	names := append(slices.Clone(featureAccess), initKey)
	for _, name := range names {
		if name == initKey && b.cfg.Init {
			continue
		}
		for _, e := range entries {
			if raw, ok := e[name]; ok && isSet(raw) {
				shown := name
				if name == "entrypoints" {
					shown = "entrypoint"
				}
				if !slices.Contains(out, shown) {
					out = append(out, shown)
				}
				break
			}
		}
	}
	return out
}

// isSet reports whether a value is set: neither null, false, "", [] nor {}.
func isSet(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return true
	}
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

// lastJSON is the last line of out that starts with {, where the devcontainer command
// prints its result.
func lastJSON(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); bytes.HasPrefix(l, []byte("{")) {
			return l
		}
	}
	return nil
}

// buildBase builds the development container image as base with devcontainer build,
// which runs no lifecycle command and starts no container, and writes no lock file
// into the repository; then it refuses an image whose label asks for access to the
// host.
func (b *build) buildBase(ctx context.Context, base string) error {
	start := time.Now()
	b.images = append(b.images, base)
	out, err := b.e.output(ctx, "devcontainer", "build",
		"--workspace-folder", b.repo, "--config", b.configPath(),
		"--image-name", base, "--no-lockfile", "--log-format", "text")
	if err != nil {
		var result struct{ Message string }
		if json.Unmarshal(lastJSON(out), &result) == nil && result.Message != "" {
			return fmt.Errorf(textBuildFailed, integration.Printable(exports.HideValues(exports.HideURLs(result.Message))))
		}
		return err
	}
	var result struct{ Outcome string }
	if json.Unmarshal(lastJSON(out), &result) != nil || result.Outcome != "success" {
		return fmt.Errorf(textBuildFailed, "it printed no success")
	}
	if err := b.checkLabel(ctx, base); err != nil {
		return err
	}
	b.line(textBuilt, seconds(time.Since(start)))
	return nil
}

// imageConfig is what the build reads of an image's configuration.
type imageConfig struct {
	Entrypoint []string
	Cmd        []string
	Env        []string
	Labels     map[string]string
}

// inspectConfig reads ref's configuration.
func (b *build) inspectConfig(ctx context.Context, ref string) (imageConfig, error) {
	var c imageConfig
	out, err := b.e.output(ctx, "docker", "image", "inspect", "--format", "{{json .Config}}", ref)
	if err != nil {
		return c, err
	}
	if json.Unmarshal(bytes.TrimSpace(out), &c) != nil {
		return c, errors.New(textConfigUnread)
	}
	return c, nil
}

// checkLabel refuses a built image whose environment sets a variable [checkEnv]
// refuses, or whose devcontainer.metadata label, an array of entries or one, asks for
// access to the host as [build.asks] has it. The label holds what read-configuration
// could not see before the build: what a Dockerfile adds.
func (b *build) checkLabel(ctx context.Context, ref string) error {
	c, err := b.inspectConfig(ctx, ref)
	if err != nil {
		return err
	}
	if err := checkEnv(c); err != nil {
		return err
	}
	b.homeSet = homeSet(c.Env)
	asked, ok := b.labelAsks(c)
	switch {
	case !ok:
		return refused(errors.New(textLabelUnread))
	case len(asked) > 0:
		return refused(fmt.Errorf(textLabelAsks, list(asked)))
	}
	return nil
}

// labelAsks is what the devcontainer.metadata label of c, an array of entries or one,
// asks for, as [build.asks] has it; ok is false for a label that does not read as JSON.
func (b *build) labelAsks(c imageConfig) (asked []string, ok bool) {
	label, set := c.Labels[metadataLabel]
	if !set {
		return nil, true
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal([]byte(label), &entries) != nil {
		var one map[string]json.RawMessage
		if json.Unmarshal([]byte(label), &one) != nil {
			return nil, false
		}
		entries = []map[string]json.RawMessage{one}
	}
	return b.asks(entries), true
}

// checkEnv refuses an image whose environment sets a variable [agentenv.Widens]
// names, naming each, never its value.
func checkEnv(c imageConfig) error {
	if widening := envWidening(c); len(widening) > 0 {
		return refused(fmt.Errorf(textEnvImage, printables(widening)))
	}
	return nil
}

// envWidening are the variables of c's environment that [agentenv.Widening] returns.
func envWidening(c imageConfig) []string {
	var names []string
	for _, kv := range c.Env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return agentenv.Widening(names)
}

// homeSet reports whether an environment sets HOME to a value that is not empty.
func homeSet(env []string) bool {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok && v != "" {
			return true
		}
	}
	return false
}

// printables is names, each printable and as [accesskey.ShownName] prints it, as a
// sentence lists them.
func printables(names []string) string {
	shown := make([]string, len(names))
	for i, n := range names {
		shown[i] = integration.Printable(accesskey.ShownName(n, false))
	}
	return list(shown)
}

// addAgent builds top from base with a Dockerfile written to a temporary folder: it
// copies the agent image's [AgentPath] whole, and makes the wall user the image's user.
// When base sets no HOME, it sets HOME to [AgentHome], which the repository's copy
// makes, the wall user's, with mode 0700, and the final image keeps: a run, which
// passes no HOME, finds the caches the commands left there. The lifecycle commands run
// with it unless containerEnv or remoteEnv sets HOME; they then run with that one, as
// [Seed] has it, and the image's environment keeps [AgentHome].
func (b *build) addAgent(ctx context.Context, base, top string) error {
	start := time.Now()
	b.images = append(b.images, top)
	dockerfile := "FROM " + base + "\n" +
		"COPY --from=" + b.o.AgentImage + " " + AgentPath + " " + AgentPath + "\n" +
		"USER " + b.u.String() + "\n"
	if !b.homeSet {
		dockerfile += "ENV HOME=" + AgentHome + "\n"
	}
	if err := b.dockerBuild(ctx, "agent", top, dockerfile); err != nil {
		return err
	}
	c, err := b.inspectConfig(ctx, top)
	if err != nil {
		return err
	}
	if err := checkEnv(c); err != nil {
		return err
	}
	b.top = c
	b.line(textAdded, seconds(time.Since(start)))
	return nil
}

// dockerBuild builds tag with docker build from dockerfile, written to the folder sub
// of the temporary folder, which is the build's whole context.
func (b *build) dockerBuild(ctx context.Context, sub, tag, dockerfile string) error {
	dir := filepath.Join(b.tmp, sub)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		return err
	}
	return b.e.run(ctx, nil, b.o.Log, "docker", "build", "--tag", tag, "--file", filepath.Join(dir, "Dockerfile"), dir)
}

// runCommands creates a container of top that runs the seed step from the helper, as
// the wall user, at [Workspace], with no capability, no new privilege, docker's default
// network, no socket and no mount but the helper's; copies the repository into it; and
// starts it, passing on the lines it prints. A step that failed printed its line.
func (b *build) runCommands(ctx context.Context, top string) error {
	start := time.Now()
	args := []string{"create", "--name", b.name, "--pull", "never",
		"--user", b.u.String(), "--workdir", Workspace,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--mount", mountArg(b.o.Helper),
		"--entrypoint", wall.HelperPath, top}
	b.created = true
	if err := b.e.run(ctx, nil, io.Discard, "docker", append(args, b.o.SeedArgs...)...); err != nil {
		return err
	}
	r, w := io.Pipe()
	written := make(chan error, 1)
	go func() {
		err := writeTree(w, b.repo, b.u.uid, b.u.gid, !b.homeSet)
		w.CloseWithError(err)
		written <- err
	}()
	err := b.e.run(ctx, r, io.Discard, "docker", "cp", "--archive", "-", b.name+":/")
	r.CloseWithError(errors.New("docker cp ended"))
	if werr := <-written; werr != nil && ctx.Err() == nil {
		var re *RefusedError
		if errors.As(werr, &re) {
			return werr
		}
		return fmt.Errorf(textNotCopied, integration.Printable(reason(werr)))
	}
	if err != nil {
		return err
	}
	b.line(textCopied, seconds(time.Since(start)))
	lines := &relay{out: b.o.Out}
	err = b.e.run(ctx, nil, lines, "docker", "start", "--attach", b.name)
	lines.flush()
	var f *failed
	if errors.As(err, &f) {
		if f.code == 1 && lines.last != "" {
			return &ShownError{Line: exports.HideValues(strings.TrimPrefix(lines.last, prefix))}
		}
		return fmt.Errorf(textCommandsExit, f.code)
	}
	return err
}

// mountArg is the --mount value that mounts the helper read-only at
// [wall.HelperPath], its source quoted as a CSV field when it holds a comma or a quote.
func mountArg(helper string) string {
	src := "source=" + helper
	if strings.ContainsAny(src, ",\"") {
		src = `"` + strings.ReplaceAll(src, `"`, `""`) + `"`
	}
	return "type=bind," + src + ",target=" + wall.HelperPath + ",readonly"
}

// finish commits the container, the image's user the wall user and its working folder
// [Workspace], then makes o.Tag from the commit with a Dockerfile that restores the
// entry point and the command top had before the container replaced them, which adds
// no layer. docker commit --change keeps the container's entry point when it is
// given an empty one, and a Dockerfile's ENTRYPOINT [] clears it. It refuses an image
// over [MaxSize].
func (b *build) finish(ctx context.Context, start time.Time) error {
	at := time.Now()
	c := b.top
	seeded := b.name + ":seeded"
	b.images = append(b.images, seeded)
	if err := b.e.run(ctx, nil, io.Discard, "docker", "commit",
		"--change", "USER "+b.u.String(), "--change", "WORKDIR "+Workspace,
		b.name, seeded); err != nil {
		return err
	}
	entrypoint, _ := json.Marshal(nonNil(c.Entrypoint))
	cmd, _ := json.Marshal(nonNil(c.Cmd))
	dockerfile := "FROM " + seeded + "\n" +
		"ENTRYPOINT " + string(entrypoint) + "\n" +
		"CMD " + string(cmd) + "\n"
	b.images = append(b.images, b.o.Tag)
	if err := b.dockerBuild(ctx, "final", b.o.Tag, dockerfile); err != nil {
		return err
	}
	final, err := b.inspectConfig(ctx, b.o.Tag)
	if err != nil {
		return err
	}
	if err := checkEnv(final); err != nil {
		return err
	}
	if added := addedProxies(b.top.Env, final.Env); len(added) > 0 {
		return refused(fmt.Errorf(textProxyBaked, printables(added)))
	}
	b.line(textCommitted, seconds(time.Since(at)))
	out, err := b.e.output(ctx, "docker", "image", "inspect", "--format", "{{.Size}}", b.o.Tag)
	if err != nil {
		return err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return fmt.Errorf(textSizeUnread, integration.Printable(strings.TrimSpace(string(out))))
	}
	if size > MaxSize {
		return refused(fmt.Errorf(textTooBig, humanSize(size)))
	}
	b.images = slices.DeleteFunc(b.images, func(s string) bool { return s == b.o.Tag })
	b.res.Target, b.res.Environment, b.environment = b.o.Tag, b.environment, ""
	b.line(textDone, exports.Value(b.o.Tag, false), seconds(time.Since(start)), humanSize(size))
	return nil
}

// nonNil is s, or an empty list for nil, so it is written [] rather than null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// humanSize is a size in MiB or GiB, with one decimal, and in bytes.
func humanSize(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GiB (%d bytes)", float64(n)/(1<<30), n)
	}
	return fmt.Sprintf("%.1f MiB (%d bytes)", float64(n)/(1<<20), n)
}

// clean removes the temporary container, the images the build made, o.Tag and the
// environment image among them when the build made them and then failed, and the
// temporary folder.
func (b *build) clean(ctx context.Context) {
	if b.created {
		b.e.clean(ctx, "rm", "--force", "--volumes", b.name)
	}
	images := slices.Clone(b.images)
	if b.environment != "" {
		images = append(images, b.environment)
	}
	if len(images) > 0 {
		b.e.clean(ctx, append([]string{"image", "rm", "--force"}, images...)...)
	}
	if b.tmp != "" {
		_ = os.RemoveAll(b.tmp)
	}
}

// relay passes the lines written to it on to out, and keeps the last.
type relay struct {
	out  io.Writer
	buf  []byte
	last string
}

// Write passes on each complete line of p.
func (r *relay) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	for {
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			break
		}
		r.emit(string(r.buf[:i]))
		r.buf = r.buf[i+1:]
	}
	return len(p), nil
}

// flush passes on what is left after the last newline.
func (r *relay) flush() {
	if len(r.buf) > 0 {
		r.emit(string(r.buf))
		r.buf = nil
	}
}

// emit passes one line on, printable, and keeps it.
func (r *relay) emit(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	r.last = line
	fmt.Fprintln(r.out, line)
}
