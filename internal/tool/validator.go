package tool

import (
	"context"
	"encoding/json"
)

// ArgsValidator is an optional pre-execution contract a Tool may implement:
// validate the model-generated raw JSON args before permission, hooks,
// leases, or Execute. Registry-backed capability dispatch (use_capability →
// tool:*) consults it during resolution so malformed calls fail before the
// authorization flow starts; the tool's own Execute checks stay as defense
// in depth. Type-assert to discover support; most tools do not implement it.
type ArgsValidator interface {
	ValidateArgs(ctx context.Context, args json.RawMessage) error
}
