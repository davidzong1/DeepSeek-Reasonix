package agent

import (
	"reasonix/internal/completion"
	"reasonix/internal/evidence"
)

// orchestrationCompletion adjudicates one plan run. Upstream's turn report is
// deliberately facts-only — the model judges completion and the host must not
// invent a quality verdict — but a plan is the host's own decomposition, so its
// report answers to the plan contract: a run that wrote to the workspace and
// verified nothing must not read as done.
func orchestrationCompletion(ledger *evidence.Ledger) completion.Report {
	rep := completion.BuildFacts(ledger, "", nil)
	proven := false
	for _, v := range rep.Verifications {
		if v.Passed && !v.Stale {
			proven = true
		}
	}
	if rep.Mutations > 0 && !proven {
		rep.Gaps = append(rep.Gaps, completion.Gap{
			Kind:   completion.GapUnverifiedChange,
			Detail: "no verification passed after the latest change",
		})
	}
	switch {
	case rep.Mutations == 0 && len(rep.Verifications) == 0 && len(rep.Gaps) == 0:
		rep.Verdict = completion.VerdictUnknown
	case len(rep.Gaps) > 0:
		rep.Verdict = completion.VerdictPartial
	default:
		rep.Verdict = completion.VerdictDone
	}
	return rep
}
