package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"

	"reasonix/internal/history"
	"reasonix/internal/historycatalog"
	"reasonix/internal/provider"
	"reasonix/internal/retrieval"
)

type targetHistorySliceCursor struct {
	V      int    `json:"v"`
	Target string `json:"target"`
	Cursor string `json:"cursor"`
}

func decodeTargetHistorySliceCursor(raw, target string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", newSessionOperationError("stale_cursor", "The session content changed. Reload it and try again.")
	}
	var cursor targetHistorySliceCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.V != 1 || cursor.Target != target {
		return "", newSessionOperationError("stale_cursor", "The session content changed. Reload it and try again.")
	}
	return cursor.Cursor, nil
}

func encodeTargetHistorySliceCursor(target, cursor string) string {
	if strings.TrimSpace(cursor) == "" {
		return ""
	}
	data, err := json.Marshal(targetHistorySliceCursor{V: 1, Target: target, Cursor: cursor})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// HistorySliceForTarget reads a bounded history page for either a canonical or
// legacy local session without selecting it or creating a controller.
func (a *App) HistorySliceForTarget(selector SessionSelector, req HistorySliceRequest) (HistorySlice, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return emptyHistorySlice(), err
	}
	targetKey := target.key()
	req.Cursor, err = decodeTargetHistorySliceCursor(req.Cursor, targetKey)
	if err != nil {
		return emptyHistorySlice(), err
	}
	req = normalizeHistorySliceRequest(req)
	if target.SessionRef.SessionID != "" {
		page, err := a.canonicalHistorySlice(a.desktopSessionService("").Query(), target.SessionRef, "", "", req)
		page.NextCursor = encodeTargetHistorySliceCursor(targetKey, page.NextCursor)
		return page, err
	}
	dir, path, err := a.sessionDirForPath(target.SessionPath)
	if err != nil {
		return emptyHistorySlice(), newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	page, err := a.coldHistorySlice(dir, path, req)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "cursor") {
			return emptyHistorySlice(), newSessionOperationError("stale_cursor", "The session content changed. Reload it and try again.")
		}
		return emptyHistorySlice(), err
	}
	page.NextCursor = encodeTargetHistorySliceCursor(targetKey, page.NextCursor)
	return page, nil
}

type targetHistorySearchCursor struct {
	V        int     `json:"v"`
	Revision uint64  `json:"revision"`
	Path     string  `json:"path"`
	Query    string  `json:"query"`
	Rank     float64 `json:"rank"`
	Message  int     `json:"message"`
	Part     int     `json:"part"`
	RowID    int64   `json:"rowId"`
}

func encodeTargetHistorySearchCursor(cursor targetHistorySearchCursor) string {
	data, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeTargetHistorySearchCursor(raw, path, query string, revision uint64) (*historycatalog.SearchCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, newSessionOperationError("stale_cursor", "The session content changed. Reload it and try again.")
	}
	var cursor targetHistorySearchCursor
	if err := json.Unmarshal(data, &cursor); err != nil ||
		cursor.V != 1 || cursor.Revision != revision ||
		sessionRuntimeKey(cursor.Path) != sessionRuntimeKey(path) ||
		cursor.Query != strings.TrimSpace(query) {
		return nil, newSessionOperationError("stale_cursor", "The session content changed. Reload it and try again.")
	}
	return &historycatalog.SearchCursor{
		Rank: cursor.Rank, SessionPath: path, MessageIndex: cursor.Message,
		PartIndex: cursor.Part, RowID: cursor.RowID,
	}, nil
}

// SearchHistoryContentForTarget searches the disposable legacy history index
// for one exact session. The cursor is bound to target, query, and index
// revision, so changing tabs or replaying a cursor against a sibling cannot
// change the routed object.
func (a *App) SearchHistoryContentForTarget(selector SessionSelector, query, cursor string, limit int) (HistorySearchPage, error) {
	target, err := a.resolveSessionTargetWithArchived(selector, true)
	if err != nil {
		return HistorySearchPage{Items: []HistorySearchHit{}}, err
	}
	if target.SessionRef.SessionID != "" {
		return HistorySearchPage{Items: []HistorySearchHit{}}, newSessionOperationError("unsupported", "Use canonical session search for this session.")
	}
	_, path, err := a.sessionDirForPath(target.SessionPath)
	if err != nil {
		return HistorySearchPage{Items: []HistorySearchHit{}}, newSessionOperationError(sessionOperationTargetNotFound, "The session no longer exists.")
	}
	status := a.GetHistoryIndexStatus()
	out := HistorySearchPage{Items: []HistorySearchHit{}, Status: status, Revision: status.Revision,
		Partial: status.State != "ready" || status.Pending > 0 || (status.Total > 0 && status.Indexed < status.Total)}
	catalog := history.SharedCatalog()
	query = strings.TrimSpace(query)
	if catalog == nil || query == "" {
		return out, nil
	}
	after, err := decodeTargetHistorySearchCursor(cursor, path, query, status.Revision)
	if err != nil {
		return out, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	result, err := catalog.Search(a.bootContext(), historycatalog.SearchRequest{
		Query: query, SessionPath: path,
		Kinds: []string{"user_text", "assistant_text", "tool_input", "tool_error"},
		Limit: limit + 1, After: after,
	})
	if err != nil {
		return out, err
	}
	queryTerms, _ := retrieval.QueryTerms(query)
	_, overlays := a.catalogRuntimeOverlays()
	loaded := map[string][]provider.Message{}
	recoveryChecked, recoveryCovered := map[string]bool{}, map[string]bool{}
	for _, candidate := range result.Items {
		hit, ok := a.historyHitFromCandidate(
			HistorySearchRequest{Query: query}, candidate, "", overlays, queryTerms,
			loaded, recoveryChecked, recoveryCovered, catalog,
		)
		if ok {
			out.Items = append(out.Items, hit)
		}
	}
	if len(result.Items) > limit {
		last := result.Items[limit-1]
		out.Items = out.Items[:min(len(out.Items), limit)]
		out.NextCursor = encodeTargetHistorySearchCursor(targetHistorySearchCursor{
			V: 1, Revision: status.Revision, Path: filepath.Clean(path), Query: query,
			Rank: last.Rank, Message: last.MessageIndex, Part: last.PartIndex, RowID: last.RowID,
		})
	}
	return out, nil
}
