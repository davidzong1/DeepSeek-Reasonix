package shellsafe

import (
	"strings"
)

// classifySed classifies a sed invocation from its static argv. The stream is a
// filter over however many input files, so a script with no file- or exec-side
// effects is a permission-safe reader — but only once the script body itself is
// statically scanned: several sed commands write files (w/W), read files into
// the stream (r/R), or execute a shell command (e), and -i edits files in
// place, so none of those are readers. -f/--file points at a script whose body
// cannot be inspected here and fails closed. The sed grammar is lexed, not
// regex-matched, so a 'w', 'e', or 'r' inside a pattern, address, replacement,
// or label never trips the denial.
func classifySed(args []string) CommandEffect {
	family := "sed"
	var scripts []string
	expectedScript := false
	operands := 0
	probe := false
	noPrint := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if !expectedScript && len(scripts) == 0 && operands == 0 && i+1 < len(args) {
				scripts = append(scripts, args[i+1])
				operands += len(args) - i - 2
			} else {
				operands += len(args) - i - 1
			}
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if !expectedScript && len(scripts) == 0 && operands == 0 {
				// First non-option argument is the script when no -e/-f appears.
				scripts = append(scripts, a)
			} else {
				operands++
			}
			continue
		}
		if strings.HasPrefix(a, "--") {
			switch {
			case a == "--expression":
				i++
				if i >= len(args) {
					return unknownEffect(family, "missing sed expression")
				}
				noPrint = true // -e is explicit; the script itself is checked below
				scripts = append(scripts, args[i])
			case strings.HasPrefix(a, "--expression="):
				noPrint = true
				scripts = append(scripts, strings.TrimPrefix(a, "--expression="))
			case a == "--file" || strings.HasPrefix(a, "--file="):
				return unknownEffect(family, "sed script file effects are not statically known")
			case a == "--in-place" || strings.HasPrefix(a, "--in-place="):
				return sedInPlaceWriter()
			case a == "--quiet" || a == "--silent":
				noPrint = true
			case a == "--posix" || a == "--regexp-extended" || a == "--separate" ||
				a == "--unbuffered" || a == "--null-data" || a == "--help" || a == "--version":
				probe = probe || a == "--help" || a == "--version"
			default:
				return unknownEffect(family, "unknown sed option")
			}
			continue
		}
		// Bundled short options, e.g. -ne 'expr' or -i.bak.
		for k := 1; k < len(a); k++ {
			switch a[k] {
			case 'n':
				noPrint = true
			case 'E', 'r', 's', 'u', 'z':
				// Read-only mode flags.
			case 'e':
				noPrint = true // -e is explicit; the script itself is checked below
				if k+1 < len(a) {
					scripts = append(scripts, a[k+1:])
					k = len(a)
				} else {
					i++
					if i >= len(args) {
						return unknownEffect(family, "missing sed expression")
					}
					scripts = append(scripts, args[i])
				}
			case 'f':
				return unknownEffect(family, "sed script file effects are not statically known")
			case 'i', 'I':
				return sedInPlaceWriter()
			default:
				return unknownEffect(family, "unknown sed option")
			}
		}
	}
	if probe && len(scripts) == 0 {
		return knownReader(family)
	}
	if len(scripts) == 0 {
		return unknownEffect(family, "no sed script")
	}
	for _, script := range scripts {
		effect, ok := sedScriptClean(script)
		if !ok {
			return effect
		}
	}
	if !noPrint {
		return unknownEffect(family, "sed without -n not provably read-only")
	}
	return knownReader(family)
}

func sedInPlaceWriter() CommandEffect {
	return knownWriter("sed", WriteWorkspaceContent, "edits files in place")
}

// sedScriptClean lexes one sed script and reports whether it is provably free
// of file and process side effects. Scripts consist of addresses, ranges, and
// commands separated by ';' or newlines; each command is checked against a
// whitelist of pure in-memory/output commands. Anything unparsable or outside
// the whitelist fails closed.
func sedScriptClean(script string) (CommandEffect, bool) {
	i, n := 0, len(script)
	for i < n {
		switch script[i] {
		case ' ', '\t', '\n', ';':
			i++
			continue
		case '#':
			for i < n && script[i] != '\n' {
				i++
			}
			continue
		}
		if !sedConsumeAddress(script, &i) {
			return unknownEffect("sed", "unparsable sed script"), false
		}
		if i >= n {
			return unknownEffect("sed", "missing sed command"), false
		}
		cmd := script[i]
		i++
		switch cmd {
		case 's':
			var effect CommandEffect
			var ok bool
			if i, effect, ok = sedConsumeSubstitution(script, i); !ok {
				return effect, false
			}
		case 'y':
			var ok bool
			if i, ok = sedConsumeTwoPart(script, i); !ok {
				return unknownEffect("sed", "unparsable sed transliteration"), false
			}
		case 'p', 'P', 'd', 'D', 'q', 'Q', 'n', 'N', 'g', 'G', 'h', 'H', 'x', '=', 'l', 'z', 'Z', 'F':
			// Pure in-memory buffer or output commands.
		case 'b', 't', 'T', ':':
			i = sedConsumeLabel(script, i)
		case 'w', 'W':
			return knownWriter("sed", WriteWorkspaceContent, "sed script writes a file"), false
		case 'r', 'R':
			return unknownEffect("sed", "sed script reads a file into the stream"), false
		case 'e':
			return unknownCodeEffect("sed", "sed script executes a nested command"), false
		default:
			return unknownEffect("sed", "unsupported sed script command"), false
		}
	}
	return knownReader("sed"), true
}

