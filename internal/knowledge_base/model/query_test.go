package model

import "testing"

func TestEffectiveLimit(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 20}, {1, 1}, {20, 20}, {50, 50}, {51, 50}, {-5, 20},
	}
	for _, tt := range tests {
		if got := (Query{Limit: tt.in}).EffectiveLimit(); got != tt.want {
			t.Errorf("EffectiveLimit(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestItemContentHashL1(t *testing.T) {
	a := validItem()
	b := a
	c := validItem()
	c.Body += " extra"

	if ItemContentHash(a) != ItemContentHash(b) {
		t.Error("L1 hash must be stable for identical items")
	}
	if ItemContentHash(a) == ItemContentHash(c) {
		t.Error("L1 hash must change with body")
	}
}

func TestRetireReasonClosed(t *testing.T) {
	for _, r := range []RetireReason{ReasonNoLongerTrue, ReasonSupersededByTomb, ReasonPersonal} {
		if !r.Valid() {
			t.Errorf("whitelisted reason %q must be valid", r)
		}
	}
	for _, r := range []RetireReason{"", "expired", "low_quality", "clear_team"} {
		if r.Valid() {
			t.Errorf("non-whitelisted reason %q must be rejected (fail-closed)", r)
		}
	}
}
