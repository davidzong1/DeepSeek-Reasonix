package cli

import (
	"reasonix/internal/config"
	"reasonix/internal/i18n"
)

// applyConfigLanguage re-detects the UI language from config when one exists;
// doctor runs stay on the env/CLI language, so they skip the config.
func applyConfigLanguage(doctorRepair bool) {
	if doctorRepair {
		return
	}
	if cfg, err := config.Load(); err == nil {
		if cfg.Language != "" {
			i18n.DetectLanguage(cfg.Language)
		}
	}
}