// sedConsumeAddress consumes an optional address and range prefix: a number,
// '$', '/regex/', '\c', optionally a second address ('addr,addr', 'addr,+n',
// 'addr,~n') and a trailing '!'. A bare command position (no address) succeeds
// with i unchanged.
func sedConsumeAddress(s string, i *int) bool {
	j := *i
	if !sedAddressAtom(s, &j) {
		return false
	}
	if j < len(s) && s[j] == ',' {
		k := j + 1
		if !sedAddressAtom(s, &k) {
			return false
		}
		j = k
	} else if j < len(s) && (s[j] == '+' || s[j] == '~') {
		j++
		for j < len(s) && isSedDigit(s[j]) {
			j++
		}
	}
	if j < len(s) && s[j] == '!' {
		j++
	}
	*i = j
	return true
}

func sedAddressAtom(s string, i *int) bool {
	j := *i
	if j >= len(s) {
		return false
	}
	if isSedDigit(s[j]) {
		for j < len(s) && isSedDigit(s[j]) {
			j++
		}
		*i = j
		return true
	}
	switch s[j] {
	case '$':
		*i = j + 1
		return true
	case '/':
		j++
		if !sedSkipRegex(s, &j) {
			return false
		}
		for j < len(s) && sedRegexFlag(s[j]) {
			j++
		}
		*i = j
		return true
	case '\\':
		if j+1 >= len(s) {
			return false
		}
		*i = j + 2
		return true
	}
	return true
}

func sedSkipRegex(s string, i *int) bool {
	j := *i
	for j < len(s) {
		if s[j] == '\\' {
			j += 2
			continue
		}
		if s[j] == '/' {
			*i = j + 1
			return true
		}
		j++
	}
	return false
}

// sedRegexFlag reports address-regex modifier letters (I/M: case-insensitive,
// multi-line). Anything else after the closing delimiter is the command itself,
// so the flag set must not span arbitrary letters.
func sedRegexFlag(c byte) bool {
	return c == 'I' || c == 'i' || c == 'M' || c == 'm'
}

// sedConsumeSubstitution parses 's/pat/repl/suffix'. The suffix may only
// contain the flags that keep the substitution a pure stream transform: g, p,
// I, i, M, m, and repeat counts. The e substitution flag executes the result
// as a shell command and w/W write matches to a file — both fail closed with a
// precise reason.
func sedConsumeSubstitution(s string, i int) (int, CommandEffect, bool) {
	sep, j, ok := sedOpenDelimiter(s, i)
	if !ok {
		return 0, unknownEffect("sed", "unparsable sed substitution"), false
	}
	if !sedSkipToSep(s, &j, sep) {
		return 0, unknownEffect("sed", "unparsable sed substitution pattern"), false
	}
	if !sedSkipToSep(s, &j, sep) {
		return 0, unknownEffect("sed", "unparsable sed substitution replacement"), false
	}
	for j < len(s) {
		switch c := s[j]; {
		case c == 'g' || c == 'p' || c == 'I' || c == 'i' || c == 'M' || c == 'm':
			j++
		case isSedDigit(c):
			j++
		case c == 'e' || c == 'E':
			return 0, unknownCodeEffect("sed", "sed substitution suffix executes a command"), false
		case c == 'w' || c == 'W':
			return 0, knownWriter("sed", WriteWorkspaceContent, "sed substitution suffix writes a file"), false
		default:
			return j, CommandEffect{}, true
		}
	}
	return j, CommandEffect{}, true
}

// sedConsumeTwoPart parses 'y/src/dst/' (character transliteration): both parts
// are plain strings with no trailing flags.
func sedConsumeTwoPart(s string, i int) (int, bool) {
	sep, j, ok := sedOpenDelimiter(s, i)
	if !ok {
		return 0, false
	}
	if !sedSkipToSep(s, &j, sep) {
		return 0, false
	}
	if !sedSkipToSep(s, &j, sep) {
		return 0, false
	}
	return j, true
}

func sedOpenDelimiter(s string, i int) (byte, int, bool) {
	if i >= len(s) {
		return 0, 0, false
	}
	return s[i], i + 1, true
}

func sedSkipToSep(s string, i *int, sep byte) bool {
	j := *i
	for j < len(s) {
		if s[j] == '\\' {
			j += 2
			continue
		}
		if s[j] == sep {
			*i = j + 1
			return true
		}
		j++
	}
	return false
}

func sedConsumeLabel(s string, i int) int {
	j := i
	for j < len(s) && sedLabelRune(s[j]) {
		j++
	}
	return j
}

func sedLabelRune(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') ||
		('0' <= c && c <= '9') || c == '_'
}

func isSedDigit(c byte) bool { return '0' <= c && c <= '9' }
