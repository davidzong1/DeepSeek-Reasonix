package boot

import (
	"io"

	"reasonix/internal/skill"
	"reasonix/internal/skill/skillwatch"
)

func newSkillWatchService(enabled bool, stderr io.Writer) *skillwatch.Service {
	if !enabled {
		return nil
	}
	return skillwatch.NewService(skillwatch.Options{Stderr: stderr})
}

func closeSkillsWithWatcher(primary, all *skill.Store, watchService **skillwatch.Service) {
	closeSkillStores(primary, all)
	if *watchService != nil {
		_ = (*watchService).Close()
		*watchService = nil
	}
}
