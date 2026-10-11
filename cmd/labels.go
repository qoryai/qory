package cmd

import (
	"errors"
	"fmt"
	"maps"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/foragerdir"
)

// A run against the server, directly or through gateway.server.via, keeps the labels
// the checkout's origin remote gives, forge and repository, from the checkout's first
// run the server accepts, beside forager.yaml ([foragerdir.LabelsDir]). The server
// chooses a run's policy by its labels, and an agent can rewrite .git/config, so a later
// run whose origin remote gives others does not start until it is run with --relabel. A
// label given with --label is used as given: it is neither checked nor kept.

// originKeys are the labels qory takes from the checkout's origin remote, in the order a
// line names them.
var originKeys = []string{"forge", "repository"}

// codeLabelsChanged is the refusal of a run whose checkout's origin remote gives other
// labels than the ones kept for the checkout.
const codeLabelsChanged = "labels_changed"

// relabelFlag is qory run's flag that runs although the origin remote gives other
// labels, and keeps the new ones.
const relabelFlag = "relabel"

// relabelUsage is the help of qory run's --relabel.
const relabelUsage = "run although the checkout's origin remote gives other labels (forge, repository) than at its first run against the server, and keep the new ones"

// relabelLocal is the refusal of --relabel with --local, as input.
func relabelLocal() error {
	return input(errors.New("--" + relabelFlag + " keeps a checkout's new labels for the server, and --local contacts none: leave out --" + relabelFlag))
}

// relabelRunKey is the refusal of --relabel with a run key, as input.
func relabelRunKey() error {
	return input(errors.New("--" + relabelFlag + " keeps a checkout's new labels, and " + runKeyGiven + ": the run's starter chose its labels; leave out --" + relabelFlag))
}

// keptLabels is what a run against the server keeps of its checkout's labels, once the
// server accepts the run.
type keptLabels struct {
	dir  foragerdir.Dir
	root string
	// labels are what is kept: the ones kept before, with the origin remote's in place
	// of them for every key not given with --label.
	labels foragerdir.Labels
	// write says labels differ from what is kept, or nothing is kept, or --relabel.
	write, before, relabel bool
}

// checkLabels compares the labels the origin remote of the checkout at root gives with
// the ones dir keeps for it, every key but those explicit gives, before anything starts
// or registers. Another value of a kept key is the labels_changed refusal, as input,
// unless relabel. It returns what to keep once the server accepts the run; nil when
// there is nothing to keep, or no directory of forager.yaml.
func checkLabels(dir foragerdir.Dir, root string, explicit map[string]string, relabel bool) (*keptLabels, error) {
	if dir == "" {
		return nil, nil
	}
	forge, repository, _ := checkout.Origin(root)
	origin := foragerdir.Labels{"forge": forge, "repository": repository}
	derived := foragerdir.Labels{}
	for _, k := range originKeys {
		if _, given := explicit[k]; !given {
			derived[k] = origin[k]
		}
	}
	if len(derived) == 0 {
		return nil, nil
	}
	held, err := dir.ReadLabels(root)
	before := err == nil
	if errors.Is(err, foragerdir.ErrNoLabels) {
		held, err = foragerdir.Labels{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !relabel {
		for k, v := range derived {
			if old, ok := held[k]; ok && old != v {
				return nil, labelsChanged(root, held, origin)
			}
		}
	}
	merged := maps.Clone(held)
	maps.Copy(merged, derived)
	write := relabel || !before || !maps.Equal(merged, held)
	return &keptLabels{dir: dir, root: root, labels: merged, write: write, before: before, relabel: relabel}, nil
}

// labelsChanged is the labels_changed refusal of the checkout at root, whose kept labels
// are held and whose origin remote now gives origin. A key that is not kept reads as the
// origin remote gives it.
func labelsChanged(root string, held, origin foragerdir.Labels) error {
	first := maps.Clone(origin)
	maps.Copy(first, held)
	return input(&refusedError{
		text: fmt.Sprintf("the checkout %s first ran against the server as %s, and its origin remote now gives %s; the server chooses a run's policy by its labels, so the run does not start: if you changed the remote, run the same command with --%s", root, labelWords(first), labelWords(origin), relabelFlag),
		err:  &session.Refusal{Code: codeLabelsChanged},
	})
}

// labelWords names a checkout's labels in a line: "forge <f>, repository <r>", "no
// forge" or "no repository" for a key without a value.
func labelWords(l foragerdir.Labels) string {
	out := ""
	for i, k := range originKeys {
		if i > 0 {
			out += ", "
		}
		if v := l[k]; v != "" {
			out += k + " " + v
		} else {
			out += "no " + k
		}
	}
	return out
}

// keep keeps the labels, once the server accepted the run, and says so on report after
// --relabel. A file that cannot be written is said on report, and the run goes on.
func (k *keptLabels) keep(report func(string)) {
	if k == nil || !k.write {
		return
	}
	if err := k.dir.WriteLabels(k.root, k.labels); err != nil {
		if k.before {
			report(fmt.Sprintf("the checkout's labels were not kept: %v; the next run checks the ones kept before", err))
		} else {
			report(fmt.Sprintf("the checkout's labels were not kept: %v; the next run checks none", err))
		}
		return
	}
	if k.relabel {
		report("the checkout's labels are now " + labelWords(k.labels))
	}
}
