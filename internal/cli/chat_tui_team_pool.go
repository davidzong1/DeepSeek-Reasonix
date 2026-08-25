package cli

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// poolInputKind is the cli-owned write state of the agent-user pool screen
// (§6.2): the field-list editor and the delete confirmation publish through
// the pool store on their confirm key — s from the editor, enter on the delete
// prompt — the same confirm() pattern as the team screens. The tui model stays
// display-only for teams; the pool is entirely cli-owned.
type poolInputKind int

const (
	poolInputNone      poolInputKind = iota
	poolInputDelete                  // confirming deletion of the focused entry
	poolInputEdit                    // field-list editor: cursor nav, enter opens a field, s saves
	poolInputEditField               // editing one field of the entry; enter confirms back to the list
)

// poolEditFields is the entry field editor's field list, id first: the id row
// is editable while adding and read-only for a published entry — the store
// never renames one. The api key renders in plaintext because the user's
// chosen contract overrides the default mask-everything policy on this screen
// (K2/K3 still govern logs, reports and messages). The api key is stored raw,
// never trimmed.
var poolEditFields = []string{
	team.AgentUserFieldID,
	team.AgentUserFieldIdentity,
	team.AgentUserFieldProvider,
	team.AgentUserFieldBaseURL,
	team.AgentUserFieldAPIKey,
	team.AgentUserFieldModel,
	team.AgentUserFieldEffort,
}

// poolState is the agent-user pool screen: the entries as last loaded, the
// focused row, the transient write state, and the editor draft. active
// replaces the team list, so "a"/"d" in the pool are pool keys, never
// team-list keys. detail toggles the entry detail view; edit is the field
// cursor of the field-list editor, whose draft is the entry under edit —
// empty while adding (adding), seeded from the focused entry while editing.
type poolState struct {
	active bool
	users  []team.AgentUser
	focus  int
	kind   poolInputKind
	buf    string
	errMsg string
	draft  team.AgentUser // editor draft: the new entry, or the entry under field edit
	detail bool           // entry detail view; esc steps back before closing
	edit   int            // field cursor into poolEditFields
	adding bool           // the editor creates a new entry (a); s calls AddAgentUser
	list   optionList     // the provider picker's option list
}

// enterTeamPool opens the pool screen from the team list, reading the entries
// from agent_users.json on entry so a stale document surfaces as a message,
// never as fabricated rows. An absent pool is an empty one whose a key
// creates the first entry.
func (p *teamPicker) enterTeamPool() {
	p.pool = poolState{active: true}
	if p.store == nil {
		p.pool.errMsg = "Agent data unavailable: project root unusable"
		return
	}
	if err := p.reloadPool(); err != nil {
		p.pool.errMsg = poolErrMsg(err)
	}
}

// reloadPool re-reads the pool into the screen, clamping the focus back into
// range after a delete.
func (p *teamPicker) reloadPool() error {
	users, err := p.store.ListAgentUsers()
	if err != nil {
		return err
	}
	p.pool.users = users
	if p.pool.focus >= len(users) {
		p.pool.focus = 0
	}
	p.pool.errMsg = ""
	return nil
}

// poolErrMsg maps a pool mutation error onto the overlay message, keeping the
// refusals readable and distinct from I/O, which reads as "unavailable". A
// field refusal names the field and reason, never the typed value (§7.3).
func poolErrMsg(err error) string {
	var fieldErr *team.AgentUserFieldError
	switch {
	case errors.As(err, &fieldErr):
		return fieldErr.Error()
	case errors.Is(err, team.ErrLastAgentUser):
		return "Cannot delete the last agent user — at least one entry must remain"
	case errors.Is(err, team.ErrAgentUserExists):
		return "An agent user with that id already exists"
	case errors.Is(err, team.ErrAgentUserNotFound):
		return "No such agent user"
	case errors.Is(err, team.ErrAgentUserInUse):
		return "Agent user is still referenced by a team or member — unbind it first"
	case errors.Is(err, team.ErrInvalidAgentUser):
		return "Agent user id must not be empty"
	default:
		return "Agent data unavailable: " + err.Error()
	}
}

