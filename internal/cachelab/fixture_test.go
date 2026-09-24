package cachelab

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFixtureIsDeterministicPerSpec(t *testing.T) {
	spec := FixtureSpec{Nonce: "run-1", Marker: "CACHELAB-ACK", Bytes: LadderSmallBytes}
	first := BuildFixture(spec)
	second := BuildFixture(spec)
	if !bytes.Equal(first.Canonical(), second.Canonical()) {
		t.Fatal("the same spec must produce the same frozen bytes")
	}
	if first.Digest() != second.Digest() {
		t.Fatalf("digests %s and %s differ for one spec", first.Digest(), second.Digest())
	}
	other := BuildFixture(FixtureSpec{Nonce: "run-2", Marker: "CACHELAB-ACK", Bytes: LadderSmallBytes})
	if other.Digest() == first.Digest() {
		t.Fatal("a different run nonce must produce different bytes, so the first request is a cold start")
	}
}

func TestFixtureLadderGrowsWithTheRegisteredSize(t *testing.T) {
	sizes := []int{LadderSmallBytes, LadderMidBytes, LadderLargeBytes}
	last := 0
	for _, size := range sizes {
		f := BuildFixture(FixtureSpec{Nonce: "run", Bytes: size})
		total := len(f.System) + len(f.User)
		if total < size {
			t.Fatalf("fixture of %d bytes produced %d", size, total)
		}
		if total <= last {
			t.Fatalf("fixture size %d did not grow past %d", total, last)
		}
		last = total
	}
}

func TestComponentPerturbationMovesExactlyOneComponent(t *testing.T) {
	base := BuildFixture(FixtureSpec{Nonce: "run", Bytes: LadderSmallBytes})
	if base.Component != "" {
		t.Fatalf("component = %q, want a baseline fixture", base.Component)
	}
	toolField := BuildFixture(FixtureSpec{Nonce: "run", Bytes: LadderSmallBytes, Component: ComponentToolSchemaField})
	if toolField.Digest() == base.Digest() {
		t.Fatal("the tool-schema perturbation must change the frozen bytes")
	}
	if toolField.System != base.System {
		t.Fatal("the tool-schema perturbation must not touch the system prompt")
	}
	if toolField.Tools[0].Description == base.Tools[0].Description {
		t.Fatal("the tool-schema perturbation must change the tool description")
	}

	systemTail := BuildFixture(FixtureSpec{Nonce: "run", Bytes: LadderSmallBytes, Component: ComponentSystemTail})
	if systemTail.System == base.System || len(systemTail.SystemTail) == 0 {
		t.Fatal("the system-tail perturbation must append to the system prompt")
	}
	if systemTail.Tools[0].Description != base.Tools[0].Description {
		t.Fatal("the system-tail perturbation must not touch the tool surface")
	}

	keyOrder := BuildFixture(FixtureSpec{Nonce: "run", Bytes: LadderSmallBytes, Component: ComponentSchemaKeyOrder})
	if keyOrder.Digest() == base.Digest() {
		t.Fatal("re-serializing one schema must change the frozen bytes")
	}
	if !sameJSONObject(t, keyOrder.Tools[1].Parameters, base.Tools[1].Parameters) {
		t.Fatal("the serialization perturbation must not change the schema's meaning")
	}
	if string(keyOrder.Tools[1].Parameters) == string(base.Tools[1].Parameters) {
		t.Fatal("the serialization perturbation must change the schema's bytes")
	}

	unregistered := BuildFixture(FixtureSpec{Nonce: "run", Bytes: LadderSmallBytes, Component: "two_things_at_once"})
	if err := unregistered.Validate(); err == nil {
		t.Fatal("an unregistered perturbation must be refused")
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("the baseline fixture must validate: %v", err)
	}
}

func TestFixtureGuardLiteralsDescribeItsOwnText(t *testing.T) {
	f := BuildFixture(FixtureSpec{Nonce: "run", Marker: "CACHELAB-ACK"})
	literals := f.GuardLiterals()
	if len(literals) == 0 {
		t.Fatal("a fixture must name what must never reach the journal")
	}
	text := f.System + f.User
	for _, literal := range literals {
		if len(literal) < 8 {
			t.Fatalf("guard literal %q is too short to be useful", literal)
		}
		if !strings.Contains(text, literal) {
			t.Fatalf("guard literal %q is not part of the fixture", literal)
		}
	}
}

func TestRegisteredArmsAreComplete(t *testing.T) {
	arms := RegisteredArms()
	if len(arms) == 0 {
		t.Fatal("no arms are registered")
	}
	seen := map[string]bool{}
	for _, arm := range arms {
		if err := arm.Validate(); err != nil {
			t.Fatalf("registration of %s is incomplete: %v", arm.ID, err)
		}
		if seen[arm.ID] {
			t.Fatalf("arm %s is registered twice", arm.ID)
		}
		seen[arm.ID] = true
	}
	for _, want := range []string{"B0-pilot", "B1-baseline-repeat", "B2-interval-short", "B3-tool-schema", "B4-ladder-large"} {
		if !seen[want] {
			t.Fatalf("arm %s is not registered", want)
		}
	}
	if _, err := ArmByID("B9-invented"); err == nil {
		t.Fatal("an unregistered arm id must be refused")
	}
	gated, err := ArmByID("B5-route-or-account")
	if err != nil {
		t.Fatal(err)
	}
	if !gated.Gated || gated.Gate == "" {
		t.Fatalf("arm %s changes the route and must carry a gate", gated.ID)
	}
}

// decodeJSON renders a JSON document canonically, so two spellings of the same
// object compare equal.
func decodeJSON(raw []byte) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	return string(canonical), err
}

// sameJSONObject compares two JSON documents by meaning rather than by bytes.
func sameJSONObject(t *testing.T, a, b []byte) bool {
	t.Helper()
	decodedA, err := decodeJSON(a)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	decodedB, err := decodeJSON(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return decodedA == decodedB
}
