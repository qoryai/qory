package compose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/module"
)

// roleContract is one role contract a module declares, read.
type roleContract struct {
	// Role is the role, <kind>/<role>.
	Role string
	// Module is the module that declares the contract.
	Module string
	// Path is the contract file's path inside the module, cleaned.
	Path string
	// Contract is the file read.
	Contract module.Contract
}

// checkContracts returns an error for a compose in which an agent that fills a role does
// not meet a contract of the role, see [module.Contract]. It first reads every contract
// that the opened modules declare, in module order and by role, whether or not an agent
// fills the role, so that a contract file that is missing or does not read fails the
// compose of the stack that declares it. It then checks every agent that fills a role
// with a contract: the agent [Result.Bind] sets, or the agent of the role's name when the
// role is not bound. A role that no composed agent fills is rule 4's to refuse. Each
// agent that lacks a part of a contract is one block of the error, by role, then in the
// order of the contracts; the block lists every part it lacks.
func checkContracts(res *Result, opened []openedModule) error {
	var contracts []roleContract
	for _, o := range opened {
		if len(o.m.Contracts) == 0 {
			continue
		}
		read, err := readContracts(o)
		if err != nil {
			return err
		}
		contracts = append(contracts, read...)
	}
	if len(contracts) == 0 {
		return nil
	}
	entries := map[string]Entry{}
	for _, e := range res.Entries {
		entries[e.Kind+"/"+e.Name] = e
	}
	modules := map[string]Module{}
	for _, m := range res.Modules {
		modules[m.Name] = m
	}
	var blocks []string
	roles := []string{}
	for _, c := range contracts {
		if !slices.Contains(roles, c.Role) {
			roles = append(roles, c.Role)
		}
	}
	slices.Sort(roles)
	for _, role := range roles {
		kind, name, _ := strings.Cut(role, "/")
		if to, ok := res.Bind[role]; ok {
			name = to
		}
		e, ok := entries[kind+"/"+name]
		if !ok {
			continue
		}
		m := modules[e.Module]
		readFile := os.ReadFile
		if m.Root != nil {
			readFile = module.ReadThrough(m.Root, m.Dir, m.RealDir)
		}
		data, err := readFile(e.Path)
		if err != nil {
			return exports.HideError(err)
		}
		doc, err := module.ParseDocument(data, e.Path)
		if err != nil {
			return err
		}
		file := e.Path
		if rel, err := filepath.Rel(m.Dir, e.Path); err == nil {
			file = filepath.ToSlash(rel)
		}
		for _, c := range contracts {
			if c.Role != role {
				continue
			}
			missing := lacks(c.Contract, doc, e.Tier)
			if len(missing) == 0 {
				continue
			}
			block := fmt.Sprintf("Complete agent %s of module %s, or bind %s to another agent.\n  The contract %s of module %s requires these parts, which %s lacks:",
				shown(e.Name), shown(e.Module), shownKey(role), exports.Value(c.Path, false), shown(c.Module), exports.Value(file, false))
			for _, part := range missing {
				block += "\n  - " + part
			}
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	return errors.New(strings.Join(blocks, "\n"))
}

// readContracts reads the contracts the opened module o declares, by role, through the
// module's directory opened as a root, so a link that leads out of the module is refused.
func readContracts(o openedModule) ([]roleContract, error) {
	name := o.m.Name
	root := o.root
	if root == nil {
		opened, err := os.OpenRoot(o.src.Dir)
		if err != nil {
			return nil, exports.HideError(err)
		}
		defer opened.Close()
		root = opened
	}
	readFile := func(path string) ([]byte, error) { return root.ReadFile(path) }
	if o.root != nil {
		through := module.ReadThrough(o.root, o.src.Dir, o.realDir)
		readFile = func(path string) ([]byte, error) { return through(filepath.Join(o.src.Dir, path)) }
	}
	var out []roleContract
	for _, role := range sortedKeys(o.m.Contracts) {
		path := o.m.Contracts[role]
		where := exports.Value(filepath.ToSlash(path), false) + " of module " + shown(name)
		shownPath := exports.Value(filepath.ToSlash(path), false)
		at := strings.Replace(role, "/", ".", 1)
		info, err := root.Stat(path)
		switch {
		case leavesRoot(root, err):
			return nil, fmt.Errorf("Replace %s with a file inside the module.\n  It is a link to a place outside the module, and qory reads only the module's files.", where)
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("Add %s to module %s, or remove contract from roles.%s.\n  The module's %s names the file as the contract of %s, and the module has no file at that path.",
				shownPath, shown(name), shown(at), module.ManifestName, shownKey(role))
		case err != nil:
			return nil, exports.HideError(err)
		case info.IsDir():
			return nil, fmt.Errorf("Point the contract of %s in module %s at a file.\n  %s is a directory.", shownKey(role), shown(name), shownPath)
		}
		data, err := readFile(path)
		if leavesRoot(root, err) {
			return nil, fmt.Errorf("Replace %s with a file inside the module.\n  It is a link to a place outside the module, and qory reads only the module's files.", where)
		}
		if err != nil {
			return nil, exports.HideError(err)
		}
		c, err := module.ParseContract(data)
		var ce *module.ContractError
		if errors.As(err, &ce) {
			return nil, errors.New(ce.In(where))
		}
		if err != nil {
			return nil, err
		}
		out = append(out, roleContract{Role: role, Module: name, Path: filepath.ToSlash(path), Contract: c})
	}
	return out, nil
}

// lacks lists the parts of c that the agent doc, whose model sets tier, does not have, in
// the contract's order: the sections, the front-matter keys, the tools, then the tier.
func lacks(c module.Contract, doc module.Document, tier string) []string {
	var out []string
	headings := module.Headings(doc.Body)
	for _, s := range c.Sections {
		if !slices.Contains(headings, s) {
			out = append(out, "the heading "+exports.Value("## "+s, true))
		}
	}
	for _, key := range c.FrontMatter {
		if empty(doc.Front[key]) {
			out = append(out, "the front-matter key "+exports.Value(key, false))
		}
	}
	tools := module.ToolsOf(doc.Front)
	for _, t := range c.Tools {
		if !slices.Contains(tools, t) {
			out = append(out, "the tool "+exports.Value(t, false))
		}
	}
	if c.Tier && tier == "" {
		out = append(out, "a tier in model, such as ${qory:fast}")
	}
	return out
}

// empty reports whether v, a front-matter value, sets nothing: absent, null, an empty or
// blank string, or an empty list or map.
func empty(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

// leavesRoot reports whether err is the error root returns for a path that leads out of
// it, such as a link to a place outside the module. Package os does not export that
// error, so leavesRoot takes it from a lookup of ".." in root, which fails with it
// before any file is read, as exports does for a process.yaml.
func leavesRoot(root *os.Root, err error) bool {
	if err == nil {
		return false
	}
	_, probe := root.Lstat("..")
	if errors.Is(probe, os.ErrClosed) {
		return false
	}
	sentinel := errors.Unwrap(probe)
	return sentinel != nil && errors.Is(err, sentinel)
}
