package cli

// P2 acceptance test for the shared member-event pump: concurrent emitters
// into one tagged channel never lose an event and per-sender order survives;
// background unread counts record every duplicate-free terminal event.

import (
	"sync"
	"testing"

	"reasonix/internal/event"
)

// TestP2MemberEventPumpPreservesPerSenderOrder pins the shared-pump contract:
// the pump is one consumer over one channel, so concurrent emitters must never
// lose an event nor reorder another sender's sequence, and unread counts must
// reflect every terminal event of every background member exactly once.
func TestP2MemberEventPumpPreservesPerSenderOrder(t *testing.T) {
	m := chatTUI{}
	m.memberEvents = make(chan memberEvent, 64)
	m.teamPick = &teamPicker{session: sessionState{
		active: true, teamName: "alpha", current: "lead", unread: map[string]int{},
	}}
	const n = 25
	for _, member := range []string{"alice", "bob"} {
		m.teamPick.session.members = append(m.teamPick.session.members, member)
	}
	var wg sync.WaitGroup
	for _, member := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(member string) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				m.memberEvents <- memberEvent{
					member: member,
					ev:     event.Event{Kind: event.Message, Text: member + "#" + byteFrame(i)},
				}
			}
		}(member)
	}

	seen := map[string][]string{}
	got := 0
	for got < 2*n {
		msg := <-m.memberEvents
		if cmd := m.handleMemberEvent(memberEventMsg(msg)); cmd == nil {
			t.Fatal("the pump must re-arm after every event")
		}
		seen[msg.member] = append(seen[msg.member], msg.ev.Text)
		got++
	}
	wg.Wait()

	for _, member := range []string{"alice", "bob"} {
		if un := m.teamPick.session.unread[member]; un != n {
			t.Errorf("unread[%s] = %d, want %d", member, un, n)
		}
		list := seen[member]
		if len(list) != n {
			t.Fatalf("%s saw %d events, want %d (no loss)", member, len(list), n)
		}
		for i := 1; i < len(list); i++ {
			if list[i] <= list[i-1] {
				t.Fatalf("%s event order broken: %v", member, list)
			}
		}
	}
	if un := m.teamPick.session.unread["lead"]; un != 0 {
		t.Errorf("bound member must not badge from background events, unread[lead] = %d", un)
	}
}

// byteFrame renders a monotone per-sender frame tag so order is comparable.
func byteFrame(i int) string {
	return string(rune('a'+i/10)) + string(rune('0'+i%10))
}
