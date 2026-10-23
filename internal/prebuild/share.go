package prebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/devcontainer"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/image"
	"github.com/qoryai/qory/internal/integration"
)

// The words of the two steps. Each is one format string.
const (
	textUpstream    = prefix + "rewrote %s to the registry's copy, %s"
	textPulledBase  = prefix + "pulled the base image %s in %s"
	textNoBase      = prefix + "no base image to report: the Dockerfile starts FROM scratch, or from an image whose name or platform is known only during the build"
	textStandard    = prefix + "pulled the standard image %s in %s; the environment step adds nothing to it"
	textFromEnv     = prefix + "pulled the environment image %s in %s; the environment step is skipped"
	textKept        = prefix + "kept the environment image as %s"
	textNotKept     = prefix + "kept no separate environment image, since %s; the target image contains the whole build"
	textNotRegistry = "give the build the registry's address as a host name, with a port or none: %q is not one"
	textNotCredHost = "give the build each host that has a credential as a host name: %q is not one"
	textNotEnvImage = "give the environment image as name@sha256:<64 hexadecimal digits>: %q is not in that form"
	textNotEnvTag   = "give the build a name for the environment image with no digest: %q is not one"
	textNotPulled   = "check that the Docker engine reaches the registry of %s: docker pull exited with an error: %s"
	textNoManifest  = "check that %s is in its registry: docker pull found no manifest for it: %s"
	textNoDigest    = "check that %s is in a registry: docker image inspect printed no registry digest for it"
	textPulledAsks  = "build from an image whose devcontainer.metadata label sets no access to the host: %s sets %s"
	textPulledLabel = "build from an image whose devcontainer.metadata label is valid JSON: the label of %s is not"
	textPulledEnv   = "build from an image whose environment does not set %s, since these variables change what the agent does: %s sets them"
)

// The reasons in [textNotKept].
const (
	reasonVariable   = "a build member uses ${localEnv:…}, ${env:…}, ${localWorkspaceFolder…} or ${devcontainerId}"
	reasonFeature    = "a local feature is outside .devcontainer/"
	reasonDockerfile = "the Dockerfile is outside .devcontainer/"
	reasonContext    = "the build context is outside .devcontainer/"
)

// hostVariables start the variables of the host, which the devcontainer command gives
// values from the machine it runs on or from the target's folder there: ${localEnv:…}
// and ${env:…}, its earlier name, ${localWorkspaceFolder…}, and ${devcontainerId}, which
// it derives from the folder.
var hostVariables = []string{"${localEnv:", "${env:", "${localWorkspaceFolder", "${devcontainerId}"}

// definitionFolder is the folder whose content is a definition another target can share.
const definitionFolder = ".devcontainer"

var (
	// registryAddress is the registry's address: a host name, and a port or none.
	registryAddress = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?$`)
	// platformShape is a FROM's --platform written out: os/architecture, and a variant
	// or none.
	platformShape = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(?:/[a-z0-9]+)?$`)
	// digestPart is the digest of a reference by digest, @ before it.
	digestPart = regexp.MustCompile(`^@sha256:[0-9a-f]{64}$`)
)

// source is what the target step starts from.
type source int

const (
	// fromBuild is the image the environment step builds from the definition.
	fromBuild source = iota
	// fromStandard is a standard image, shared/<name>, that is the definition's image,
	// with nothing to build on it.
	fromStandard
	// fromEnvironment is [Options.EnvironmentImage].
	fromEnvironment
)

// checkSteps refuses the options of the two steps no image is built from, and sets the
// build's [upstream].
func (b *build) checkSteps() error {
	o := b.o
	if o.Registry != "" && !registryAddress.MatchString(o.Registry) {
		return refused(fmt.Errorf(textNotRegistry, shownValue(o.Registry)))
	}
	var hosts []string
	for _, h := range o.CredentialHosts {
		if !registryAddress.MatchString(strings.ToLower(h)) {
			return refused(fmt.Errorf(textNotCredHost, shownValue(h)))
		}
		hosts = append(hosts, hostOf(h))
	}
	if o.EnvironmentImage != "" && !digestRef.MatchString(o.EnvironmentImage) {
		return refused(fmt.Errorf(textNotEnvImage, shownValue(o.EnvironmentImage)))
	}
	if t := o.EnvironmentTag; t != "" && (image.CheckRef(t) != nil || strings.Contains(t, "@")) {
		return refused(fmt.Errorf(textNotEnvTag, shownValue(t)))
	}
	b.up = upstream{registry: o.Registry, credentialed: hosts}
	return nil
}

