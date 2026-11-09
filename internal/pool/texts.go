package pool

// The lines the pool program logs. Each is a fixed text, and what varies goes in its
// attributes, except the formats for fmt.Sprintf below; [lineLockNotTried] is the one
// format that has the path of a file in its text. Every line passes [SafeHandler]
// before it is written.
const (
	lineClaimed     = "claimed a run"
	lineClaimFailed = "the claim failed, so it is sent again after a wait"
	lineNotRecorded = "Qory Apiary did not record the run's start"
	lineStarted     = "started qory run for the dispatch"
	// lineHeartbeatFailed is a dispatched, sent while qory run runs, that failed with
	// another error than 410 gone or 409 lease_expired.
	lineHeartbeatFailed = "dispatched failed, so it is sent again after a wait"
	// lineDispatchEnded is a dispatched, sent while qory run runs, that returned 410
	// gone or 409 lease_expired.
	lineDispatchEnded = "the dispatch ended at Qory Apiary, so its qory run was stopped"
	// lineLifetime is a qory run stopped at the claim's max_lifetime_minutes.
	lineLifetime = "the dispatch reached its max_lifetime_minutes, so its qory run was stopped"
	lineEnded    = "qory run for the dispatch ended"
	lineReleased = "released the run"
	// lineOverAlready is a release that returned 410 gone or 409 lease_expired.
	lineOverAlready = "the dispatch was over already, so nothing was released"
	// lineNotReleased is a release that returned 409 machine_exists, or that failed for
	// a claim that could not be written to the journal: the pool does not send it
	// again.
	lineNotReleased = "the run could not be released"
	// lineReleaseKept is a release that failed and was not sent again before the pool
	// stopped; the claimed entry stays in the journal.
	lineReleaseKept = "the release of the run failed before the pool stopped; the pool sends it again at its next start"
	// lineNotStarted is a qory run that could not be started.
	lineNotStarted = "the run could not be started"
	// lineLeftoverStopped is a qory run left from before the pool started whose lock
	// is held, logged once the leader of its process group, the attribute
	// process_group, passed the check of the leader's start that run.pgid records.
	// The pool then sends SIGTERM to the group, and SIGKILL after the grace only while
	// the lock is still held.
	lineLeftoverStopped = "a qory run of the dispatch is left from before the pool started, so the pool stops it"
	// lineLeftoverHeld is a qory run left from before the pool started whose lock is
	// still held after the waits: the entry stays in the journal, and no gone is sent.
	// The attribute path is the lock file's.
	lineLeftoverHeld = "stop the programs that hold run.lock in the dispatch's folder, then restart the pool: a qory run left from before the pool started still holds that lock, so the pool keeps the dispatch for its next start and reports no gone"
	// lineReapFailed is a removal of a left qory run's wall that failed; gone is sent
	// all the same.
	lineReapFailed = "remove the containers and networks with the label dev.qory.run=<run_id> from the container engine: the removal of the wall of a qory run left from before the pool started failed; the pool reports the machine as gone all the same"
	// lineNotJournaled is a write of the journal that failed.
	lineNotJournaled = "the journal of the pool's dispatches could not be written"
	// lineLogUploaded is qory run's output, uploaded as the dispatch's machine log.
	lineLogUploaded = "uploaded qory run's output as the dispatch's machine log"
	// lineLogNotUploaded is a machine log whose upload failed or returned a refusal;
	// the dispatch's end does not change.
	lineLogNotUploaded = "qory run's output could not be uploaded as the dispatch's machine log"
	// lineGone is a machine end gone that got the signed 204.
	lineGone = "reported the machine as gone"
	// lineGoneKept is a machine end gone that failed once the pool was stopping.
	lineGoneKept = "the gone report failed before the pool stopped; the pool sends it again at its next start"
	// lineGone400, lineGone403 and lineGone404 are a machine end gone that got a signed
	// 400, 403 or 404, after which the pool does not send it again. The status's code
	// is in the attribute error.
	lineGone400 = "update qory to the version of this Qory Apiary: Qory Apiary returned 400 for the gone report, so the pool does not send it again"
	lineGone403 = "check this pool's key in Qory Apiary: Qory Apiary returned 403 for the gone report, so the pool does not send it again"
	lineGone404 = "check the server address and update Qory Apiary: Qory Apiary returned 404 for the gone report, so the pool does not send it again"
	// lineRelease400, lineRelease403 and lineRelease404 are a release that got a signed
	// 400, 403 or 404, after which the pool does not send it again. The status's code is
	// in the attribute error.
	lineRelease400 = "update qory to the version of this Qory Apiary: Qory Apiary returned 400 for the release, so the pool does not send it again"
	lineRelease403 = "check this pool's key in Qory Apiary: Qory Apiary returned 403 for the release, so the pool does not send it again"
	lineRelease404 = "check the server address and update Qory Apiary: Qory Apiary returned 404 for the release, so the pool does not send it again"
)

// lineGoneFailed is a machine end gone that failed with no response, an unsigned
// response, a signed 429, a signed 503 unavailable or another server error: a format
// for fmt.Sprintf of the wait before it is sent again, as Go prints a time.Duration. The
// wait is also the attribute wait of the line.
const lineGoneFailed = "the gone report failed; the pool sends it again in %s"

// lineLockNotTried is a lock of a dispatch's folder that the pool could not try, such
// as a run.lock it has no permission to open, or a link: the entry stays in the journal, and no gone
// is sent. It is a format for fmt.Sprintf of the path of run.lock, as
// [exports.HidePath] prints it, and of the error.
const lineLockNotTried = "check `%s`: the pool could not lock it (%s), so it keeps the dispatch for its next start and reports no gone"

// lineReleaseFailed is a release that failed with no response, an unsigned response, a
// fourth signed 429, a server error or another refusal, while the pool runs: a format
// for fmt.Sprintf of the wait before it is sent again, as lineGoneFailed is.
const lineReleaseFailed = "the release of the run failed; the pool sends it again in %s"

// The reasons a release gives, display text in Qory Apiary. Each is 1 to 200
// characters, as release.schema.json requires.
const (
	// ReasonNotStarted is a run that could not be started on this machine.
	ReasonNotStarted = "the run could not be started on this machine"
	// ReasonRestarted is a claimed dispatch the journal contained when the pool program
	// started.
	ReasonRestarted = "the pool program restarted before the run ended"
	// ReasonStoppedBeforeStart is a dispatch whose run had not started when the pool
	// program was stopped.
	ReasonStoppedBeforeStart = "the pool program stopped before the run started"
	// ReasonLeasePassed is a dispatch whose lease ended before its run could start.
	ReasonLeasePassed = "the lease ended before the run could be started"
	// ReasonMachine is a claim whose machine member is set: a qory pool creates no
	// machine.
	ReasonMachine = "this pool creates no machine of the type in the claim"
)