// handlePoolKey routes a keypress on the pool screen and reports whether it
// consumed the key: up/down move the focus, a arms the field-list editor on an
// empty draft, d arms the delete confirmation, e arms the editor on the focused
// entry, esc steps out of detail or back to the team list. The editor and the
// write states own every key while they are active (§6.2), so "a"/"d" in them
// are ordinary letters, never pool keys.
func handlePoolKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	pool := &p.pool
	switch pool.kind {
	case poolInputDelete:
		switch msg.String() {
		case "enter":
			p.confirmPoolDelete()
		case "esc", "q", "ctrl+c":
			pool.kind = poolInputNone
		}
		return true
	case poolInputEdit:
		return handlePoolEditKey(p, msg)
	case poolInputEditField:
		return handlePoolEditFieldKey(p, msg)
	}
	switch msg.String() {
	case "up":
		movePoolFocus(pool, -1)
	case "down", "j":
		movePoolFocus(pool, +1)
	case "a":
		if pool.errMsg == "" {
			p.armPoolEditor(true)
		}
	case "d":
		if pool.focus < len(pool.users) {
			pool.kind = poolInputDelete
		}
	case "enter":
		if pool.focus < len(pool.users) {
			pool.detail = !pool.detail
		}
	case "e":
		if pool.focus < len(pool.users) {
			p.armPoolEditor(false)
		}
	case "l":
		p.toggleLeader()
	case "esc":
		if pool.detail {
			pool.detail = false
		} else {
			p.pool = poolState{} // back to the team list, write state included
		}
	}
	return true
}

// poolProviderOptions is the provider picker's choice list: a legacy value an
// older version wrote leads the list — marked in place, so confirming it
// rewrites the same string and the pick survives until a legal option is
// chosen — then the blank "unconfigured" state and the canonical values in
// store order.
func poolProviderOptions(current string) []option {
	opts := []option{}
	if current != "" && !providerIsCanonical(current) {
		opts = append(opts, option{id: current, label: "legacy: " + current})
	}
	opts = append(opts, option{id: "", label: "Unconfigured"})
	for _, o := range team.ProviderOptions() {
		opts = append(opts, option{id: o.Value, label: o.Label})
	}
	return opts
}

// providerIsCanonical reports whether v is one of the store's current provider
// values, as opposed to a legacy string that predates the canonical set.
func providerIsCanonical(v string) bool {
	for _, o := range team.ProviderOptions() {
		if o.Value == v {
			return true
		}
	}
	return false
}

// armPoolEditor opens the field-list editor: on an empty draft for a (adding,
// so s calls AddAgentUser), or seeded with the focused entry for e (s calls
// UpdateAgentUser). The cursor lands on the first missing field, so a new
// entry starts at the id and a partially configured one at the gap. Nothing is
// written until s.
func (p *teamPicker) armPoolEditor(adding bool) {
	pool := &p.pool
	pool.kind = poolInputEdit
	pool.adding = adding
	if adding {
		pool.draft = team.AgentUser{}
	} else {
		pool.draft = pool.users[pool.focus]
	}
	pool.edit = firstMissingField(pool.draft, adding)
	pool.buf = ""
}

// firstMissingField returns the first editor row whose field is empty, so the
// cursor lands where the entry is incomplete — the id for a brand-new draft.
// For a published entry the id row is skipped (it is immutable), and a fully
// configured entry starts at the first editable field.
func firstMissingField(u team.AgentUser, adding bool) int {
	for i := range poolEditFields {
		if !adding && i == 0 {
			continue
		}
		if poolFieldValue(u, i) == "" {
			return i
		}
	}
	if adding {
		return 0
	}
	return 1
}

// handlePoolEditKey routes a keypress in the field-list editor: up/down move
// the field cursor, enter opens the focused field for typing (the id row of a
// published entry instead moves past it — the store never renames one), s
// saves the draft through full validation, esc cancels the whole edit without
// writing. The editor owns every key while active (§6.2), so "a"/"d" are
// letters here.
func handlePoolEditKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	pool := &p.pool
	switch msg.String() {
	case "up", "k":
		pool.edit = (pool.edit + len(poolEditFields) - 1) % len(poolEditFields)
	case "down", "j":
		pool.edit = (pool.edit + 1) % len(poolEditFields)
	case "enter":
		if pool.edit == 0 && !pool.adding {
			pool.edit = 1 // the id row of an existing entry is immutable
		} else {
			pool.kind = poolInputEditField
			if poolEditFields[pool.edit] == team.AgentUserFieldProvider {
				pool.list.setOptions(optionSingle, poolProviderOptions(pool.draft.Provider), pool.draft.Provider)
			} else {
				pool.buf = poolFieldValue(pool.draft, pool.edit)
			}
		}
	case "s":
		p.savePoolEdit()
	case "esc", "ctrl+c":
		pool.kind, pool.buf, pool.draft, pool.errMsg, pool.adding = poolInputNone, "", team.AgentUser{}, "", false
	}
	return true
}

