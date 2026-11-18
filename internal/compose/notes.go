package compose

import (
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/exports"
)

// RoleNote is one note of the composed harness, a skill with the frontmatter keys
// notes_for and paths, as the home's role-notes.json lists it.
type RoleNote struct {
	// Name is the skill's name: the note's body is skills/<name>/SKILL.md in the home.
	Name string
	// Roles are the roles the note is for, each <kind>/<role> as [Role.Role] writes it,
	// agents/<role> for the notes_for value <role>, in the order the skill lists them.
	Roles []string
	// Paths are the skill's globs from the checkout root, as written.
	Paths []string
	// Module is the module that ships the skill.
	Module string
	// Layer is 0 for a module of the checkout's qory.yaml, or of the stack composed when it
	// extends none, and 1 for a module of the base stack it extends, which cannot extend
	// another. A lower number is nearer to the checkout.
	Layer int
}

// RoleNotes are the notes of the composed skills, in the order of [Result.Entries];
// nil for none.
func (r *Result) RoleNotes() []RoleNote {
	var out []RoleNote
	for _, e := range r.Entries {
		if e.Kind != "skills" || e.Note == nil {
			continue
		}
		note := RoleNote{Name: e.Name, Paths: e.Note.Paths, Module: e.Module}
		for _, role := range e.Note.Roles {
			note.Roles = append(note.Roles, "agents/"+role)
		}
		if m, ok := moduleOf(r, e.Module); ok && m.Base {
			note.Layer = 1
		}
		out = append(out, note)
	}
	return out
}

// checkNotes returns an error for the first composed note, in the order of the entries,
// that is for a role the stack fills nowhere. The keys of a note are checked when the
// module is read, see [module.Note].
func checkNotes(res *Result) error {
	var filled []string
	for _, r := range res.Roles {
		if kind, role, _ := strings.Cut(r.Role, "/"); kind == "agents" && !r.Replaced && !slices.Contains(filled, role) {
			filled = append(filled, role)
		}
	}
	for _, e := range res.Entries {
		if e.Kind != "skills" || e.Note == nil {
			continue
		}
		for _, role := range e.Note.Roles {
			if slices.Contains(filled, role) {
				continue
			}
			such := ""
			if len(filled) > 0 {
				such = ", such as " + shown(filled[0])
			}
			return exports.Errorf("Set notes_for in %s of module %s to a role the stack fills%s.\n  %s is none.", shown(e.Name), checkoutName(res, e.Module), such, shown(role))
		}
	}
	return nil
}
