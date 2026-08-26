package agentruntime

import (
	"strconv"
	"strings"

	"reasonix/internal/team"
)

// Link is one auditable step of the assembled context chain (§2.10): the
// step's name, its source reference, and its body. The chain is assembled
// in order and never reordered.
type Link struct {
	Name string
	Ref  string
	Body string
}

// AssembledContext is the injected prompt plus the chain that produced it.
// The chain stays inspectable — every step records where its content came
// from (route §7).
type AssembledContext struct {
	Links []Link
	Text  string
}

// InjectTask assembles the §7 context chain for one task: private task
// context, the current generation's durable inbox commands, and the board
// view, in that order. Task id and generation ride as structured labels —
// display hints for the model; the board's stamped identity is the ACL
// boundary, never the text (route §6.2).
func InjectTask(task team.Task, inbox []InboxItem, boardView string) AssembledContext {
	links := make([]Link, 0, 3)
	var sb strings.Builder
	taskBody := task.Desc
	if task.Expected != "" {
		taskBody += "\n[expected] " + task.Expected
	}
	links = append(links, Link{Name: "task", Ref: string(task.ContextRef), Body: taskBody})
	sb.WriteString("[task: " + string(task.ID) + "]\n" + taskBody + "\n")
	if len(inbox) > 0 {
		var b strings.Builder
		for _, item := range inbox {
			b.WriteString("[task: " + string(item.TaskID) + "] " + item.Summary + "\n")
		}
		links = append(links, Link{Name: "inbox", Ref: inboxRef(inbox), Body: strings.TrimSuffix(b.String(), "\n")})
		sb.WriteString("[command inbox] (generation " + strconv.FormatUint(inbox[0].Generation, 10) + ")\n" + b.String())
	}
	if boardView != "" {
		links = append(links, Link{Name: "board", Ref: "board:latest", Body: boardView})
		sb.WriteString("[board view]\n" + boardView + "\n")
	}
	return AssembledContext{Links: links, Text: strings.TrimSuffix(sb.String(), "\n")}
}

// inboxRef names the source range of the injected commands by seq.
func inboxRef(items []InboxItem) string {
	return "board:seq:" + strconv.FormatInt(items[0].Seq, 10) + "-" + strconv.FormatInt(items[len(items)-1].Seq, 10)
}
