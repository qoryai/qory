package pool

// The lines the pool program logs. Each is a fixed text; what varies goes in its
// attributes, and every line passes [SafeHandler] before it is written.
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
	// lineNotReleased is a release Qory Apiary did not take.
	lineNotReleased = "the run could not be released"
	// lineNotStarted is a qory run that could not be started.
	lineNotStarted = "the run could not be started"
	// lineNotJournaled is a write of the journal that failed.
	lineNotJournaled = "the journal of the pool's dispatches could not be written"
	// lineLogUploaded is qory run's output, uploaded as the dispatch's machine log.
	lineLogUploaded = "uploaded qory run's output as the dispatch's machine log"
	// lineLogNotUploaded is a machine log whose upload failed or returned a refusal;
	// the dispatch's end does not change.
	lineLogNotUploaded = "qory run's output could not be uploaded as the dispatch's machine log"
)

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
