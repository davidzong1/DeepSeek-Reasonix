package cli

import (
	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/telemetry"
)

// webHandoff is what the post-TUI teardown reports back: whether the session
// asked to continue in the Web UI, and the identity it must resume as.
type webHandoff struct {
	launch    bool
	path      string
	sessionID string
	modelRef  string
}

// closeAfterTUI releases everything the chat session held, in the one order that
// is safe once bubbletea has given the terminal back. Controller.Close runs
// SessionEnd hooks and kills plugin subprocesses, which corrupts raw mode if done
// while the TUI is alive — that is why retired controllers were stashed rather
// than closed at /model switch time, and why team member backends are closed here
// and not in exitTeam. fallback covers a final model that is not a chatTUI.
func closeAfterTUI(final tea.Model, fallback *control.Controller, reporter *telemetry.Reporter) webHandoff {
	fm, ok := final.(chatTUI)
	if !ok {
		reporter.RecordRecovery(fallback.DrainRecoveryMetrics())
		fallback.Close()
		return webHandoff{}
	}
	// Team resources first: this hands the window's controller identity back to
	// the chat's own backend, so the ctrl.Close() below cannot land a second time
	// on a member controller the registry just closed.
	fm.closeTeamResources()
	for _, oc := range fm.oldControllers {
		if c, ok := oc.(*control.Controller); ok {
			reporter.RecordRecovery(c.DrainRecoveryMetrics())
		}
		oc.Close()
	}
	if fm.ctrl == nil {
		reporter.RecordRecovery(fallback.DrainRecoveryMetrics())
		fallback.Close()
		return webHandoff{launch: fm.launchWebOnExit}
	}
	if c, ok := fm.ctrl.(*control.Controller); ok {
		reporter.RecordRecovery(c.DrainRecoveryMetrics())
	}
	fm.ctrl.Close()
	return webHandoff{
		launch:    fm.launchWebOnExit,
		path:      fm.launchWebResumePath,
		sessionID: fm.launchWebSessionID,
		modelRef:  fm.launchWebModelRef,
	}
}