// handlePoolEditFieldKey routes a keypress inside one field's edit: printable
// keys type, enter validates and merges the field into the draft, esc discards
// this field's edit and returns to the field list. A validation refusal keeps
// the edit on screen so it can be fixed in place. The provider field is a
// picker instead — printable keys never touch it.
func handlePoolEditFieldKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	pool := &p.pool
	if poolEditFields[pool.edit] == team.AgentUserFieldProvider {
		return handlePoolProviderKey(p, msg)
	}
	switch msg.String() {
	case "enter":
		p.commitPoolField()
	case "esc", "ctrl+c":
		pool.kind, pool.buf, pool.errMsg = poolInputEdit, "", ""
	case "backspace":
		if pool.buf != "" {
			pool.buf = strings.TrimSuffix(pool.buf, lastRune(pool.buf))
		}
	default:
		if msg.String() == "space" {
			pool.buf += " "
		} else if printableKey(msg.String()) {
			pool.buf += msg.String()
		}
	}
	return true
}

// handlePoolProviderKey routes a keypress inside the provider picker: the
// option list moves with up/down, enter confirms the highlighted option, esc
// cancels the field edit untouched, and every printable key is inert — a
// provider is chosen, never typed. A legacy value opens as a marked option,
// so confirming it rewrites the same string until the user highlights a legal
// one.
func handlePoolProviderKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	pool := &p.pool
	_, action := pool.list.handleKey(msg)
	switch action {
	case optionListCommit:
		p.commitPoolField()
	case optionListCancel:
		pool.kind, pool.errMsg = poolInputEdit, ""
		pool.list = optionList{}
	}
	return true
}

// poolFieldValue reads a pool entry's editable field by cursor index. The
// indices line up with poolEditFields; the api key is included because the
// editor renders key material in plaintext (user contract).
func poolFieldValue(u team.AgentUser, i int) string {
	switch poolEditFields[i] {
	case team.AgentUserFieldID:
		return u.UserID
	case team.AgentUserFieldIdentity:
		return u.Identity
	case team.AgentUserFieldProvider:
		return u.Provider
	case team.AgentUserFieldBaseURL:
		return u.BaseURL
	case team.AgentUserFieldModel:
		return u.Model
	case team.AgentUserFieldEffort:
		return u.Effort
	default:
		return u.APIKey
	}
}

// commitPoolField validates and merges the typed value into the editor draft,
// then returns to the field list. The provider picker merges its committed
// option instead — its choice set is closed (blank plus the three canonical
// values), and the whole-entry validation at s still guards the draft. The api
// key is stored raw: trimming would alter a secret the user deliberately
// typed. The id row writes only while adding — a published id is immutable
// (§2.1).
func (p *teamPicker) commitPoolField() {
	pool := &p.pool
	field := poolEditFields[pool.edit]
	if field == team.AgentUserFieldProvider {
		if id, ok := pool.list.choice(); ok {
			pool.buf = id
		}
		applyPoolEditField(pool, field)
		pool.errMsg, pool.buf, pool.kind = "", "", poolInputEdit
		return
	}
	if err := team.ValidateAgentUserField(field, pool.buf); err != nil {
		pool.errMsg = poolErrMsg(err)
		return
	}
	applyPoolEditField(pool, field)
	pool.errMsg = ""
	pool.buf = ""
	pool.kind = poolInputEdit
}

// applyPoolEditField copies the typed value into the editor draft. Non-secret
// fields are trimmed; the api key is stored raw; the id row writes only while
// adding.
func applyPoolEditField(pool *poolState, field string) {
	switch field {
	case team.AgentUserFieldID:
		if pool.adding {
			pool.draft.UserID = strings.TrimSpace(pool.buf)
		}
	case team.AgentUserFieldIdentity:
		pool.draft.Identity = strings.TrimSpace(pool.buf)
	case team.AgentUserFieldProvider:
		pool.draft.Provider = strings.TrimSpace(pool.buf)
	case team.AgentUserFieldBaseURL:
		pool.draft.BaseURL = strings.TrimSpace(pool.buf)
	case team.AgentUserFieldModel:
		pool.draft.Model = strings.TrimSpace(pool.buf)
	case team.AgentUserFieldEffort:
		pool.draft.Effort = strings.TrimSpace(pool.buf)
	case team.AgentUserFieldAPIKey:
		pool.draft.APIKey = pool.buf
	}
}

