package cli

import (
	"unicode/utf8"

	"reasonix/internal/team"
)

// The team overlay's property editors keep each open field's value in a plain
// string plus a rune cursor into it. Cursor arithmetic is rune-based so a
// left/right across a CJK or emoji rune moves by one visible character, and
// edits insert and delete at the cursor instead of only appending.

// fieldRuneCount returns the rune length of s — the valid range of a rune
// cursor (0..n).
func fieldRuneCount(s string) int { return utf8.RuneCountInString(s) }

// fieldClampCur clamps cur to s's rune range.
func fieldClampCur(s string, cur int) int {
	if cur < 0 {
		return 0
	}
	if n := fieldRuneCount(s); cur > n {
		return n
	}
	return cur
}

// fieldMove returns the rune cursor one step left (d < 0) or right (d > 0),
// clamped at either end.
func fieldMove(s string, cur int, d int) int {
	cur = fieldClampCur(s, cur)
	if d < 0 && cur > 0 {
		return cur - 1
	}
	if d > 0 && cur < fieldRuneCount(s) {
		return cur + 1
	}
	return cur
}

// fieldInsert places ins at the rune cursor of s and returns the new value
// with the cursor just past the inserted runes.
func fieldInsert(s string, cur int, ins string) (string, int) {
	rs := []rune(s)
	cur = fieldClampCur(s, cur)
	out := make([]rune, 0, len(rs)+utf8.RuneCountInString(ins))
	out = append(out, rs[:cur]...)
	out = append(out, []rune(ins)...)
	out = append(out, rs[cur:]...)
	return string(out), cur + utf8.RuneCountInString(ins)
}

// fieldBackspace deletes the rune before the cursor (backspace); the cursor
// retreats one. fieldDelete deletes the rune after it.
func fieldBackspace(s string, cur int) (string, int) {
	cur = fieldClampCur(s, cur)
	if cur == 0 {
		return s, 0
	}
	rs := []rune(s)
	return string(append(rs[:cur-1], rs[cur:]...)), cur - 1
}

func fieldDelete(s string, cur int) (string, int) {
	rs := []rune(s)
	cur = fieldClampCur(s, cur)
	if cur == len(rs) {
		return s, cur
	}
	return string(append(rs[:cur], rs[cur+1:]...)), cur
}

// fieldCursorView renders the buffer with the block cursor ▏ between the runes
// at the cursor, the way the property editors draw an open field's value.
func fieldCursorView(s string, cur int) string {
	rs := []rune(s)
	cur = fieldClampCur(s, cur)
	return string(rs[:cur]) + "▏" + string(rs[cur:])
}

// fieldPasteEnd parks the open property field's cursor at its end after the
// overlay's paste path appended to the buffer: a bracketed paste is one blob
// typed at the tail, and the next rune must follow it, not land mid-buffer at
// a cursor the user had moved earlier.
func fieldPasteEnd(p *teamPicker) {
	if p.pool.active && p.pool.kind == poolInputEditField && poolEditFields[p.pool.edit] != team.AgentUserFieldProvider {
		p.pool.cur = fieldRuneCount(p.pool.buf)
	}
	if p.memberEdit.kind == memberEditFieldEdit && len(memberEditFields) > p.memberEdit.edit && memberEditFields[p.memberEdit.edit] == "role" {
		p.memberEdit.cur = fieldRuneCount(p.memberEdit.buf)
	}
}

// teamOverlayPaste appends a bracketed paste to the overlay's active text
// buffer and snaps that field's cursor to the end, so the next typed rune
// follows the blob instead of landing at a stale mid-buffer position.
func teamOverlayPaste(p *teamPicker, content string) {
	if target := teamPasteTarget(p); target != nil {
		*target += content
		fieldPasteEnd(p)
	}
}
