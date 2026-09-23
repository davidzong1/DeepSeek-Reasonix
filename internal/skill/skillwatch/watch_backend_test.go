package skillwatch

// Watch-dependent tests need the host to be able to arm a filesystem watch;
// when it cannot (inotify exhaustion reports as "too many open files"), they
// would report a timeout that says nothing about the code.

import (
	"testing"

	"github.com/fsnotify/fsnotify"
)

func requireWatchBackend(t *testing.T) {
	t.Helper()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skipf("host cannot arm a filesystem watch (%v); skipping a watch-dependent test", err)
	}
	defer watcher.Close()
	if err := watcher.Add(t.TempDir()); err != nil {
		t.Skipf("host cannot watch a directory (%v); skipping a watch-dependent test", err)
	}
}
