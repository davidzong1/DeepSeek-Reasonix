package main

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"

	"reasonix/desktop/internal/hostrpc"
	"reasonix/internal/control"
	"reasonix/internal/secrets"
	"reasonix/internal/servecontract"
	"reasonix/internal/session"
)

func (a *App) writeSessionDiagnosticExport(job *sessionExportJob) error {
	extra := map[string]any{"sessionIdentity": map[string]any{"session": job.handle.Snapshot.Ref, "source": "local", "workspaceRoot": job.workspaceRoot, "storageGeneration": job.handle.Snapshot.StorageGeneration}, "exportSnapshot": job.handle.Snapshot, "frontendObservation": job.observation}
	var frontend map[string]json.RawMessage
	_ = json.Unmarshal(job.observation, &frontend)
	if value := frontend["readDiagnostics"]; len(value) > 0 {
		extra["readDiagnostics"] = value
	}
	if len(job.observation) == 0 {
		extra["frontendObservation"] = nil
	}
	return writeGoalDiagnosticsFile(job.path, func(dst io.Writer) error {
		if job.client != nil {
			extra["sessionIdentity"] = map[string]any{"session": job.handle.Snapshot.Ref, "source": "remote", "connectionHostId": job.sourceHostID, "workspaceRoot": job.workspaceRoot, "storageGeneration": job.handle.Snapshot.StorageGeneration}
			body, _ := json.Marshal(extra)
			resp, err := serveDoForSession(job.ctx, job.client, http.MethodPost, serveURL(job.base, "/session-export/diagnostic"), body, job.route)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return errors.New("remote session diagnostics are unavailable")
			}
			_, err = io.Copy(dst, resp.Body)
			return err
		}
		metadata := control.GoalDiagnosticMetadata{ApplicationVersion: version, BuildCommit: buildCommit(), ProtocolVersion: hostrpc.ProtocolVersion, Capabilities: []string{servecontract.SessionExportV1, servecontract.GoalLifecycleV2}}
		if job.controller != nil {
			return job.controller.WriteSessionDiagnostics(job.ctx, dst, metadata, extra)
		}
		// Cold sessions have durable evidence but no process-local runtime metrics.
		unavailable := []string{"runtime, submissionDiagnostics, shellDiagnostics and process-local lifecycle history are unavailable for a cold session"}
		if job.handle.Snapshot.ReadIncomplete {
			unavailable = append(unavailable, "snapshot capture encountered unreadable durable events; only the readable prefix is available")
		}
		fields := map[string]any{"schemaVersion": 1, "exportedAt": job.handle.Snapshot.CapturedAt, "metadata": metadata, "runtime": nil, "observation": nil, "submissionDiagnostics": nil, "shellDiagnostics": nil, "activationChanges": []any{}}
		maps.Copy(fields, extra)
		if _, err := io.WriteString(dst, "{\n"); err != nil {
			return err
		}
		enc := json.NewEncoder(dst)
		for name, value := range fields {
			key, _ := json.Marshal(name)
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if _, err = io.WriteString(dst, string(key)+":"+secrets.Redact(string(data))+",\n"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(dst, "\"commits\":["); err != nil {
			return err
		}
		first := true
		var writeErr error
		through := uint64(0)
		err := job.query.StreamExportCommits(job.ctx, job.handle.Snapshot, func(commit session.Commit) error {
			if commit.LastSequence() > job.handle.Snapshot.SnapshotSequence {
				return nil
			}
			bytes, err := json.Marshal(commit)
			if err != nil {
				return err
			}
			if !first {
				if _, err = io.WriteString(dst, ","); err != nil {
					writeErr = err
					return err
				}
			}
			first = false
			through = commit.LastSequence()
			_, err = io.WriteString(dst, secrets.Redact(string(bytes)))
			writeErr = err
			return err
		})
		if job.ctx.Err() != nil {
			return job.ctx.Err()
		}
		if writeErr != nil {
			return writeErr
		}
		if err != nil {
			unavailable = append(unavailable, "durable event traversal failed: "+secrets.RedactError(err))
		}
		if _, err = io.WriteString(dst, "],\n\"acceptedThrough\":"); err != nil {
			return err
		}
		if err = enc.Encode(through); err != nil {
			return err
		}
		if _, err = io.WriteString(dst, ",\"durableThrough\":"); err != nil {
			return err
		}
		if err = enc.Encode(through); err != nil {
			return err
		}
		if _, err = io.WriteString(dst, ",\"unavailable\":"); err != nil {
			return err
		}
		if err = enc.Encode(unavailable); err != nil {
			return err
		}
		_, err = io.WriteString(dst, "}\n")
		return err
	})
}
