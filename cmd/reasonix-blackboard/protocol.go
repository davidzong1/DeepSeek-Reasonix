package main

// Wire types for the reasonix-blackboard JSON protocol (route P6.1):
// snake_case request on stdin, response on stdout. Pure translations of
// the team store primitives — business logic never lives here.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"reasonix/internal/team"
)

// request is the union of every op's stdin payload; fields are decoded per
// op, so unused fields are simply ignored.
type request struct {
	Op string `json:"op"` // append | read-after | read-view | bind | cursor

	// append
	BoardID      string          `json:"board_id"`
	EventID      string          `json:"event_id,omitempty"`
	ClientMsgID  string          `json:"client_msg_id,omitempty"`
	Kind         string          `json:"kind,omitempty"` // append: event kind; read-after: filter
	TaskID       string          `json:"task_id,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"` // RFC3339; empty = now UTC
	Summary      string          `json:"summary,omitempty"`
	ArtifactRefs []wireRef       `json:"artifact_refs,omitempty"`
	Supersedes   []int64         `json:"supersedes,omitempty"`
	Identity     *wireIdentity   `json:"identity,omitempty"`
	Conclusion   *wireConclusion `json:"conclusion,omitempty"`

	// read-after / read-view
	AfterSeq int64  `json:"after_seq,omitempty"`
	MemberID string `json:"member_id,omitempty"` // read-after filter
	Limit    int    `json:"limit,omitempty"`

	// export
	IncludeArchived bool `json:"include_archived,omitempty"`

	// bind / cursor
	Action     string       `json:"action,omitempty"` // bind|unbind|get|all | advance|get
	Handoff    *wireHandoff `json:"handoff,omitempty"`
	ConsumerID string       `json:"consumer_id,omitempty"`
	Generation uint64       `json:"generation,omitempty"`
	LastSeq    int64        `json:"last_seq,omitempty"`
}

