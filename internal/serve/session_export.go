package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"reasonix/internal/control"
	"reasonix/internal/session"
	"reasonix/internal/sessionexport"
)

func (s *Server) sessionExportSnapshot(w http.ResponseWriter, r *http.Request) {
	s.bindMu.Lock()
	if !s.validateExpectedSessionLocked(w, r) {
		s.bindMu.Unlock()
		return
	}
	query, ref, ok := s.canonicalSessionQuery(w, r)
	s.bindMu.Unlock()
	if !ok {
		return
	}
	var snapshot session.ExportSnapshot
	var err error
	if r.URL.Query().Get("diagnostic") == "1" {
		snapshot, err = query.CaptureDiagnosticSnapshot(r.Context(), ref)
	} else {
		snapshot, err = query.CaptureExportSnapshot(r.Context(), ref)
	}
	if err != nil {
		http.Error(w, "Unable to capture session export", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(snapshot)
}

// The snapshot is a stateless read capability within the authenticated session.
// No remote job/temporary files survive the response, including disconnects.
func (s *Server) sessionExportDocument(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Snapshot session.ExportSnapshot `json:"snapshot"`
		Format   string                 `json:"format"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&request); err != nil {
		http.Error(w, "invalid export request", http.StatusBadRequest)
		return
	}
	documentName := ""
	switch request.Format {
	case "markdown":
		documentName = "markdown"
	case "json":
		documentName = "json"
	case "blocks":
		documentName = "blocks"
	default:
		http.Error(w, "invalid export format", http.StatusBadRequest)
		return
	}
	s.bindMu.Lock()
	if !s.validateExpectedSessionLocked(w, r) {
		s.bindMu.Unlock()
		return
	}
	query, ref, ok := s.canonicalSessionQuery(w, r)
	s.bindMu.Unlock()
	if !ok {
		return
	}
	if request.Snapshot.Ref != ref {
		http.Error(w, "export target changed", http.StatusConflict)
		return
	}
	// Filesystem identity comes only from the canonical binding. Rebuilding the
	// snapshot prevents request data from becoming a path component even if the
	// equality check above is weakened later.
	snapshot := session.ExportSnapshot{
		ReadIncomplete:    request.Snapshot.ReadIncomplete,
		Ref:               ref,
		StorageGeneration: request.Snapshot.StorageGeneration,
		SnapshotSequence:  request.Snapshot.SnapshotSequence,
		AcceptedThrough:   request.Snapshot.AcceptedThrough,
		DurableThrough:    request.Snapshot.DurableThrough,
		CapturedAt:        request.Snapshot.CapturedAt,
		Title:             request.Snapshot.Title,
	}
	dir, err := os.MkdirTemp("", "reasonix-session-export-")
	if err != nil {
		http.Error(w, "export staging failed", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)
	doc, err := sessionexport.BuildForRef(r.Context(), query, ref, snapshot, dir, nil)
	if err != nil {
		http.Error(w, "session export failed or source changed", http.StatusConflict)
		return
	}
	file, err := os.Open(filepath.Join(dir, documentName))
	if err != nil {
		http.Error(w, "export unavailable", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	// Content-Length lets a client distinguish EOF from a truncated transport.
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "export unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Reasonix-Export-Records", fmt.Sprint(doc.Records))
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	_, _ = io.Copy(w, file)
}

func (s *Server) sessionExportDiagnostic(w http.ResponseWriter, r *http.Request) {
	var extra map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&extra); err != nil {
		http.Error(w, "invalid observation", http.StatusBadRequest)
		return
	}
	s.bindMu.Lock()
	if !s.validateExpectedSessionLocked(w, r) {
		s.bindMu.Unlock()
		return
	}
	controller, ok := s.ctl().(*control.Controller)
	caps := s.capabilities()
	s.bindMu.Unlock()
	if !ok {
		http.Error(w, "diagnostics unavailable", http.StatusNotImplemented)
		return
	}
	// Stage before sending headers so a failed writer never becomes a valid-looking
	// truncated JSON download. Flush failures remain evidence inside the document.
	file, err := os.CreateTemp("", "reasonix-diagnostic-")
	if err != nil {
		http.Error(w, "diagnostics unavailable", http.StatusInternalServerError)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	allowed := map[string]any{}
	for _, name := range []string{"sessionIdentity", "exportSnapshot", "frontendObservation", "readDiagnostics"} {
		if value, ok := extra[name]; ok {
			allowed[name] = value
		}
	}
	if err = controller.WriteSessionDiagnostics(r.Context(), file, control.GoalDiagnosticMetadata{Capabilities: caps}, allowed); err != nil {
		http.Error(w, "diagnostic export failed", http.StatusInternalServerError)
		return
	}
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "diagnostic export failed", http.StatusInternalServerError)
		return
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "diagnostic export failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, file)
}

func (s *Server) sessionExportValidate(w http.ResponseWriter, r *http.Request) {
	var snapshot session.ExportSnapshot
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&snapshot); err != nil {
		http.Error(w, "invalid snapshot", http.StatusBadRequest)
		return
	}
	s.bindMu.Lock()
	if !s.validateExpectedSessionLocked(w, r) {
		s.bindMu.Unlock()
		return
	}
	query, ref, ok := s.canonicalSessionQuery(w, r)
	s.bindMu.Unlock()
	if !ok {
		return
	}
	if ref != snapshot.Ref || query.ValidateExportSourceForRef(ref, snapshot) != nil {
		http.Error(w, "export source changed", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
