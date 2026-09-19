package shellsafe

import (
	"strings"
)

// classifyXxd classifies an xxd invocation from its static argv. xxd writes the
// second positional operand in every mode — `xxd [options] [infile [outfile]]`
// is not a reverse-only shape — so that operand count is the verdict, and -r
// additionally decodes into it. Options are parsed the way xxd parses them:
// only the letter right after the dash is an option, so -ps stays postscript
// output instead of plain -p plus -s, and an option value is either attached
// (-l64) or the next argument (-l 64, -s -5). An option shape this parser does
// not recognize fails closed, because undercounting operands would hide the
// one that writes.
func classifyXxd(args []string) CommandEffect {
	if hasEffectArg(args, "-r", "--revert") || hasShortOption(args, 'r') {
		return knownWriter("xxd", WriteWorkspaceContent, "writes decoded bytes")
	}
	operands, ok := xxdOperands(args)
	if !ok {
		return unknownEffect("xxd", "unsupported xxd option shape")
	}
	if operands > 1 {
		return knownWriter("xxd", WriteWorkspaceContent, "writes the output operand")
	}
	return knownReader("xxd")
}

// xxdValueOptions take an argument; xxdBooleanOptions never consume the next
// argv entry. An option outside both sets stays unproven.
const (
	xxdValueOptions   = "cglos"
	xxdBooleanOptions = "abCdeEhipruv"
)

// xxdOperands counts the arguments that can name a file, and reports whether the
// option shape was understood at all. A lone "-" counts like any other operand,
// and long options fail closed: xxd rejects them, and reading them as flags
// would let the operand after one go uncounted.
func xxdOperands(args []string) (int, bool) {
	operands := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			operands++
			continue
		}
		if len(arg) < 2 || arg[1] == '-' {
			return 0, false
		}
		switch flag := arg[1]; {
		case strings.IndexByte(xxdValueOptions, flag) >= 0:
			if len(arg) > 2 {
				continue
			}
			if i+1 >= len(args) {
				return 0, false
			}
			i++
		case strings.IndexByte(xxdBooleanOptions, flag) >= 0:
		default:
			return 0, false
		}
	}
	return operands, true
}