type wireIdentity struct {
	MemberID   string `json:"member_id"`
	Role       string `json:"role,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
}

type wireRef struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
	Digest string `json:"digest,omitempty"`
}

type wireConclusion struct {
	Topic     string `json:"topic"`
	BaseEpoch uint64 `json:"base_epoch"`
	Summary   string `json:"summary"`
}

type wireHandoff struct {
	TaskID       string    `json:"task_id"`
	Digest       string    `json:"digest"`
	ArtifactRefs []wireRef `json:"artifact_refs,omitempty"`
	Pending      string    `json:"pending,omitempty"`
}

type response struct {
	OK       bool          `json:"ok"`
	Event    *wireEvent    `json:"event,omitempty"`
	Page     *wirePage     `json:"page,omitempty"`
	View     *wireView     `json:"view,omitempty"`
	Record   *wireRecord   `json:"record,omitempty"`
	Records  []wireRecord  `json:"records,omitempty"`
	Cursor   *wireCursor   `json:"cursor,omitempty"`
	Snapshot *wireSnapshot `json:"snapshot,omitempty"`
	Jsonl    string        `json:"jsonl,omitempty"`
	Error    *wireError    `json:"error,omitempty"`
}

// wireSnapshot summarizes one export run; Jsonl carries the whole
// checkpoint-consistent snapshot so the caller reconciles against Digest.
type wireSnapshot struct {
	Lines    int    `json:"lines"`
	Digest   string `json:"digest"`
	Archived int    `json:"archived"`
}

type wireError struct {
	Kind    string         `json:"kind"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

type wireEvent struct {
	SchemaVersion uint16    `json:"schema_version"`
	BoardID       string    `json:"board_id"`
	Seq           int64     `json:"seq"`
	EventID       string    `json:"event_id"`
	ClientMsgID   string    `json:"client_msg_id"`
	Kind          string    `json:"kind"`
	TaskID        string    `json:"task_id"`
	MemberID      string    `json:"member_id"`
	Role          string    `json:"role"`
	Agent         string    `json:"agent"`
	Generation    uint64    `json:"generation"`
	CreatedAt     time.Time `json:"created_at"`
	Digest        string    `json:"digest"`
	Summary       string    `json:"summary"`
	ArtifactRefs  []wireRef `json:"artifact_refs,omitempty"`
	Supersedes    []int64   `json:"supersedes,omitempty"`
}

type wirePage struct {
	Events     []wireEvent `json:"events"`
	NextSeq    int64       `json:"next_seq"`
	HasMore    bool        `json:"has_more"`
	NeedResync bool        `json:"need_resync"`
}

type wireView struct {
	SourceSeq   int64               `json:"source_seq"`
	Epoch       uint64              `json:"epoch"`
	Digest      string              `json:"digest"`
	Conclusions []wireConclusionRow `json:"conclusions"`
}

type wireConclusionRow struct {
	TaskID   string `json:"task_id"`
	Topic    string `json:"topic"`
	Epoch    uint64 `json:"epoch"`
	EventSeq int64  `json:"event_seq"`
	Digest   string `json:"digest"`
	Summary  string `json:"summary"`
}

type wireRecord struct {
	MemberID   string `json:"member_id"`
	LeaderID   string `json:"leader_id"`
	Generation uint64 `json:"generation"`
	Status     string `json:"status"`
	TaskID     string `json:"task_id"`
	BoundAt    string `json:"bound_at"`
}

type wireCursor struct {
	BoardID    string `json:"board_id"`
	ConsumerID string `json:"consumer_id"`
	Generation uint64 `json:"generation"`
	LastSeq    int64  `json:"last_seq"`
}

// Handle translates one request and returns the response JSON. A nil error
// means the response was produced (even for business rejections); a non-nil
// error means the request could not be served at all (bad JSON, unknown op,
// store failure) and the caller should exit non-zero without a response.
func Handle(ctx context.Context, store *team.SQLiteStore, registry *team.BindingRegistry, in []byte) ([]byte, error) {
	var req request
	if err := json.Unmarshal(in, &req); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	switch req.Op {
	case "append":
		return handleAppend(ctx, store, req)
	case "read-after":
		return handleReadAfter(ctx, store, req)
	case "read-view":
		return handleReadView(ctx, store, req)
	case "bind":
		return handleBind(registry, req)
	case "cursor":
		return handleCursor(ctx, store, req)
	case "export":
		return handleExport(ctx, store, req)
	}
	return nil, fmt.Errorf("unknown op %q", req.Op)
}

func handleAppend(ctx context.Context, store *team.SQLiteStore, req request) ([]byte, error) {
	id, err := req.identity()
	if err != nil {
		return encodeError("invalid-request", err.Error(), nil), nil
	}
	if err := team.CheckBoardAccess(req.BoardID, id, true); err != nil {
		return encodeStoreError(err), nil
	}
	if err := store.CheckGeneration(ctx, req.BoardID, id.MemberID, id.Generation); err != nil {
		return encodeStoreError(err), nil
	}
	createdAt, err := parseTime(req.CreatedAt)
	if err != nil {
		return encodeError("invalid-request", err.Error(), nil), nil
	}
	in := team.AppendInput{
		BoardID:      req.BoardID,
		EventID:      req.EventID,
		ClientMsgID:  req.ClientMsgID,
		Kind:         team.EventKind(req.Kind),
		TaskID:       team.TaskID(req.TaskID),
		CreatedAt:    createdAt,
		Summary:      req.Summary,
		ArtifactRefs: wireRefs(req.ArtifactRefs),
		Supersedes:   req.Supersedes,
		Stamped:      id,
	}
	if req.Conclusion != nil {
		in.Conclusion = &team.ConclusionUpdate{
			Topic:     req.Conclusion.Topic,
			BaseEpoch: req.Conclusion.BaseEpoch,
			Summary:   req.Conclusion.Summary,
		}
	}
	ev, err := store.Append(ctx, in)
	if err != nil {
		return encodeStoreError(err), nil
	}
	return json.Marshal(response{OK: true, Event: wireOfEvent(ev)})
}

func handleReadAfter(ctx context.Context, store *team.SQLiteStore, req request) ([]byte, error) {
	id, err := req.identity()
	if err != nil {
		return encodeError("invalid-request", err.Error(), nil), nil
	}
	if err := team.CheckBoardAccess(req.BoardID, id, false); err != nil {
		return encodeStoreError(err), nil
	}
	page, err := store.ReadAfter(ctx, req.BoardID, req.AfterSeq, team.Filter{
		TaskID:   team.TaskID(req.TaskID),
		Kind:     team.EventKind(req.Kind),
		MemberID: req.MemberID,
		Limit:    req.Limit,
		Stamped:  id,
	})
	if err != nil {
		return encodeStoreError(err), nil
	}
	out := &wirePage{NextSeq: page.NextSeq, HasMore: page.HasMore, NeedResync: page.NeedResync}
	for _, ev := range page.Events {
		out.Events = append(out.Events, *wireOfEvent(ev))
	}
	return json.Marshal(response{OK: true, Page: out})
}

func handleReadView(ctx context.Context, store *team.SQLiteStore, req request) ([]byte, error) {
	id, err := req.identity()
	if err != nil {
		return encodeError("invalid-request", err.Error(), nil), nil
	}
	if err := team.CheckBoardAccess(req.BoardID, id, false); err != nil {
		return encodeStoreError(err), nil
	}
	view, err := store.ReadView(ctx, req.BoardID, team.ViewSpec{TaskID: team.TaskID(req.TaskID), Limit: req.Limit})
	if err != nil {
		return encodeStoreError(err), nil
	}
	out := &wireView{SourceSeq: view.SourceSeq, Epoch: view.Epoch, Digest: view.Digest}
	for _, c := range view.Conclusions {
		out.Conclusions = append(out.Conclusions, wireConclusionRow{
			TaskID: string(c.TaskID), Topic: c.Topic, Epoch: c.Epoch,
			EventSeq: c.EventSeq, Digest: c.Digest, Summary: c.Summary,
		})
	}
	return json.Marshal(response{OK: true, View: out})
}

func handleBind(registry *team.BindingRegistry, req request) ([]byte, error) {
	switch req.Action {
	case "bind":
		id, err := req.identity()
		if err != nil {
			return encodeError("invalid-request", err.Error(), nil), nil
		}
		rec, err := registry.Bind(req.MemberID, id, team.TaskID(req.TaskID))
		if err != nil {
			return encodeStoreError(err), nil
		}
		return json.Marshal(response{OK: true, Record: wireOfRecord(rec)})
	case "unbind":
		id, err := req.identity()
		if err != nil {
			return encodeError("invalid-request", err.Error(), nil), nil
		}
		var h team.Handoff
		if req.Handoff != nil {
			h = team.Handoff{TaskID: team.TaskID(req.Handoff.TaskID), Digest: req.Handoff.Digest, Pending: req.Handoff.Pending}
			for _, r := range req.Handoff.ArtifactRefs {
				h.ArtifactRefs = append(h.ArtifactRefs, team.ArtifactRef{Name: r.Name, Path: r.Path, Size: r.Size, Digest: r.Digest})
			}
		}
		rec, err := registry.Unbind(req.MemberID, id, h)
		if err != nil {
			return encodeStoreError(err), nil
		}
		return json.Marshal(response{OK: true, Record: wireOfRecord(rec)})
	case "get":
		rec, ok := registry.GetBind(req.MemberID)
		if !ok {
			return encodeError("not-found", "no binding for member", nil), nil
		}
		return json.Marshal(response{OK: true, Record: wireOfRecord(rec)})
	case "all":
		records := registry.All()
		out := make([]wireRecord, 0, len(records))
		for _, rec := range records {
			out = append(out, *wireOfRecord(rec))
		}
		return json.Marshal(response{OK: true, Records: out})
	}
	return encodeError("invalid-request", "unknown bind action "+req.Action, nil), nil
}

func handleCursor(ctx context.Context, store *team.SQLiteStore, req request) ([]byte, error) {
	switch req.Action {
	case "advance":
		err := store.AdvanceCursor(ctx, team.CursorUpdate{
			BoardID:    req.BoardID,
			ConsumerID: req.ConsumerID,
			Generation: req.Generation,
			LastSeq:    req.LastSeq,
		})
		if err != nil {
			return encodeStoreError(err), nil
		}
		return json.Marshal(response{OK: true})
	case "get":
		c, err := store.GetCursor(ctx, req.BoardID, req.ConsumerID)
		if err != nil {
			return encodeStoreError(err), nil
		}
		return json.Marshal(response{OK: true, Cursor: &wireCursor{
			BoardID: c.BoardID, ConsumerID: c.ConsumerID, Generation: c.Generation, LastSeq: c.LastSeq,
		}})
	}
	return encodeError("invalid-request", "unknown cursor action "+req.Action, nil), nil
}

func (r *request) identity() (team.Identity, error) {
	if r.Identity == nil {
		return team.Identity{}, errors.New("identity is required")
	}
	return team.Identity{
		MemberID:   r.Identity.MemberID,
		Role:       r.Identity.Role,
		Agent:      r.Identity.Agent,
		Generation: r.Identity.Generation,
	}, nil
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Now().UTC(), nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// encodeStoreError maps a team store/binding error to the wire error shape.
// Business rejections are responses, not process failures: the caller reads
// the JSON and keeps the DB handle.
func encodeStoreError(err error) []byte {
	var ce *team.ErrConflict
	if errors.As(err, &ce) {
		return encodeError("conflict", err.Error(), map[string]any{
			"current_epoch": ce.CurrentEpoch,
			"current_seq":   ce.CurrentSeq,
		})
	}
	switch {
	case errors.Is(err, team.ErrForbidden):
		return encodeError("forbidden", err.Error(), nil)
	case errors.Is(err, team.ErrStaleGeneration):
		return encodeError("stale-generation", err.Error(), nil)
	case errors.Is(err, team.ErrCursorBackwards):
		return encodeError("cursor-backwards", err.Error(), nil)
	case errors.Is(err, team.ErrCursorNotFound):
		return encodeError("cursor-not-found", err.Error(), nil)
	case errors.Is(err, team.ErrNotBound):
		return encodeError("not-bound", err.Error(), nil)
	case errors.Is(err, team.ErrBindConflict):
		return encodeError("bind-conflict", err.Error(), nil)
	case errors.Is(err, team.ErrInvalidHandoff):
		return encodeError("invalid-handoff", err.Error(), nil)
	case errors.Is(err, team.ErrInvalidTask):
		return encodeError("invalid-task", err.Error(), nil)
	}
	return encodeError("internal", err.Error(), nil)
}

func encodeError(kind, message string, detail map[string]any) []byte {
	out, _ := json.Marshal(response{OK: false, Error: &wireError{Kind: kind, Message: message, Detail: detail}})
	return out
}

// wireOfEvent maps a stamped event to the wire shape. The digest field is
// store-computed over the Go struct's default field names — any side that
// recomputes it must serialize identically (route §1.1).
func wireOfEvent(ev team.BoardEvent) *wireEvent {
	refs := make([]wireRef, 0, len(ev.ArtifactRefs))
	for _, r := range ev.ArtifactRefs {
		refs = append(refs, wireRef{Name: r.Name, Path: r.Path, Size: r.Size, Digest: r.Digest})
	}
	return &wireEvent{
		SchemaVersion: ev.SchemaVersion,
		BoardID:       ev.BoardID,
		Seq:           ev.Seq,
		EventID:       ev.EventID,
		ClientMsgID:   ev.ClientMsgID,
		Kind:          string(ev.Kind),
		TaskID:        string(ev.TaskID),
		MemberID:      ev.MemberID,
		Role:          ev.Role,
		Agent:         ev.Agent,
		Generation:    ev.Generation,
		CreatedAt:     ev.CreatedAt,
		Digest:        ev.Digest,
		Summary:       ev.Summary,
		ArtifactRefs:  refs,
		Supersedes:    ev.Supersedes,
	}
}

func wireRefs(in []wireRef) []team.ArtifactRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]team.ArtifactRef, 0, len(in))
	for _, r := range in {
		out = append(out, team.ArtifactRef{Name: r.Name, Path: r.Path, Size: r.Size, Digest: r.Digest})
	}
	return out
}

func wireOfRecord(rec team.BindRecord) *wireRecord {
	return &wireRecord{
		MemberID:   rec.MemberID,
		LeaderID:   rec.LeaderID,
		Generation: rec.Generation,
		Status:     string(rec.Status),
		TaskID:     string(rec.TaskID),
		BoundAt:    rec.BoundAt.Format(time.RFC3339Nano),
	}
}

// handleExport dumps the checkpoint-consistent JSONL snapshot (route
// §1.4). It is a management read: the snapshot is derived from the only
// source of truth, never a second write path, so no identity gate applies.
func handleExport(ctx context.Context, store *team.SQLiteStore, req request) ([]byte, error) {
	var buf bytes.Buffer
	rep, err := store.ExportSnapshot(ctx, &buf, team.ExportOptions{
		SinceSeq:        req.AfterSeq,
		IncludeArchived: req.IncludeArchived,
	})
	if err != nil {
		return encodeStoreError(err), nil
	}
	return json.Marshal(response{
		OK:       true,
		Snapshot: &wireSnapshot{Lines: rep.Lines, Digest: rep.Digest, Archived: rep.Archived},
		Jsonl:    buf.String(),
	})
}
