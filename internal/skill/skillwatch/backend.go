package skillwatch

import (
	"errors"
	"io"
)

// errHelperStopped reports a helper backend that cannot serve registrations:
// process dead and out of restart budget, or never started.
var errHelperStopped = errors.New("watch helper stopped")

// helperProcess is the minimal process handle the helper client needs; it
// exists so tests can inject a re-exec without depending on os/exec here.
type helperProcess interface {
	Stdin() io.Writer
	Stdout() io.Reader
	Wait() error
	Kill() error
}

// backend abstracts the physical watch mechanism. register may block (the
// helper path waits for the pipe round trip), so the service always calls it
// from its own goroutine; cancel is fire-and-forget and close is terminal.
type backend interface {
	register(id, rootGen uint64, root string, dirs []string) error
	cancel(id uint64)
	close() error
	physicalWatches() uint64
}
