package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

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

// maxImageRefusalDetail is the most runes of Forager's detail that qory run prints in
// the line of image_access_denied, image_rate_limited or image_registry_unreachable. The
// detail contains the image's reference, the host of the registry or token realm, and
// for image_rate_limited a Retry-After value, so it passes the 200 runes of
// [integration.Printable]. For registry.example.com/team/app@sha256:<64 hex>, it is
// 390 runes for a token realm's 429 and 418 for a certificate error. A 502 from the
// run's proxy adds the first line of its body, at most 512 bytes, for 906 runes.
const maxImageRefusalDetail = 1000

// lowerFirst is s with its first rune in lower case.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// fetchImage fetches the image of a walled run before its first join, as the run's image
// step: [session.Run.FetchImage], which writes no step for an image the machine
// defines, checks the image's manifest at its registry through the run's proxy, and
// pulls the image, also one the engine holds by its digest. With proxy, the pull goes
// through the run's proxy.
//
// A refusal of the image is a [*reasonError] with Forager's code as a [*runLineError],
// and the line ends with Forager's detail and the code in parentheses:
//
//   - image_missing and image_user_root: [textImageMissing] or [textImageUserRoot] puts
//     qory's fix before the detail. For a run from a run code, fromCode, the line ends
//     with [codeUsed].
//   - image_access_denied, image_rate_limited and image_registry_unreachable: the line is
//     Forager's detail with its first letter in lower case, as qory's lines start, cut
//     at [maxImageRefusalDetail] runes in place of 200; the detail starts with the fix.
//     For a run from a run code, the line ends with [codeUsedStartStage].
//
// Any other error is a [*runLineError] of [textNoImage], or [textNoImageFromCode] for a
// run from a run code. Each text has proxy's password, and every secret, hidden, and
// contains Forager's text alone as its cause, so it unwraps to no error of Forager's.
func fetchImage(ctx context.Context, run openedRun, proxy *session.EngineProxy, fromCode bool) error {
	err := run.FetchImage(ctx)
	if err == nil {
		return nil
	}
	hidden := func(text string) string {
		if proxy != nil && proxy.Password != "" {
			text = strings.ReplaceAll(text, proxy.Password, "<hidden>")
		}
		return exports.HideValues(text)
	}
	shown := func(text string) string { return integration.Printable(hidden(text)) }
	var ref *session.Refusal
	if errors.As(err, &ref) {
		detail := ref.Detail
		if detail == "" {
			detail = ref.Error()
		}
		line := ""
		switch ref.Code {
		case refusal.ImageMissing, refusal.ImageUserRoot:
			text := textImageMissing
			if ref.Code == refusal.ImageUserRoot {
				text = textImageUserRoot
			}
			line = fmt.Sprintf(text, shown(detail)+" ("+ref.Code+")")
			if fromCode {
				line += codeUsed
			}
		case refusal.ImageAccessDenied, refusal.ImageRateLimited, refusal.ImageRegistryUnreachable:
			line = lowerFirst(integration.PrintableWithin(hidden(detail), maxImageRefusalDetail)) + " (" + ref.Code + ")"
			if fromCode {
				line += codeUsedStartStage
			}
		}
		if line != "" {
			return &runLineError{&reasonError{code: ref.Code, text: line}}
		}
	}
	if fromCode {
		return &runLineError{fmt.Errorf(textNoImageFromCode, shown(err.Error()))}
	}
	return &runLineError{fmt.Errorf(textNoImage, shown(err.Error()))}
}
