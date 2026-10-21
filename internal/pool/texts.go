package pool

// The lines the pool program logs. Each is a fixed text; what varies goes in its
// attributes, and every line passes [SafeHandler] before it is written.
const (
	lineClaimed       = "claimed a run"
	lineClaimFailed   = "the claim failed, so it is sent again after a wait"
	lineNotRecorded   = "Qory Apiary did not record the run's start"
	lineHandoverAgain = "the handover is sent again after a wait"
	lineStarted       = "started the run"
	lineNotPosted     = "the run's outcome could not be posted"
	linePosted        = "posted the run's outcome"
	lineReleased      = "released the run"
	lineOverAlready   = "the dispatch was over already, so nothing was released"
	// lineHandoverRefused is a handover Qory Apiary refused with 403 forbidden.
	lineHandoverRefused = "the handover was refused, so nothing was released"
	// lineNotReleased is a release Qory Apiary did not take.
	lineNotReleased = "the run could not be released"
	// lineNotStarted is a run whose start failed before qory run made its record.
	lineNotStarted = "the run could not be started"
	// lineNotJournaled is a write of the journal that failed.
	lineNotJournaled = "the journal of the pool's dispatches could not be written"
)

// The reasons a release gives, display text in Qory Apiary. After the handover of a
// prebuild, Qory Apiary records the reason as the build's failure.
const (
	// ReasonNotStarted is a run that could not be started on this machine.
	ReasonNotStarted = "the run could not be started on this machine"
	// ReasonRestarted is a dispatch the journal held when the pool program started.
	ReasonRestarted = "the pool program restarted before the run ended"
	// ReasonStoppedBeforeStart is a dispatch whose run had not started when the pool
	// program was stopped.
	ReasonStoppedBeforeStart = "the pool program stopped before the run started"
	// ReasonStep is a run of a step the pool program does not run.
	ReasonStep = "the pool program runs only the explore and prebuild steps"
	// ReasonOtherStep is a run handed over for the pool's other step than the one it
	// was claimed for.
	ReasonOtherStep = "the run was handed over for another step than the one it was claimed for"
	// ReasonLeasePassed is a dispatch whose lease ended before its run could start.
	ReasonLeasePassed = "the lease ended before the run could be started"
	// ReasonHandedOver is a run handed over before, whose run key is not given again.
	ReasonHandedOver = "the run was handed over before, so this machine did not start it"
	// ReasonNotHanded is a run whose run key did not reach this machine.
	ReasonNotHanded = "the run key could not be handed over"
	// ReasonPrebuildCap is a prebuild claimed while as many builds as pool.prebuilds
	// allows were going on.
	ReasonPrebuildCap = "this machine is running as many builds as it may at once"
)
