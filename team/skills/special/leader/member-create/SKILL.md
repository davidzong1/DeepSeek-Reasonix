---
name: member-create
team role: leader
description: Add members to a Reasonix team with leader_add_member, the leader-only member-create tool. A member is created with the full non-leader attribute set; agent-user credentials follow the binding strategy (pin a pool entry, assign a custom pool, or inherit the team pool head). The leader property is never creatable.
---

# Member Create

Agent-user credentials follow the binding strategy (bind a pool entry or inherit the team pool head). The leader property is never creatable. A member slot is the
team's fixed topology: identity, role, lifecycle state, and binding to an
agent-user pool entry. A member is created regular and active; everything else
about how it runs is set here or inherits.

## Required

- `member_id` — the only required attribute. The id is the member's identity
 inside the team; a duplicate or blank id is refused.

## Non-leader attributes (all optional)

- `role` — the business role the member plays (coder, reviewer, tester, ...).
 Role-less members render an unconfigured hint at prompt assembly; prefer
 setting one at creation.
- `agent_user_ref` — pins the member to one pool entry: its provider, model,
 and credentials come from that entry. Prefer the default when unsure: with
 no pin the member inherits the team's pool head — the same credential config
 the team dials by default.
- `agent_type` — launch-type override (`claude`, `codex`, or a custom launch
 type). Empty inherits the team default launch type.
- `proxy_enabled` — force the team proxy on (`true`) or off (`false`) for this
 member. Omitted inherits the team default.
- `agent_user_pool` — a dedicated custom pool for this member (its own list of
 pool entries;
 the first is the member's dialing head). Omit to inherit the
 team pool. Every entry must exist in the pool, and the entries replace
 inheritance — never combine with `agent_user_ref`.

## Never creatable

- The **leader property** is leader-only in both encodings — the `leader`
 flag and the legacy role value `leader` are refused. Members are created
 regular; leadership is granted separately through the leader management
 flow. The leader's approval mode is fixed to auto for the same reason.
- Lifecycle state: a new member joins the active roster; status changes after
 creation through lifecycle operations.

## Workflow

1. Check the pool before adding a member the team must run: a session cannot
 start while the member has no pool entry to dial (its own custom pool, a
 pin, or the team pool head). Pool entries are configured through the team
 management UI — an empty pool refuses the session until one exists.
2. Create with the defaults whenever they fit — inheritance is the strategy,
 not a fallback.
3. Verify the created member with `leader_list_team` before dispatching work.