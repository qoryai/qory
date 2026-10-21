package prebuild

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/devcontainer"
)

// textOutside is a build member that names a place outside the repository: the
// configuration's path and the member.
const textOutside = "%s sets %s to a place outside the repository; the image is built only from the repository's files"

// textUnresolved is a build member whose path goes through a link that does not
// resolve, such as one that leads nowhere: the configuration's path and the member.
const textUnresolved = "%s sets %s to a path through a link that does not resolve; the build cannot tell whether it stays in the repository, so it takes no such path"

// textPathVariable is a build member that holds a variable: the configuration's path and
// the member.
const textPathVariable = "%s uses ${ in %s; devcontainer build gives a variable there a value the build cannot check, so it takes the Dockerfile and the context only as paths written out"

// confine refuses a configuration whose build.dockerfile or build.context names a place
// outside the repository at repo: an absolute path, or one that leaves the repository
// as written or once its links are followed. The reference implementation confines
// neither, and docker build reads whatever the context holds.
//
// Each is taken as written. One that holds ${ is refused: the reference implementation
// replaces ${containerWorkspaceFolder} and ${containerWorkspaceFolderBasename} there
// with workspaceFolder as the configuration writes it, and leaves ${containerEnv:…} as
// it is (spec-common/variableSubstitution.ts, replaceWithContext;
// spec-node/configContainer.ts), so the path it builds from is not one this check can
// see. The rest is resolved as the reference implementation resolves it, with
// path.posix.resolve from the configuration's folder
// (spec-configuration/configuration.ts, resolveConfigFilePath): the Dockerfile from that
// folder, and the context from that folder, or, when it is left out, the Dockerfile's
// folder (spec-node/utils.ts, getDockerContextPath). It returns the Dockerfile's path
// relative to repo, with slashes, "" when there is none.
func confine(repo string, c *devcontainer.Config) (string, error) {
	if c.Build == nil {
		return "", nil
	}
	folder := path.Dir(filepath.ToSlash(c.Path))
	dockerfile, context := c.Build.Dockerfile, c.Build.Context
	for _, m := range [][2]string{{"build.dockerfile", dockerfile}, {"build.context", context}} {
		if strings.Contains(m[1], "${") {
			return "", refused(fmt.Errorf(textPathVariable, c.Path, m[0]))
		}
	}
	type place struct{ member, rel string }
	var places []place
	if dockerfile != "" {
		places = append(places, place{"build.dockerfile", path.Join(folder, dockerfile)})
	}
	switch {
	case context != "":
		places = append(places, place{"build.context", path.Join(folder, context)})
	case dockerfile != "":
		places = append(places, place{"build.context", path.Dir(path.Join(folder, dockerfile))})
	}
	for _, p := range places {
		raw := dockerfile
		if p.member == "build.context" {
			raw = context
		}
		if path.IsAbs(raw) || strings.HasPrefix(raw, `\`) {
			return "", refused(fmt.Errorf(textOutside, c.Path, p.member))
		}
		switch inside, err := inside(repo, p.rel); {
		case err != nil:
			return "", refused(fmt.Errorf(textUnresolved, c.Path, p.member))
		case !inside:
			return "", refused(fmt.Errorf(textOutside, c.Path, p.member))
		}
	}
	if dockerfile == "" {
		return "", nil
	}
	return path.Join(folder, dockerfile), nil
}

// inside reports whether rel, a path relative to repo with slashes, stays in repo: as
// written, and with every link on its way followed as far as the path exists. It
// returns an error when something on the way is there and does not resolve.
func inside(repo, rel string) (bool, error) {
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return false, nil
	}
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return false, err
	}
	resolved, err := evalExisting(filepath.Join(real, filepath.FromSlash(rel)))
	if err != nil {
		return false, err
	}
	r, err := filepath.Rel(real, resolved)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r), nil
}

// evalExisting is p with the links of its longest existing part followed, and the rest,
// which is not there, as written: a Dockerfile not there still has its folder's links
// followed.
func evalExisting(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err == nil {
		return resolved, nil
	}
	// What is there and does not resolve, such as a link that leads nowhere, is
	// refused; only what is not there at all is taken as written.
	if _, lerr := os.Lstat(p); !errors.Is(lerr, fs.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(p)
	if parent == p {
		return "", err
	}
	head, err := evalExisting(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(head, filepath.Base(p)), nil
}
