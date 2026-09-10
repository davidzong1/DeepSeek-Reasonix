package boot

import (
	"fmt"

	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/migration"
)

// legacyMigration is one startup config migration's outcome: whether it rewrote
// the user's config, and why it failed when it did not.
type legacyMigration struct {
	migrated bool
	err      error
}

// legacyMigrations carries every legacy-config migration build runs before the
// config is loaded, so their user-facing notices are emitted in one place.
type legacyMigrations struct {
	configResult     *config.MigrationResult
	configErr        error
	deepSeekProtocol legacyMigration
	stepLimits       legacyMigration
	redactToolOutput legacyMigration
	memoryCompiler   legacyMigration
	multiThreshold   legacyMigration
}

// emitLegacyMigrationNotices reports what the legacy-config migrations did: a
// notice per migration that rewrote the user's config, a warning per failed one,
// then the memory and session source imports. Emissions keep the migrations'
// own order, and every notice keeps its level and text.
func emitLegacyMigrationNotices(sink event.Sink, cfg *config.Config, m legacyMigrations) {
	if m.configErr != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Config migration did not complete.", Detail: "config migration from ~/.reasonix failed: " + m.configErr.Error()})
	} else if m.configResult != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: m.configResult.Notice()})
	}
	emitUserConfigUpgradeNotice(sink, cfg, m.deepSeekProtocol.migrated, m.deepSeekProtocol.err)
	if m.stepLimits.migrated || cfg.IgnoredLegacyAgentStepLimits() {
		level := event.LevelInfo
		text := "Deprecated agent step limits were removed."
		detail := "[agent].max_steps and planner_max_steps are no longer used; Reasonix now manages interactive progress automatically. " +
			"Use the CLI --max-steps flag for a one-off run or [bot].max_steps for unattended bot sessions."
		if m.stepLimits.err != nil {
			level = event.LevelWarn
			text = "Deprecated agent step limits were ignored."
			detail += " The old keys were ignored but could not be removed: " + m.stepLimits.err.Error()
		}
		sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  level,
			Text:   text,
			Detail: detail,
		})
	} else if m.stepLimits.err != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Deprecated agent step-limit migration did not complete.", Detail: m.stepLimits.err.Error()})
	}
	if m.redactToolOutput.migrated || m.redactToolOutput.err != nil {
		level := event.LevelInfo
		text := "Deprecated redact_tool_output setting was removed."
		detail := "[secrets].redact_tool_output no longer has any effect: ordinary model/tool content and local session/job artifacts now preserve their original text. Explicit diagnostics and reasonix doctor redact-sessions still redact credential values."
		if m.redactToolOutput.err != nil {
			level = event.LevelWarn
			text = "Deprecated redact_tool_output setting was ignored."
			detail += " The old key could not be removed: " + m.redactToolOutput.err.Error()
		}
		sink.Emit(event.Event{Kind: event.Notice, Level: level, Text: text, Detail: detail})
	}
	if m.memoryCompiler.migrated || m.memoryCompiler.err != nil {
		level := event.LevelInfo
		text := "Deprecated memory_compiler setting was removed."
		detail := "The Memory v5 execution compiler has been removed from Reasonix: [agent].memory_compiler no longer has any effect, user turns are never replaced by compiled execution contracts, and no compiler state is written. Old transcripts containing compiled turns still display normally."
		if m.memoryCompiler.err != nil {
			level = event.LevelWarn
			text = "Deprecated memory_compiler setting was ignored."
			detail += " The old key could not be removed: " + m.memoryCompiler.err.Error()
		}
		sink.Emit(event.Event{Kind: event.Notice, Level: level, Text: text, Detail: detail})
	}
	if m.multiThreshold.migrated || m.multiThreshold.err != nil {
		level := event.LevelInfo
		text := "上下文维护已简化为单一自动压缩阈值。"
		detail := "Context maintenance now uses a single automatic compact_ratio (default 0.80). soft_compact_ratio, tool_result_snip_ratio, compact_force_ratio, cold_resume_prune, and context_editing were removed from config."
		if m.multiThreshold.err != nil {
			level = event.LevelWarn
			text = "Deprecated multi-threshold compaction keys were ignored."
			detail += " The old keys could not be removed: " + m.multiThreshold.err.Error()
		}
		sink.Emit(event.Event{Kind: event.Notice, Level: level, Text: text, Detail: detail})
	}
	migration.MigrateLegacyMemorySources(sink)
	migration.MigrateLegacySessionSources(sink)
	if ignored := cfg.IgnoredProjectDefaultModel(); ignored != "" {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Ignored the project config's default_model.", Detail: fmt.Sprintf("./reasonix.toml sets default_model = %q but no configured provider serves it; using %q from your user config instead. Edit or remove that default_model line to silence this notice.", ignored, cfg.DefaultModel)})
	}
}
