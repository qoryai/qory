package compose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

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
// with a contract: each member of a group role, in the group's order, the agent
// [Result.Bind] sets for a single role, or the agent of the role's name when the role is
// not bound. A member that the bind replaced fills nothing and is not checked. A role
// that no composed agent fills is rule 4's to refuse. Each agent that lacks a part of a
// contract is one block of the error, by role, then by agent, then in the order of the
// contracts; the block lists every part it lacks.
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
		fillers := []string{name}
		members, group := res.Groups[role]
		if group {
			fillers = members
		} else if to, ok := res.Bind[role]; ok {
			fillers = []string{to}
		}
		for _, filler := range fillers {
			e, ok := entries[kind+"/"+filler]
			if !ok {
				continue
			}
			var member *Role
			if group {
				member = memberRole(res, role, filler)
			}
			agentBlocks, err := checkAgent(res, e, modules[e.Module], role, member, contracts)
			if err != nil {
				return err
			}
			blocks = append(blocks, agentBlocks...)
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	return errors.New(strings.Join(blocks, "\n"))
}

// memberRole is the member of the group role that fills it with the entry named member,
// with each list that adds it. A member no list adds is set by the bind of the document
// composed.
func memberRole(res *Result, role, member string) *Role {
	for _, r := range res.Roles {
		if r.Role == role && r.Name == member && !r.Replaced {
			return &r
		}
	}
	return &Role{Role: role, Name: member, From: "bind", Group: true}
}

// fromBaseOnly reports whether the bind of the base stack a checkout extends is the one
// list that adds r, a member of a group role.
func fromBaseOnly(res *Result, r *Role) bool {
	return res.Base != nil && r.From == "base" && len(r.Also) == 0
}

// fromBaseAndMore reports whether the bind of the base stack a checkout extends adds r, a
// member of a group role, and one or more modules' provides add it too.
func fromBaseAndMore(res *Result, r *Role) bool {
	return res.Base != nil && r.From == "base" && len(r.Also) > 0
}

// excludable reports whether the checkout's qory.yaml can exclude an agent of m: any
// agent of a module qory.yaml adds, and an agent of a base module that qory.yaml can list
// when the base lets an extending module ship agents.
func excludable(res *Result, m Module) bool {
	if !m.Base {
		return true
	}
	return m.BaseName != "" && res.Base != nil && res.Base.Extending != nil && slices.Contains(res.Base.Extending.Kinds, "agents")
}

// listsOf is each list that adds r, a member of a group role: the list that set it, then
// those of [Role.Also].
func listsOf(res *Result, r Role) []string {
	lists := []string{listOf(res, r)}
	for _, also := range r.Also {
		also.Role = r.Role
		lists = append(lists, listOf(res, also))
	}
	return lists
}

// listOf is the list that set r, a member of a group role, as rule 4's texts write it,
// see [memberLists].
func listOf(res *Result, r Role) string {
	kind, name, _ := strings.Cut(r.Role, "/")
	switch {
	case r.Module != "":
		return "provides." + shown(kind) + "." + shown(name) + " of module " + shown(r.Module)
	case r.From == "base":
		return "bind." + shown(kind) + "." + shown(name) + " in the base stack"
	}
	return "bind." + shown(kind) + "." + shown(name) + " in " + docFile(res.Stack)
}

// addedBy is the fix and the items of a member of a group role that lists add, for a
// fix line that starts with lead: for one list, "remove it from <list>" and no items; for
// several, "remove it from the lists that add it" and one item line per list, each
// "  - <list>". When lead and one list make a fix line over 100 characters, the list
// moves to an item line, after "remove it from the list that adds it". many reports
// whether the items are those of several lists.
func addedBy(lead string, lists []string) (fix, items string, many bool) {
	if len(lists) == 1 && fits(lead+"remove it from "+lists[0]+".") {
		return "remove it from " + lists[0], "", false
	}
	items = itemLines(lists)
	if len(lists) == 1 {
		return "remove it from the list that adds it", items, false
	}
	return "remove it from the lists that add it", items, true
}

// itemLines is lists, each on an item line "  - <list>", each line after a newline.
func itemLines(lists []string) string {
	out := ""
	for _, l := range lists {
		out += "\n  - " + l
	}
	return out
}

// fits reports whether line, the first line of a refusal, has at most 100 characters.
func fits(line string) bool { return utf8.RuneCountInString(line) <= 100 }

// checkAgent is a block for each contract of role that the agent e, of the composed
// module m, lacks a part of. member is nil for a single role, and for a group role the
// member e fills, see [memberHead].
func checkAgent(res *Result, e Entry, m Module, role string, member *Role, contracts []roleContract) ([]string, error) {
	var blocks []string
	readFile := os.ReadFile
	if m.Root != nil {
		readFile = module.ReadThrough(m.Root, m.Dir, m.RealDir)
	}
	data, err := readFile(e.Path)
	if err != nil {
		return nil, exports.HideError(err)
	}
	doc, err := module.ParseDocument(data, e.Path)
	if err != nil {
		return nil, err
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
		contract := exports.Value(c.Path, false) + " of module " + shown(c.Module)
		block := fmt.Sprintf("Complete agent %s of module %s, or bind %s to another agent.\n  The contract %s requires these parts, which %s lacks:",
			shown(e.Name), shown(e.Module), shownKey(role), contract, exports.Value(file, false))
		tail := ""
		if member != nil {
			block, tail = memberHead(res, e, m, *member, contract, exports.Value(file, false)+" of module "+shown(e.Module))
		}
		for _, part := range missing {
			block += "\n  - " + part
		}
		blocks = append(blocks, block+tail)
	}
	return blocks, nil
}

