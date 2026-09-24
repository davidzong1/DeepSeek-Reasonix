package cachereason

import (
	"slices"
	"testing"
)

// TestVocabularyIsTotal pins the shape every consumer relies on: every declared
// value resolves to a kind, and Values lists exactly those values.
func TestVocabularyIsTotal(t *testing.T) {
	values := Values()
	if len(values) == 0 {
		t.Fatal("the vocabulary is empty, which cannot be right")
	}
	if !slices.IsSorted(values) {
		t.Fatalf("Values must be sorted for a reproducible report, got %v", values)
	}
	if len(slices.Compact(slices.Clone(values))) != len(values) {
		t.Fatalf("Values repeats a value: %v", values)
	}
	for _, value := range values {
		if value == "" {
			t.Fatal("an empty reason value would be indistinguishable from no reason")
		}
		kind, ok := KindOf(value)
		if !ok {
			t.Fatalf("%q is listed but has no kind", value)
		}
		if kind != Structural && kind != Rewrite {
			t.Fatalf("%q has unknown kind %q", value, kind)
		}
	}
}

// TestKindOfRefusesTheUnknown pins the direction the whole vocabulary exists for:
// a value nobody declared is reported as unknown rather than placed in the
// nearest kind, because a rewrite reported as an unexplained change sends the
// reader to the wrong fix.
func TestKindOfRefusesTheUnknown(t *testing.T) {
	for _, value := range []string{"", "snip", "log_rewrite", "compact-auto", "COMPACT_AUTO"} {
		if kind, ok := KindOf(value); ok {
			t.Fatalf("KindOf(%q) = (%q, true), want it refused", value, kind)
		}
	}
}

// TestBothKindsArePopulated guards the classifier's two branches: a vocabulary
// that lost its structural half would make every framing change look like a
// fold-boundary candidate.
func TestBothKindsArePopulated(t *testing.T) {
	counts := map[Kind]int{}
	for _, value := range Values() {
		kind, _ := KindOf(value)
		counts[kind]++
	}
	for _, kind := range []Kind{Structural, Rewrite} {
		if counts[kind] == 0 {
			t.Fatalf("kind %q has no values; the vocabulary would classify nothing as it", kind)
		}
	}
}
