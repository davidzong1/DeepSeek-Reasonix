package team

import "testing"

// TestTransitionTaskEdges pins the single state-migration map (§2.6): every
// legal edge passes, every move outside the map is rejected. The runtime,
// scheduler and report path share this map, so a rejected edge here is a
// contract violation, not a caller bug.
func TestTransitionTaskEdges(t *testing.T) {
	legal := [][2]TaskStatus{
		{TaskStatusCreated, TaskStatusAssigned},
		{TaskStatusAssigned, TaskStatusRunning},
		{TaskStatusAssigned, TaskStatusCanceled},
		{TaskStatusRunning, TaskStatusReported},
		{TaskStatusRunning, TaskStatusFailed},
		{TaskStatusRunning, TaskStatusCanceled},
		{TaskStatusReported, TaskStatusArchived},
		{TaskStatusFailed, TaskStatusAssigned}, // re-dispatch after crash/refusal
		{TaskStatusRunning, TaskStatusRunning}, // resume keeps running
	}
	for _, e := range legal {
		if err := TransitionTask(e[0], e[1]); err != nil {
			t.Errorf("TransitionTask(%s -> %s) should be legal: %v", e[0], e[1], err)
		}
	}
}

func TestTransitionTaskRejectsIllegalEdges(t *testing.T) {
	illegal := [][2]TaskStatus{
		{TaskStatusCreated, TaskStatusRunning}, // must pass assigned
		{TaskStatusCreated, TaskStatusArchived},
		{TaskStatusAssigned, TaskStatusReported}, // must run first
		{TaskStatusRunning, TaskStatusArchived},  // must report first
		{TaskStatusReported, TaskStatusRunning},  // reported is terminal until archived
		{TaskStatusArchived, TaskStatusCreated},  // archived is terminal
		{TaskStatusFailed, TaskStatusCreated},
		{TaskStatusCanceled, TaskStatusRunning},
	}
	for _, e := range illegal {
		if err := TransitionTask(e[0], e[1]); err == nil {
			t.Errorf("TransitionTask(%s -> %s) must be rejected", e[0], e[1])
		}
	}
}
