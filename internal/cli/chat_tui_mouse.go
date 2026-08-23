package cli

import (
	tea "charm.land/bubbletea/v2"
)

// mouseCopyOrPaste applies the terminal mouse conventions while Reasonix owns
// the mouse: middle-click pastes tmux's buffer, or else the X11/Wayland PRIMARY
// selection; right-click copies an active composer or transcript selection and
// otherwise pastes the clipboard into whatever text target is active. It
// reports the command to run and whether the click was consumed — an
// unconsumed click falls through to the left-button gestures.
func (m *chatTUI) mouseCopyOrPaste(msg tea.MouseClickMsg) (tea.Cmd, bool) {
	switch {
	case msg.Button == tea.MouseMiddle:
		if !m.mousePasteAllowed() {
			return nil, true
		}
		return pasteMiddleClick(), true
	case msg.Button != tea.MouseRight:
		return nil, false
	case m.validComposerSelection() && !m.composerSel.empty():
		return m.copySelectionWithNotice(m.selectedComposerText()), true
	case m.sel.active && !m.sel.empty():
		text := m.selectedText()
		m.sel = selection{}
		return m.copySelectionWithNotice(text), true
	case m.mousePasteAllowed():
		return pasteClipboardText(), true
	}
	return nil, false
}
