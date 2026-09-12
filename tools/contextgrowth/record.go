package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// delegationTools fan work out. A result reached through one is fan-out shaped
// by construction, so it counts however few times the name appears.
var delegationTools = map[string]bool{
	"task": true, "parallel_tasks": true, "fleet": true,
	"orchestrate": true, "read_subagent_result": true,
}

// localOnlyTool is a display-only sentinel (provider.LocalOnlyToolName), not a
// call. Counting it as a tool result would attribute growth to nothing.
const localOnlyTool = "__reasonix_local_only__"

const (
	roleTool       = "tool"
	roleAssistant  = "assistant"
	capabilityTool = "use_capability"
)

// fanoutRepeats is the pre-registered threshold: a tool name appearing this many
// times in one session is fan-out rather than a one-off read.
const fanoutRepeats = 3

// message is the subset of a transcript line this harness reads. Every other
// field is ignored on purpose, so a schema addition cannot change a verdict.
type message struct {
	Role             string     `json:"role"`
	Name             string     `json:"name"`
	ToolCallID       string     `json:"tool_call_id"`
	Content          string     `json:"content"`
	RawContent       string     `json:"raw_content"`
	ReasoningContent string     `json:"reasoning_content"`
	ToolCalls        []toolCall `json:"tool_calls"`
}

// toolCall carries name and arguments flat. The OpenAI-shaped nested `function`
// object is not what these transcripts write, so there is no fallback to it.
type toolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// session is one transcript's attributed growth, in bytes of message content.
type session struct {
	Path        string
	Messages    int
	ToolBytes   int64
	AssistBytes int64
	FanoutBytes int64
	// DelegatedBytes is the subset of ToolBytes produced by a delegation tool,
	// direct or via the capability channel. It asks a different question than
	// FanoutBytes: whether the workload contains fan-out at all (§13 P0).
	DelegatedBytes int64
	ToolCounts     map[string]int
}

// body returns the provider-visible content, which IS the context P0 asks
// about. raw_content is the fuller local original and overstates it: one
// recorded grep result is 2.8 MB locally against 19 KB on the wire, because the
// tool layer already truncates at a fixed byte budget. -local-original switches
// to that other question deliberately.
func (m message) body(localOriginal bool) string {
	if localOriginal && m.RawContent != "" {
		return m.RawContent
	}
	return m.Content
}

// delegationsResolved maps the tool_call id of each use_capability call that
// names a delegation tool to true, so the result of that call can be attributed
// the way §13 P0's prose asks for. Only these calls produce that id: a call to a
// delegation tool directly produces a result named after the tool, which
// delegationTools already catches.
func delegationsResolved(msgs []message) map[string]bool {
	out := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if c.Name != capabilityTool {
				continue
			}
			var args struct {
				CapabilityID string `json:"capability_id"`
			}
			if err := json.Unmarshal([]byte(c.Arguments), &args); err != nil {
				continue
			}
			if namesDelegation(args.CapabilityID) {
				out[c.ID] = true
			}
		}
	}
	return out
}

// namesDelegation reports whether a capability id targets a delegation tool,
// with or without the `tool:` prefix the capability channel uses.
func namesDelegation(id string) bool {
	id = strings.TrimPrefix(strings.TrimSpace(id), "tool:")
	return delegationTools[id]
}

// readSession is the recorder: one transcript to one attributed series. It makes
// two passes, because whether a tool result is fan-out shaped depends on how
// often that tool is called across the whole session.
func readSession(path string, localOriginal bool) (session, error) {
	s := session{Path: path, ToolCounts: map[string]int{}}
	msgs, err := decodeMessages(path)
	if err != nil {
		return s, err
	}
	s.Messages = len(msgs)
	resolved := delegationsResolved(msgs)
	for _, m := range msgs {
		if m.Role == roleTool && m.Name != "" && m.Name != localOnlyTool {
			s.ToolCounts[m.Name]++
		}
	}
	for _, m := range msgs {
		size := int64(len(m.body(localOriginal)))
		switch m.Role {
		case roleTool:
			if m.Name == localOnlyTool {
				continue
			}
			s.ToolBytes += size
			direct := delegationTools[m.Name]
			if direct || resolved[m.ToolCallID] {
				s.DelegatedBytes += size
			}
			if isFanout(m.Name, direct, resolved[m.ToolCallID], s.ToolCounts) {
				s.FanoutBytes += size
			}
		case roleAssistant:
			s.AssistBytes += size + int64(len(m.ReasoningContent))
		}
	}
	return s, nil
}

// isFanout applies the pre-registered narrow test: a delegation result reached
// directly or through the capability channel, or a name repeated at least
// fanoutRepeats times in this session.
func isFanout(name string, direct, viaCapability bool, counts map[string]int) bool {
	if name == "" {
		return false
	}
	if direct || viaCapability {
		return true
	}
	return counts[name] >= fanoutRepeats
}

func decodeMessages(path string) ([]message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var m message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m.Role == "" {
			continue
		}
		out = append(out, m)
	}
	return out, sc.Err()
}
