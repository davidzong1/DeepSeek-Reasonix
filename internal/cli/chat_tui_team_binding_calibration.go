package cli

import (
	"reasonix/internal/team"
)

// teamBindingCalibration is the team session window's reaction to the
// server's persisted BindRecord for the bound member (route §4.3).
type teamBindingCalibration int

const (
	calibrationKeep teamBindingCalibration = iota
	// calibrationExitSession forces the window out of the session: the
	// server released the member or never bound it, so the local binding
	// is stale and must not stay (route §4.3: trust the server record).
	calibrationExitSession
	// calibrationRefreshWindow reloads the member's context: the server's
	// generation is newer than the one this window last saw, so its
	// seen-set and L0/L2 views belong to the previous window.
	calibrationRefreshWindow
)

// calibrateTeamSessionBinding decides how an active team session reacts to
// the server's persisted BindRecord, instead of trusting local guesswork
// (route §4.3). A released or never-bound member exits the session; a newer
// generation means the window was replaced and must reload; an in-flight
// transition changes nothing — the server decides when it settles.
// localGen is the generation this window last observed for the member.
func calibrateTeamSessionBinding(active bool, rec team.BindRecord, ok bool, localGen uint64) teamBindingCalibration {
	if !active {
		return calibrationKeep
	}
	if !ok || rec.Status == team.BindStatusUnbound {
		return calibrationExitSession
	}
	if rec.Status == team.BindStatusTransitioning {
		return calibrationKeep
	}
	if rec.Generation > localGen {
		return calibrationRefreshWindow
	}
	return calibrationKeep
}
