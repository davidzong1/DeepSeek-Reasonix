package main

import (
	"encoding/json"
	"reasonix/internal/session"
)

func (a *App) ListSessionComposerConflicts(ref session.SessionRef) (result []string, err error) {
	result = []string{}
	defer func() { err = sessionUIError(err, composerRecordKey(ref), "") }()
	if err = validateLocalSessionRef(ref); err != nil {
		return result, err
	}
	rows, err := a.sessionUIStore().ConflictPayloads(a.bootContext(), "composer", composerRecordKey(ref))
	if err != nil {
		return result, err
	}
	for _, row := range rows {
		var view SessionComposerState
		if err = json.Unmarshal(row, &view); err != nil {
			return result, err
		}
		result = append(result, view.ContentJSON)
	}
	return result, nil
}