// shownValue is v as an error prints it, printable, and with a secret in it hidden.
func shownValue(v string) string {
	return integration.Printable(exports.Value(v, false))
}

// chooseSource sets what the target step starts from and, for an image the environment
// step builds, whether the build keeps it apart, by the sharing rule: a definition of
// the image form, or one whose Dockerfile and build context are in .devcontainer/,
// with no local feature outside .devcontainer/, and none of [hostVariables] in image,
// build or features. The environment image then
// depends on its bytes alone, so other targets with the same definition can start from
// it.
func (b *build) chooseSource() {
	c := b.cfg
	imageForm := c.Build == nil || c.Build.Dockerfile == ""
	switch {
	case b.o.EnvironmentImage != "":
		b.source = fromEnvironment
		return
	case b.o.Registry != "" && imageForm && len(c.Features) == 0 && strings.HasPrefix(c.Image, b.o.Registry+"/shared/"):
		b.source = fromStandard
		return
	}
	b.share, b.notShared = true, ""
	strs := []string{c.Image}
	for _, m := range b.set {
		if m.Name == "build" || m.Name == "features" {
			strs = append(strs, jsonStrings(m.Value)...)
		}
	}
	for _, s := range strs {
		if slices.ContainsFunc(hostVariables, func(v string) bool { return strings.Contains(s, v) }) {
			b.share, b.notShared = false, reasonVariable
			return
		}
	}
	folder := path.Dir(c.Path)
	for _, id := range c.Features {
		if (strings.HasPrefix(id, "./") || strings.HasPrefix(id, "../")) && !b.inDefinition(path.Join(folder, id)) {
			b.share, b.notShared = false, reasonFeature
			return
		}
	}
	if imageForm {
		return
	}
	context := path.Dir(b.dockerfile)
	if c.Build.Context != "" {
		context = path.Join(folder, c.Build.Context)
	}
	switch {
	case !b.inDefinition(b.dockerfile):
		b.share, b.notShared = false, reasonDockerfile
	case !b.inDefinition(context):
		b.share, b.notShared = false, reasonContext
	}
}

// inDefinition reports whether rel, a path relative to the repository with slashes, is
// [definitionFolder] or in it, as written and with its links followed, as [inside] has
// it.
func (b *build) inDefinition(rel string) bool {
	rel = path.Clean(rel)
	sub := "."
	if rel != definitionFolder {
		var ok bool
		if sub, ok = strings.CutPrefix(rel, definitionFolder+"/"); !ok {
			return false
		}
	}
	in, err := inside(filepath.Join(b.repo, definitionFolder), sub)
	return err == nil && in
}

