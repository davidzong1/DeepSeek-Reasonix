package cli

import (
	"testing"
	"time"

	"reasonix/internal/team"
)

func boundRecord(gen uint64) team.BindRecord {
	return team.BindRecord{
		MemberID: "m1", LeaderID: "leader-a", Generation: gen,
		Status: team.BindStatusBound, TaskID: "t1", BoundAt: time.Now(),
	}
}

func TestCalibrateInactiveKeeps(t *testing.T) {
	if got := calibrateTeamSessionBinding(false, boundRecord(1), true, 1); got != calibrationKeep {
		t.Fatalf("inactive session must keep, got %v", got)
	}
}

func TestCalibrateMissingRecordExits(t *testing.T) {
	if got := calibrateTeamSessionBinding(true, team.BindRecord{}, false, 1); got != calibrationExitSession {
		t.Fatalf("no server record must exit the session, got %v", got)
	}
}

func TestCalibrateUnboundRecordExits(t *testing.T) {
	rec := boundRecord(1)
	rec.Status = team.BindStatusUnbound
	if got := calibrateTeamSessionBinding(true, rec, true, 1); got != calibrationExitSession {
		t.Fatalf("server-released member must exit, got %v", got)
	}
}

func TestCalibrateSameGenerationKeeps(t *testing.T) {
	if got := calibrateTeamSessionBinding(true, boundRecord(3), true, 3); got != calibrationKeep {
		t.Fatalf("matching generation must keep, got %v", got)
	}
}

func TestCalibrateNewerGenerationRefreshes(t *testing.T) {
	if got := calibrateTeamSessionBinding(true, boundRecord(4), true, 3); got != calibrationRefreshWindow {
		t.Fatalf("newer generation must refresh, got %v", got)
	}
}

func TestCalibrateTransitioningKeepsEvenIfNewer(t *testing.T) {
	rec := boundRecord(9)
	rec.Status = team.BindStatusTransitioning
	if got := calibrateTeamSessionBinding(true, rec, true, 1); got != calibrationKeep {
		t.Fatalf("in-flight transition must keep, got %v", got)
	}
}