// savePoolEdit is the s key: the one store write of the whole editor. The
// draft validates as a whole entry (id non-empty, every field legal) — a
// refusal locates the offending field and stays on the list — then publishes
// through the CAS path: AddAgentUser for a new entry (a), UpdateAgentUser for
// an existing one (e), and re-reads (write-then-read-back, §8.3). Editing an
// existing entry whose provider predates the canonical set skips the strict
// whole-entry gate: the store applies the legacy-preserve exemption (the
// provider is preserved until the user picks a legal option), and its refusal
// still renders.
func (p *teamPicker) savePoolEdit() {
	pool := &p.pool
	if err := team.ValidateAgentUser(pool.draft); err != nil {
		var fe *team.AgentUserFieldError
		if !errors.As(err, &fe) || fe.Field != team.AgentUserFieldProvider || pool.adding {
			pool.errMsg = poolErrMsg(err)
			locatePoolEditField(pool, err)
			return
		}
	}
	var err error
	if pool.adding {
		err = p.store.AddAgentUser(pool.draft)
	} else {
		err = p.store.UpdateAgentUser(pool.draft)
	}
	if err != nil {
		pool.errMsg = poolErrMsg(err)
		return
	}
	pool.kind, pool.buf, pool.edit, pool.draft, pool.adding = poolInputNone, "", 0, team.AgentUser{}, false
	if err := p.reloadPool(); err != nil {
		pool.errMsg = poolErrMsg(err)
	}
}

// locatePoolEditField moves the editor cursor onto the field a refusal names,
// so a save failure is positioned, never a bare message. A blank id — the one
// refusal that is not field-typed — lands on the id row.
func locatePoolEditField(pool *poolState, err error) {
	if errors.Is(err, team.ErrInvalidAgentUser) {
		pool.edit = 0
		return
	}
	var fe *team.AgentUserFieldError
	if !errors.As(err, &fe) {
		return
	}
	for i, f := range poolEditFields {
		if f == fe.Field {
			pool.edit = i
			return
		}
	}
}

// boundMembers lists the team/member pairs bound to a pool entry across the
// registry as last loaded, so the pool detail can show where an entry is used
// before it is deleted.
func (p *teamPicker) boundMembers(userID string) []string {
	var out []string
	for _, t := range p.doc.Teams {
		for _, slot := range t.Template {
			if slot.AgentUserRef == userID {
				out = append(out, t.Name+"/"+slot.MemberID)
			}
		}
	}
	return out
}

// movePoolFocus shifts the pool focus one step, clamped.
func movePoolFocus(pool *poolState, d int) {
	if n := len(pool.users); n > 0 {
		pool.focus = min(max(pool.focus+d, 0), n-1)
	}
}

// confirmPoolDelete removes the focused entry and re-reads; the store refuses
// deleting the last entry (ErrLastAgentUser), which the pool renders.
func (p *teamPicker) confirmPoolDelete() {
	p.pool.kind = poolInputNone
	if p.pool.focus >= len(p.pool.users) {
		return
	}
	id := p.pool.users[p.pool.focus].UserID
	if err := p.store.DeleteAgentUser(id); err != nil {
		p.pool.errMsg = poolErrMsg(err)
		return
	}
	if err := p.reloadPool(); err != nil {
		p.pool.errMsg = poolErrMsg(err)
	}
}

// handleWheel routes a wheel event into the active option list — the member
// field picker or the pool's provider picker — and reports whether it consumed
// it. Everywhere else the wheel keeps scrolling the transcript.
func (p *teamPicker) handleWheel(b tea.MouseButton) bool {
	if b != tea.MouseWheelUp && b != tea.MouseWheelDown {
		return false
	}
	if p.pool.active && p.pool.kind == poolInputEditField &&
		poolEditFields[p.pool.edit] == team.AgentUserFieldProvider {
		return p.pool.list.wheel(b == tea.MouseWheelUp)
	}
	if !p.pool.active && p.model.Mode() == tui.ModeContext &&
		p.memberEdit.kind == memberEditFieldEdit {
		return p.memberEdit.list.wheel(b == tea.MouseWheelUp)
	}
	return false
}
