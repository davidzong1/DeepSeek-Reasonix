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
	cur    int // rune cursor into buf while a text field is being typed
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

// teamSubscreenKey reports whether the active team subscreen — the agent-user
// pool manager or the roster's team-pool editor — consumed the key. Only one is
// active at a time; both own every key while up (§6.2).
func teamSubscreenKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	if p.pool.active && handlePoolKey(p, msg) {
		return true
	}
	return p.teamPool.active && handleTeamPoolSelKey(p, msg)
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
	pool.cur = 0
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
				pool.cur = fieldRuneCount(pool.buf)
			}
		}
	case "s":
		p.savePoolEdit()
	case "esc", "ctrl+c":
		pool.kind, pool.buf, pool.draft, pool.errMsg, pool.adding, pool.cur = poolInputNone, "", team.AgentUser{}, "", false, 0
	}
	return true
}

// handlePoolEditFieldKey routes a keypress inside one field's edit: runes
// insert at the cursor and left/right/home/end move it, enter validates and
// merges the field into the draft, esc discards this field's edit and returns
// to the field list. A validation refusal keeps the edit on screen so it can
// be fixed in place. The provider field is a picker instead — printable keys
// never touch it.
func handlePoolEditFieldKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	pool := &p.pool
	if poolEditFields[pool.edit] == team.AgentUserFieldProvider {
		return handlePoolProviderKey(p, msg)
	}
	switch msg.String() {
	case "enter":
		p.commitPoolField()
	case "esc", "ctrl+c":
		pool.kind, pool.buf, pool.errMsg, pool.cur = poolInputEdit, "", "", 0
	case "backspace":
		pool.buf, pool.cur = fieldBackspace(pool.buf, pool.cur)
	case "delete":
		pool.buf, pool.cur = fieldDelete(pool.buf, pool.cur)
	case "left":
		pool.cur = fieldMove(pool.buf, pool.cur, -1)
	case "right":
		pool.cur = fieldMove(pool.buf, pool.cur, +1)
	case "home":
		pool.cur = 0
	case "end":
		pool.cur = fieldRuneCount(pool.buf)
	default:
		if msg.String() == "space" {
			pool.buf, pool.cur = fieldInsert(pool.buf, pool.cur, " ")
		} else if printableKey(msg.String()) {
			pool.buf, pool.cur = fieldInsert(pool.buf, pool.cur, msg.String())
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
		pool.errMsg, pool.buf, pool.kind, pool.cur = "", "", poolInputEdit, 0
		return
	}
	if err := team.ValidateAgentUserField(field, pool.buf); err != nil {
		pool.errMsg = poolErrMsg(err)
		return
	}
	applyPoolEditField(pool, field)
	pool.errMsg = ""
	pool.buf = ""
	pool.cur = 0
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
// still renders. An edit bound by assembled members is refused while one is
// mid-turn and retires their idle backends after the write, so the next bind
// assembles the new provider/model (mirroring the member role/proxy gate).
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
	// The store gate accepts any well-formed field; only the adapter knows which
	// effort and model its endpoint serves. Asking it here turns a typo into an
	// editor refusal instead of a silent bind failure at t.
	if err := dryRunPoolEntry(pool.draft); err != nil {
		pool.errMsg = poolErrMsg(err)
		return
	}
	// Editing the entry repoints what bound members dial, so refuse while one is
	// mid-turn. Only a draft that changes a runtime field triggers the gate: an
	// identity-only edit serves nothing a backend baked in at assembly.
	runtimeChanged := !pool.adding && pool.focus < len(pool.users) &&
		memberAgentUserFingerprint(pool.users[pool.focus]) != memberAgentUserFingerprint(pool.draft)
	if runtimeChanged {
		if busy := p.poolBusyReferrers(pool.draft.UserID); len(busy) > 0 {
			pool.errMsg = "Finish or stop " + strings.Join(busy, ", ") + " before editing this agent user"
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
	// The write changed what bound members dial, so retire their now-idle
	// backends: the next bind assembles the new identity (the [1m] window
	// included) instead of keeping the pre-edit model until a later bind.
	if runtimeChanged {
		p.releasePoolReferrers(pool.draft.UserID)
	}
	pool.kind, pool.buf, pool.edit, pool.draft, pool.adding, pool.cur = poolInputNone, "", 0, team.AgentUser{}, false, 0
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

// poolAgentUserBindings lists the bindings that resolve to a pool entry across
// the registry as loaded, reusing the store's own override-else-default
// resolution — an edit must also reach the members that inherit the team
// default, which a slot-level check would miss.
func (p *teamPicker) poolAgentUserBindings(userID string) []team.MemberBinding {
	if p.store == nil {
		return nil
	}
	var out []team.MemberBinding
	for _, t := range p.doc.Teams {
		bindings, err := p.store.Bindings(t.Name)
		if err != nil {
			continue
		}
		for _, b := range bindings {
			if b.AgentUserRef == userID {
				out = append(out, b)
			}
		}
	}
	return out
}

// poolBusyReferrers names the assembled referencing backends that are mid-turn
// — running, waiting on a prompt, or executing background jobs. Closing one
// under a save would kill a live turn (§4.5), so the pool editor refuses.
func (p *teamPicker) poolBusyReferrers(userID string) []string {
	if p.backends == nil {
		return nil
	}
	var busy []string
	for _, b := range p.poolAgentUserBindings(userID) {
		backend, ok := p.backends.bound(b.Team, b.MemberID)
		if !ok {
			continue
		}
		if st := backend.RuntimeStatus(); st.Running || st.PendingPrompt || st.BackgroundJobs > 0 {
			busy = append(busy, b.Team+"/"+b.MemberID)
		}
	}
	return busy
}

// releasePoolReferrers retires the assembled backends of an edited entry that
// are idle, so the next bind dials the new provider/model. A backend that
// turned mid-turn since the save gate passed stays: closing it would cut that
// turn short, and the fingerprint-aware bind rebuilds it once it idles.
func (p *teamPicker) releasePoolReferrers(userID string) {
	if p.backends == nil {
		return
	}
	for _, b := range p.poolAgentUserBindings(userID) {
		backend, ok := p.backends.bound(b.Team, b.MemberID)
		if !ok {
			continue
		}
		if st := backend.RuntimeStatus(); st.Running || st.PendingPrompt || st.BackgroundJobs > 0 {
			continue
		}
		p.backends.release(b.Team, b.MemberID)
	}
}

// teamPoolSelState is the roster's ordered team-pool editor (§3.1): one row per
// registry entry, with the chosen pool entries selected in the order the user
// toggled them on — the saved pool order is the click order, never the registry
// order. active replaces the roster; nothing persists until Enter/s.
type teamPoolSelState struct {
	active bool
	users  []team.AgentUser // candidate rows, registry order
	sel    []string         // ordered selected ids — the pool being edited
	focus  int              // row cursor into users
	errMsg string
}

// openTeamPoolSel arms the team-pool editor from the roster u key: every
// registry entry is a candidate, and the team's current effective pool seeds
// the selection in pool order. Candidates absent from the registry (a dangling
// pool reference) drop out — the next save rewrites the pool from reality.
func (p *teamPicker) openTeamPoolSel() {
	if p.kind != teamInputNone || p.errMsg != "" {
		return
	}
	st := teamPoolSelState{active: true}
	users, err := p.store.ListAgentUsers()
	if err != nil {
		p.errMsg = pickerErrMsg(err)
		return
	}
	st.users = users
	present := make(map[string]bool, len(users))
	for _, u := range users {
		present[u.UserID] = true
	}
	if eff, err := p.store.EffectiveTeamPool(p.model.Name()); err == nil {
		for _, id := range eff {
			if present[id] {
				st.sel = append(st.sel, id)
			}
		}
	}
	p.teamPool = st
}

// handleTeamPoolSelKey owns every key while the team-pool editor is active:
// up/down move the cursor, space toggles the focused entry on (appending it to
// the selection tail — click order) or off, Enter/s publish the ordered pool,
// and esc cancels with zero writes.
func handleTeamPoolSelKey(p *teamPicker, msg tea.KeyPressMsg) bool {
	st := &p.teamPool
	switch msg.String() {
	case "up", "k":
		if n := len(st.users); n > 0 {
			st.focus = (st.focus + n - 1) % n
		}
	case "down", "j":
		if n := len(st.users); n > 0 {
			st.focus = (st.focus + 1) % n
		}
	case "space":
		p.toggleTeamPoolSelRow()
	case "enter", "s":
		p.commitTeamPoolSel()
	case "esc", "ctrl+c", "q":
		p.teamPool = teamPoolSelState{}
	}
	return true
}

// toggleTeamPoolSelRow flips the focused entry in the pool being edited: off
// removes it from the selection, on appends it to the selection tail, so the
// saved order is the order the user clicked entries on.
func (p *teamPicker) toggleTeamPoolSelRow() {
	st := &p.teamPool
	if st.focus >= len(st.users) {
		return
	}
	id := st.users[st.focus].UserID
	for i, sel := range st.sel {
		if sel == id {
			st.sel = append(st.sel[:i], st.sel[i+1:]...)
			return
		}
	}
	st.sel = append(st.sel, id)
}

// commitTeamPoolSel is the Enter/s key: it publishes the ordered pool through
// the store's validated, order-preserving write and returns to the roster.
// Saving an empty selection clears the team's default, which gates sessions
// until a pool is configured again (the roster's u is the way back in).
func (p *teamPicker) commitTeamPoolSel() {
	st := &p.teamPool
	if err := p.store.SetTeamAgentUserPool(p.model.Name(), st.sel); err != nil {
		st.errMsg = poolErrMsg(err)
		return
	}
	p.teamPool = teamPoolSelState{}
	if err := p.reload(""); err != nil {
		p.errMsg = pickerErrMsg(err)
	}
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
