package evidence

import (
	"encoding/json"
	"strings"
)

// opPathsField reads the path of every entry in a tool's ops array. A multi-file
// transaction (atomic_write's ops) names its targets only inside that array, so
// without this the receipt would claim a write covering no path and every "was
// this file changed" check would miss all of them.
//
// Entries that are not objects, or that carry no path, are skipped rather than
// failing the whole extraction: a malformed entry is the tool's own validation
// problem, not a reason to drop the paths that were readable.
func opPathsField(fields map[string]json.RawMessage, key string) []string {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var ops []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &ops); err != nil {
		return nil
	}
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		if path := strings.TrimSpace(op.Path); path != "" {
			out = append(out, path)
		}
	}
	return out
}