// jsonStrings are the strings of raw, a JSON value: each member's name and each string,
// at any depth.
func jsonStrings(raw json.RawMessage) []string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for k, e := range v {
				out = append(out, k)
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// rewrite sets the edits of the definition's copy that the devcontainer command reads:
// image and, in a copy of the Dockerfile, the images of FROM and COPY --from, as
// [upstream] has them. Feature IDs stay as written: the devcontainer command orders
// Features by installsAfter on the whole ID, registry host included, and fetches
// dependsOn by the ID as written, so a rewritten ID would drop an ordering. It prints a line for each reference it rewrites. The
// target's files stay as they are.
func (b *build) rewrite() error {
	if b.up.registry == "" {
		return nil
	}
	var done [][2]string
	note := func(from, to string) {
		if from != to && !slices.Contains(done, [2]string{from, to}) {
			done = append(done, [2]string{from, to})
		}
	}
	if b.cfg.Image != "" {
		note(b.cfg.Image, b.up.image(b.cfg.Image))
	}
	b.edit.Image = b.up.image
	if b.dockerfile != "" {
		data, err := readDockerfile(b.repo, b.dockerfile)
		if err != nil {
			return refused(fmt.Errorf(textDockerfileUnread, b.cfg.Path, integration.Printable(exports.Value(b.dockerfile, false)), integration.Printable(exports.HideError(err).Error())))
		}
		out, rewrites := b.up.rewriteDockerfile(data)
		b.dockerfileData = out
		if len(rewrites) > 0 {
			if b.edit.Dockerfile, err = b.writeDockerfile(out); err != nil {
				return err
			}
		}
		for _, r := range rewrites {
			note(r[0], r[1])
		}
	}
	for _, r := range done {
		b.line(textUpstream, integration.Printable(exports.Value(r[0], false)), integration.Printable(exports.Value(r[1], false)))
	}
	b.rewritten = len(done) > 0
	return nil
}

// writeDockerfile writes data, the Dockerfile rewritten, to a folder of the temporary
// folder under the Dockerfile's name, and the Dockerfile's <name>.dockerignore beside it
// when the repository has one, which BuildKit reads in place of the context's
// .dockerignore. It returns the copy's path.
func (b *build) writeDockerfile(data []byte) (string, error) {
	dir := filepath.Join(b.tmp, "dockerfile")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	copied := filepath.Join(dir, path.Base(b.dockerfile))
	if err := os.WriteFile(copied, data, 0o600); err != nil {
		return "", err
	}
	if ignore, err := readDockerfile(b.repo, b.dockerfile+".dockerignore"); err == nil {
		if err := os.WriteFile(copied+".dockerignore", ignore, 0o600); err != nil {
			return "", err
		}
	}
	return copied, nil
}

// baseRef is the image the definition's build starts from, after [build.rewrite]: image,
// or the image the Dockerfile's last stage, or build.target, starts from, as
// [dockerfileBase] has it, and the --platform of that image's FROM, "" for none. ok is
// false when there is none the build can read.
func (b *build) baseRef() (ref, platform string, ok bool) {
	ref = b.up.image(b.cfg.Image)
	if b.dockerfile != "" {
		data := b.dockerfileData
		if data == nil {
			var err error
			if data, err = readDockerfile(b.repo, b.dockerfile); err != nil {
				return "", "", false
			}
		}
		if ref, platform, ok = dockerfileBase(data, b.cfg.Build.Target, b.cfg.Build.Args, b.engine); !ok {
			return "", "", false
		}
	}
	if _, ok := parseRef(ref); !ok || image.CheckRef(ref) != nil || strings.Contains(ref, "${") {
		return "", "", false
	}
	if platform != "" && !platformShape.MatchString(platform) {
		return "", "", false
	}
	return ref, platform, true
}

// resolveBase sets the result's base to the definition's base image by digest: pinned
// in the definition, or pulled, which prints a line. It prints a line when there is no
// base image to report. A pulled image stays on the engine, as BuildKit's cache keeps
// the images a build pulls.
func (b *build) resolveBase(ctx context.Context) error {
	ref, platform, ok := b.baseRef()
	if !ok {
		b.line(textNoBase)
		return nil
	}
	if pinned, ok := pinnedRef(ref); ok {
		b.res.Base = pinned
		return nil
	}
	start := time.Now()
	digest, err := b.pull(ctx, ref, platform)
	if err != nil {
		return err
	}
	b.res.Base = digest
	b.line(textPulledBase, integration.Printable(exports.Value(digest, false)), seconds(time.Since(start)))
	return nil
}

// pinnedRef is ref as name@sha256:digest when ref is by digest.
func pinnedRef(ref string) (string, bool) {
	r, ok := parseRef(ref)
	if !ok || !digestPart.MatchString(r.digest()) {
		return "", false
	}
	name, _, _ := cutName(ref)
	return name + r.digest(), true
}

// pull pulls ref with docker pull, for platform when it is not "", its output to the
// log, and returns it by digest: pinned, or as docker image inspect's RepoDigests has it
// for ref's name.
func (b *build) pull(ctx context.Context, ref, platform string) (string, error) {
	shown := integration.Printable(exports.Value(ref, false))
	args := []string{"pull"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	if err := b.e.run(ctx, nil, b.o.Log, "docker", append(args, ref)...); err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		why, missing := err.Error(), false
		if f, ok := errors.AsType[*failed](err); ok {
			if f.last != "" {
				why = f.last
			}
			missing = imageMissing(f.raw)
		}
		if missing {
			return "", &notFoundError{err: fmt.Errorf(textNoManifest, shown, integration.Printable(why))}
		}
		return "", fmt.Errorf(textNotPulled, shown, integration.Printable(why))
	}
	if pinned, ok := pinnedRef(ref); ok {
		return pinned, nil
	}
	out, err := b.e.output(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", ref)
	if err != nil {
		return "", err
	}
	want, _ := parseRef(ref)
	var digests []string
	_ = json.Unmarshal(bytes.TrimSpace(out), &digests)
	for _, d := range digests {
		name, digest, _ := strings.Cut(d, "@")
		if r, ok := parseRef(name); ok && r.name() == want.name() && digestPart.MatchString("@"+digest) {
			return d, nil
		}
	}
	return "", fmt.Errorf(textNoDigest, shown)
}

// Docker's lines for an image its registry does not have, from Docker 26 to 29: the
// registry has no manifest for the tag or the digest, or no repository of the name;
// with the containerd image store, the reference resolves to nothing. A denied pull,
// an unreachable registry and a timeout match none of them.
var (
	manifestUnknown = regexp.MustCompile(`manifest for \S+ not found: manifest unknown: `)
	nameUnknown     = regexp.MustCompile(`repository \S+ not found: name unknown: `)
	notResolved     = regexp.MustCompile(`(?m)failed to resolve reference "([^"\s]+)": (\S+): not found$`)
)

// imageMissing reports whether out, the last line docker pull wrote on its standard
// error, is a pull of an image its registry does not have. The containerd line counts only with
// the same reference twice: another reference is a part of the image, not the image.
func imageMissing(out string) bool {
	if manifestUnknown.MatchString(out) || nameUnknown.MatchString(out) {
		return true
	}
	for _, m := range notResolved.FindAllStringSubmatch(out, -1) {
		if m[1] == m[2] {
			return true
		}
	}
	return false
}

// notFoundError is a pull of an image its registry does not have. It is
// [ErrImageMissing] once env is set, for the environment image alone.
type notFoundError struct {
	err error
	env bool
}

// Error is the pull's text.
func (e *notFoundError) Error() string { return e.err.Error() }

// Unwrap is the pull's error.
func (e *notFoundError) Unwrap() error { return e.err }

// Is reports whether target is [ErrImageMissing], for the environment image.
func (e *notFoundError) Is(target error) bool { return e.env && target == ErrImageMissing }

// takePulled pulls ref, the image the target step starts from, checks it as
// [build.checkLabel] checks a built image, prints the line format, with the image by
// digest and how long it took, and returns the image by digest.
func (b *build) takePulled(ctx context.Context, ref, format string) (string, error) {
	start := time.Now()
	digest, err := b.pull(ctx, ref, "")
	if err != nil {
		return "", err
	}
	if err := b.checkPulled(ctx, digest); err != nil {
		return "", err
	}
	b.line(format, integration.Printable(exports.Value(digest, false)), seconds(time.Since(start)))
	return digest, nil
}

// checkPulled refuses a pulled image as [build.checkLabel] refuses a built one, with
// texts that contain the image's reference. It removes nothing: the engine keeps what
// it pulled.
func (b *build) checkPulled(ctx context.Context, ref string) error {
	shown := integration.Printable(exports.Value(ref, false))
	c, err := b.inspectConfig(ctx, ref)
	if err != nil {
		return err
	}
	if widening := envWidening(c); len(widening) > 0 {
		return refused(fmt.Errorf(textPulledEnv, printables(widening), shown))
	}
	b.homeSet = homeSet(c.Env)
	asked, ok := b.labelAsks(c)
	switch {
	case !ok:
		return refused(fmt.Errorf(textPulledLabel, shown))
	case len(asked) > 0:
		return refused(fmt.Errorf(textPulledAsks, shown, list(asked)))
	}
	return nil
}

// keepEnvironment tags base, the environment step's image, as the environment image
// the build keeps, when the sharing rule allows, and prints a line either way.
func (b *build) keepEnvironment(ctx context.Context, base string) error {
	if !b.share {
		b.line(textNotKept, b.notShared)
		return nil
	}
	tag := b.o.EnvironmentTag
	if tag == "" {
		tag = b.name + ":environment"
	}
	b.environment = tag
	if err := b.e.run(ctx, nil, b.o.Log, "docker", "tag", base, tag); err != nil {
		return err
	}
	b.line(textKept, integration.Printable(exports.Value(tag, false)))
	return nil
}

// edits is the definition's copy as [build.rewrite] set it, without initializeCommand
// when the configuration has it.
func (b *build) edits() devcontainer.Edit {
	e := b.edit
	if b.cfg.Has(initializeMember) {
		e.Drop = []string{initializeMember}
	}
	return e
}
