package skill

// Watch-backed catalog tests need the host to be able to arm a filesystem
// watch; when it cannot (inotify instance exhaustion reports as "too many open
// files"), those tests would report a timeout that says nothing about the code.

import (
	"testing"

	"github.com/fsnotify/fsnotify"
)

// requireWatchBackend skips the test when this host cannot arm a filesystem
// watch at all. It probes with a throwaway watcher on a temp directory, which
// is the same call the store makes.
func requireWatchBackend(t *testing.T) {
	t.Helper()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skipf("host cannot arm a filesystem watch (%v); skipping a watch-dependent catalog test", err)
	}
	defer watcher.Close()
	if err := watcher.Add(t.TempDir()); err != nil {
		t.Skipf("host cannot watch a directory (%v); skipping a watch-dependent catalog test", err)
	}
}
