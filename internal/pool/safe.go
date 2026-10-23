package pool

import (
	"context"
	"log/slog"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/pool/child"
)

// hiddenText takes the place of a logged message or value that contains what starts a
// secret.
const hiddenText = "[hidden]"

// SafeHandler is h, but a record's message, and each of its values and those of the
// handler's attributes, that contains what starts an access key secret, a run key, a
// run code or an enrolment code, qak_, qrk_, qecr_, qecn_, qecp_, qecg_ or qeci_ in any
// case, as [child.ContainsSecret] checks, is written as [hidden]. A value is checked as
// the handler would write it: a group's members one by one, and any other value by its
// text.
func SafeHandler(h slog.Handler) slog.Handler { return safeHandler{h} }

type safeHandler struct{ h slog.Handler }

// Enabled is h's.
func (s safeHandler) Enabled(ctx context.Context, l slog.Level) bool { return s.h.Enabled(ctx, l) }

// Handle writes r through h, its message and values as [SafeHandler] says.
func (s safeHandler) Handle(ctx context.Context, r slog.Record) error {
	msg := r.Message
	if child.ContainsSecret(msg) {
		msg = hiddenText
	}
	out := slog.NewRecord(r.Time, r.Level, msg, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(safeAttr(a))
		return true
	})
	return s.h.Handle(ctx, out)
}

// WithAttrs is h with attrs, each as [SafeHandler] writes it.
func (s safeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		safe[i] = safeAttr(a)
	}
	return safeHandler{s.h.WithAttrs(safe)}
}

// WithGroup is h with the group name, [hidden] when it contains what starts a secret.
func (s safeHandler) WithGroup(name string) slog.Handler {
	if child.ContainsSecret(name) {
		name = hiddenText
	}
	return safeHandler{s.h.WithGroup(name)}
}

// safeAttr is a with its key and value as [SafeHandler] writes them.
func safeAttr(a slog.Attr) slog.Attr {
	if child.ContainsSecret(a.Key) {
		a.Key = hiddenText
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		members := v.Group()
		safe := make([]slog.Attr, len(members))
		for i, m := range members {
			safe[i] = safeAttr(m)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(safe...)}
	}
	if child.ContainsSecret(v.String()) {
		return slog.String(a.Key, hiddenText)
	}
	return slog.Attr{Key: a.Key, Value: v}
}

// errAttr is the error attribute of a line, its text as [exports.HideError] prints it.
func errAttr(err error) slog.Attr { return slog.String("error", exports.HideError(err).Error()) }
