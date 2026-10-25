package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// textImageMissing is the line of a walled run whose image its registry does not have,
// with Forager's detail after it.
const textImageMissing = "push the image to its registry, or select another image in the run's policy: %s"

// textImageUserRoot is the line of a walled run whose image runs as root, or as a user
// that cannot be checked for root, with Forager's detail after it.
const textImageUserRoot = "set USER in the image to a user other than root that its /etc/passwd lists, or select another image in the run's policy: %s"

// textNoImage is the line of a walled run whose image's fetch failed, with Forager's
// error after it.
const textNoImage = "run again: the fetch of the run's image failed: %s"

// textNoImageFromCode is [textNoImage] for a run from a run code. The code is used by
// then, so the run starts again only from the new run code that taking the work item in
// again makes.
const textNoImageFromCode = "take the work item in again in Qory Apiary: the fetch of the run's image failed: %s" + codeUsed

// imageRefusedError is Forager's refusal of a walled run's image at its fetch,
// image_missing or image_user_root: qory run closes the run with code as its reason.
type imageRefusedError struct{ code, text string }

func (e *imageRefusedError) Error() string { return e.text }

// fetchImage fetches the image of a walled run before its first join, as the run's image
// step: [session.Run.FetchImage], which writes no step for an image the machine
// defines, and pulls no image the engine holds by its digest. With proxy, the pull goes
// through the run's proxy. A refusal of the image, image_missing or image_user_root, is
// an [*imageRefusedError] as a [*runLineError]; any other error is a [*runLineError] of
// [textNoImage], or [textNoImageFromCode] for a run from a run code, fromCode, whose
// refusal lines end with [codeUsed] too. Each text has proxy's password, and every
// secret, hidden, and contains Forager's text alone as its cause, so it unwraps to no
// error of Forager's.
func fetchImage(ctx context.Context, run openedRun, proxy *session.EngineProxy, fromCode bool) error {
	err := run.FetchImage(ctx)
	if err == nil {
		return nil
	}
	shown := func(text string) string {
		if proxy != nil && proxy.Password != "" {
			text = strings.ReplaceAll(text, proxy.Password, "<hidden>")
		}
		return integration.Printable(exports.HideValues(text))
	}
	var ref *session.Refusal
	if errors.As(err, &ref) && (ref.Code == refusal.ImageMissing || ref.Code == refusal.ImageUserRoot) {
		text := textImageMissing
		if ref.Code == refusal.ImageUserRoot {
			text = textImageUserRoot
		}
		detail := ref.Detail
		if detail == "" {
			detail = ref.Error()
		}
		line := fmt.Sprintf(text, shown(detail)+" ("+ref.Code+")")
		if fromCode {
			line += codeUsed
		}
		return &runLineError{&imageRefusedError{code: ref.Code, text: line}}
	}
	if fromCode {
		return &runLineError{fmt.Errorf(textNoImageFromCode, shown(err.Error()))}
	}
	return &runLineError{fmt.Errorf(textNoImage, shown(err.Error()))}
}