// memberHead is the fix and the cause line of a block of the agent e, of module m, that
// lacks parts of contract and fills r, a member of a group role; tail is what follows the
// parts. agent is e's file as the cause line names it.
//
// A member that lists the reader edits add gets the fix that removes it from those
// lists: from the one list, or from the lists that add it with a line per list. A member
// that only the bind of the base stack adds, of an agent of a base module, gets an
// exclude in the checkout's qory.yaml when the base allows one, see [excludable], or the
// request to the base stack's maintainer, and the cause line names the base stack and
// the contract. A member that the base's bind and a module's provides add gets the
// exclude, or the change to the agent where the reader can make it, and the lists after
// the parts.
func memberHead(res *Result, e Entry, m Module, r Role, contract, agent string) (head, tail string) {
	name := shown(e.Name)
	cause := fmt.Sprintf("  The contract %s requires these parts, which %s lacks:", contract, agent)
	key, in := shownKey(e.Kind+"/"+e.Name), " in "+docFile(res.Stack)
	exclude := "Exclude " + key + in
	switch {
	case fromBaseOnly(res, &r) && m.Base:
		bound := name + " of module " + shown(e.Module)
		fix := "Ask the base stack's maintainer to complete agent " + name + "."
		if excludable(res, m) {
			fix = "Exclude " + key + " of module " + shown(e.Module) + in + ", or ask the base stack's maintainer to complete it."
			if fits(fix) {
				bound = name
			} else {
				fix = exclude + ", or ask the base stack's maintainer to complete it."
			}
		}
		return fmt.Sprintf("%s\n  The base stack %s binds %s to %s, and the agent lacks these parts of the contract %s:", fix, res.Base, bound, shownKey(r.Role), contract), ""
	case fromBaseAndMore(res, &r):
		fix := "Ask the base stack's maintainer to complete agent " + name + "."
		switch {
		case !m.Base:
			fix = exclude + ", or complete it in module " + shown(e.Module) + "."
			if !fits(fix) {
				fix = exclude + ", or complete it."
			}
		case excludable(res, m):
			fix = exclude + ", or ask the base stack's maintainer to complete it."
		}
		return fix + "\n" + cause, "\n  These lists add it:" + itemLines(listsOf(res, r))
	}
	lead := "Complete agent " + name + ", or "
	remove, items, many := addedBy(lead, listsOf(res, r))
	head = lead + remove + "."
	switch {
	case many:
		head += "\n  These lists add it:" + items
	case items != "":
		head += "\n  This list adds it:" + items
	}
	return head + "\n" + cause, ""
}

// readContracts reads the contracts the opened module o declares, by role, through the
// module's directory opened as a root. For a module read through a root, see
// [Module.Root], each link on a contract's path is followed as [module.ResolveIn] follows
// it, so an absolute link that stays inside the module is read. For any other module,
// root follows only relative links. A link that leads out of the module is refused, and so
// is an absolute link that root does not follow.
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
	var out []roleContract
	for _, role := range sortedKeys(o.m.Contracts) {
		path := o.m.Contracts[role]
		shownPath := exports.Value(filepath.ToSlash(path), false)
		where := shownPath + " of module " + shown(name)
		linked := fmt.Errorf("Replace %s with a file inside the module.\n  It is an absolute link, or a link that leads out of the module, and qory reads only the module's files.", where)
		inner, err := path, error(nil)
		if o.root != nil {
			inner, err = module.ResolveIn(o.root, o.realDir, path)
			// A lookup on the path fails with a *fs.PathError, which exits 1 as a read
			// error does elsewhere; only a path that leads out of the module is refused.
			var pathErr *fs.PathError
			switch {
			case errors.Is(err, fs.ErrNotExist):
			case errors.As(err, &pathErr):
				return nil, exports.HideError(err)
			case errors.Is(err, module.ErrLeadsOut):
				return nil, linked
			case errors.Is(err, module.ErrTooManyLinks):
				return nil, fmt.Errorf("Replace %s with a file inside the module.\n  The path passes through more than 40 links.", where)
			case err != nil:
				return nil, err
			}
		}
		var info fs.FileInfo
		if err == nil {
			info, err = root.Stat(inner)
		}
		switch {
		case leavesRoot(root, err):
			return nil, linked
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("Add %s to module %s, or remove contract from roles.%s.\n  %s sets roles.%s.contract to %s, and the module has no file at that path.",
				shownPath, shown(name), shown(strings.Replace(role, "/", ".", 1)), module.ManifestName, shown(strings.Replace(role, "/", ".", 1)), shownPath)
		case err != nil:
			return nil, exports.HideError(err)
		case info.IsDir():
			return nil, fmt.Errorf("Point the contract of %s in module %s at a file.\n  %s is a directory.", shownKey(role), shown(name), shownPath)
		}
		data, err := root.ReadFile(inner)
		if leavesRoot(root, err) {
			return nil, linked
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
