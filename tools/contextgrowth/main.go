// contextgrowth answers P0 of docs/agent_architecture_decision.md: it attributes
// recorded context growth to mechanical fan-out (a) or exploratory turns (b) and
// applies the pre-registered decision rule. It is a reader; nothing imports it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// absorbGate is §13's pre-registered threshold on the (a) mass.
const absorbGate = 0.80

type verdict struct {
	Sessions, Skipped      int
	ToolBytes, AssistBytes int64
	FanoutBytes            int64
	DelegatedBytes         int64
	Tools                  map[string]int
}

func (v verdict) total() int64 { return v.ToolBytes + v.AssistBytes }

func (v verdict) shareA() float64 {
	if v.total() == 0 {
		return 0
	}
	return float64(v.ToolBytes) / float64(v.total())
}

// delegatedShare is how much of (a) arrived through a delegation tool. A low
// figure is a caveat on the gate's applicability, not on its arithmetic.
func (v verdict) delegatedShare() float64 {
	if v.ToolBytes == 0 {
		return 0
	}
	return float64(v.DelegatedBytes) / float64(v.ToolBytes)
}

func (v verdict) absorbable() float64 {
	if v.ToolBytes == 0 {
		return 0
	}
	return float64(v.FanoutBytes) / float64(v.ToolBytes)
}

// ships applies the rule exactly as §13 fixed it: (a) must dominate and closed
// operators must absorb at least absorbGate of it.
func (v verdict) ships() bool {
	return v.shareA() > 0.5 && v.absorbable() >= absorbGate
}

func main() {
	root := flag.String("root", defaultRoot(), "state root holding projects/<slug>/sessions")
	min := flag.Int("min-messages", 20, "skip transcripts shorter than this")
	// The verdict rests on a corpus, so the check that it does not rest on one
	// file in that corpus belongs in the tool rather than in an ad-hoc script.
	sensitivity := flag.Bool("sensitivity", false, "also report the verdict with the largest-contributing session dropped")
	localOriginal := flag.Bool("local-original", false, "measure the fuller local raw_content instead of the provider-visible content")
	flag.Parse()

	paths, err := transcripts(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contextgrowth: %v\n", err)
		os.Exit(2)
	}
	v := verdict{Tools: map[string]int{}}
	kept := make([]session, 0, len(paths))
	for _, p := range paths {
		s, err := readSession(p, *localOriginal)
		if err != nil || s.Messages < *min {
			v.Skipped++
			continue
		}
		kept = append(kept, s)
		v.Sessions++
		v.add(s)
	}
	report(v, *localOriginal)
	if *sensitivity {
		reportSensitivity(kept)
	}
}

func (v *verdict) add(s session) {
	v.ToolBytes += s.ToolBytes
	v.AssistBytes += s.AssistBytes
	v.FanoutBytes += s.FanoutBytes
	v.DelegatedBytes += s.DelegatedBytes
	for name, n := range s.ToolCounts {
		v.Tools[name] += n
	}
}

// reportSensitivity drops the single largest-contributing session and re-derives
// the verdict. A verdict that flips here rests on one file, whatever its
// percentages looked like.
func reportSensitivity(kept []session) {
	if len(kept) < 2 {
		return
	}
	largest := 0
	for i, s := range kept {
		if s.ToolBytes+s.AssistBytes > kept[largest].ToolBytes+kept[largest].AssistBytes {
			largest = i
		}
	}
	rest := verdict{Tools: map[string]int{}}
	for i, s := range kept {
		if i != largest {
			rest.Sessions++
			rest.add(s)
		}
	}
	big := kept[largest]
	fmt.Printf("\nsensitivity: dropping %s\n", filepath.Base(big.Path))
	fmt.Printf("  it carries              %10d B of %d attributed in %d sessions\n",
		big.ToolBytes+big.AssistBytes,
		sum(kept), len(kept))
	fmt.Printf("  absorbable without it   %10.1f%% -> %s\n",
		100*rest.absorbable(), verdictWord(rest))
}

func sum(sessions []session) int64 {
	var out int64
	for _, s := range sessions {
		out += s.ToolBytes + s.AssistBytes
	}
	return out
}

func verdictWord(v verdict) string {
	if v.ships() {
		return "P3 ships"
	}
	return "P3 CANCELLED"
}

func report(v verdict, localOriginal bool) {
	measure := "provider-visible content"
	if localOriginal {
		measure = "local raw_content"
	}
	fmt.Printf("sample: %d sessions read, %d skipped, %.1f MiB attributed (%s)\n",
		v.Sessions, v.Skipped, float64(v.total())/(1<<20), measure)
	if v.Sessions == 0 || v.total() == 0 {
		fmt.Println("verdict: NO DATA - the rule cannot be applied")
		os.Exit(1)
	}
	fmt.Printf("(a) tool-result growth      %10d B  %5.1f%%\n", v.ToolBytes, 100*v.shareA())
	fmt.Printf("(b) assistant-turn growth   %10d B  %5.1f%%\n", v.AssistBytes, 100*(1-v.shareA()))
	fmt.Printf("    of (a), fan-out shaped  %10d B  %5.1f%% (gate %.0f%%)\n",
		v.FanoutBytes, 100*v.absorbable(), 100*absorbGate)
	// Reported apart from the gate because it asks a different question: the
	// gate asks whether an operator could reach the growth, this asks whether
	// the workload contains the fan-out the gate was written about at all.
	fmt.Printf("    reached via delegation  %10d B  %5.1f%% of (a)\n",
		v.DelegatedBytes, 100*v.delegatedShare())
	fmt.Println()
	names := make([]string, 0, len(v.Tools))
	for n := range v.Tools {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return v.Tools[names[i]] > v.Tools[names[j]] })
	fmt.Print("top tools by call count:")
	for i, n := range names {
		if i == 6 {
			break
		}
		fmt.Printf(" %s=%d", n, v.Tools[n])
	}
	fmt.Println()
	if v.ships() {
		fmt.Printf("verdict: (a) DOMINANT and absorbable -> P3 ships\n")
		return
	}
	fmt.Printf("verdict: P3 CANCELLED - ")
	if v.shareA() <= 0.5 {
		fmt.Printf("(b) dominates at %.1f%%\n", 100*(1-v.shareA()))
		return
	}
	fmt.Printf("(a) dominates but only %.1f%% is absorbable\n", 100*v.absorbable())
}

func transcripts(root string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "projects", "*", "sessions", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		// .events.jsonl is the event log, not the message transcript.
		if filepath.Ext(m) == ".jsonl" && !hasSuffix(m, ".events.jsonl") {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out, nil
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func defaultRoot() string {
	if v := os.Getenv("REASONIX_STATE_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("REASONIX_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".reasonix"
	}
	return filepath.Join(home, ".reasonix")
}
