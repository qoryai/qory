package pushcheck

import (
	"errors"
	"fmt"
	"slices"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/provisioner"
)

// TextFilledMember is the first line of the refusal of an output that sets a member that
// qory fills at the push, with the member and the type. The outbox of a session has the
// same line.
const TextFilledMember = "Remove %s from the %s output: qory fills it at the push."

// FilledMember is the check of a POST to the run API against the members that qory fills
// at the push: the members of a type in types that [exports.ProcessType.Marks] has. A push
// of such a type whose file sets one of them is a refusal, for the first member in name
// order, with the line [TextFilledMember]. qory fills these members when it stores the
// outbox from the host, a push that does not pass this check, so a POST from behind the
// wall sets none.
//
// A POST of another path, a push of a type with no such member, a withdrawal with no files
// and a file that is no JSON object pass. With no such member in any of types, every POST
// passes. Otherwise a POST that [readRunAPIPost] cannot read in one way, and a file that
// has a member twice or one that differs from a marked member only in case, are the
// refusals of [unreadPush]. The check never changes the body.
func FilledMember(types []exports.ProcessType, req session.RunAPIRequest) *session.RunAPIRefusal {
	if !slices.ContainsFunc(types, func(t exports.ProcessType) bool { return len(t.Marks) > 0 }) {
		return nil
	}
	post, err := readRunAPIPost(req)
	switch {
	case post.kind != postPush:
		return nil
	case err != nil:
		return unreadPush(err).Refusal()
	}
	i := slices.IndexFunc(types, func(t exports.ProcessType) bool { return t.Name == post.artifactType })
	if i < 0 || len(types[i].Marks) == 0 {
		return nil
	}
	marked := make([]string, 0, len(types[i].Marks))
	for name := range types[i].Marks {
		marked = append(marked, name)
	}
	slices.Sort(marked)
	for _, f := range post.files {
		top, err := members(f.Content, marked)
		if errors.Is(err, errAmbiguous) {
			return unreadPush(err).Refusal()
		}
		if err != nil {
			continue
		}
		for _, name := range marked {
			if _, ok := top[name]; ok {
				line := fmt.Sprintf(TextFilledMember, provisioner.ShownType(exports.Value(name, false)), provisioner.ShownType(types[i].Name))
				return (&Failure{Message: exports.Fit(line), Step: exports.Fit(line)}).Refusal()
			}
		}
	}
	return nil
}
