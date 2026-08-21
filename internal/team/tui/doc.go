// Package tui is the team management view model (§3.2, §8.2 dim 1): the team
// list, a team's member roster ordered by state priority, one member's context
// view, and exit semantics. It is transport-agnostic — no terminal I/O, no
// tmux or spawn — so a frontend (the CLI [ TEAM ] view) maps keypresses to
// Events and renders the model state.
//
// Layering: tui imports internal/team only, never a frontend. The model owns
// ordering and focus; lifecycle stays with the domain layer.
//
// Reference: docs/team-mcp-port/TASK.md §2.2, §3.2, §4 P4.
package tui
