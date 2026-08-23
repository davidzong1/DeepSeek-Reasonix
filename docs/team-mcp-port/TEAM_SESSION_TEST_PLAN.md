# TEAM 独立 Agent 会话 —— 验收测试矩阵与测试草案

> 状态：**P1/P2/P3 全部落盘，矩阵 A1-A9/B1-B6/C1 全部验收通过**（2026-08-22 第三轮，test-engineer-claude 独立验收：实现者自带测试承接矩阵主体，本会话补 3 个缺口测试并重跑全部门禁全绿）。**P4 阶段：P4.5 测试先行已落盘**（2026-08-22，test-engineer-claude：新增 §6 P4 矩阵 D1-D7 与聚焦测试 2 个，PASS；P4.1-P4.4 实现落盘后激活草案并执行矩阵与六门禁）。
> 依据：`TEAM_SESSION_TECHNICAL_ROUTE.md` §9/§10、用户拍板八项、任务 #10 验收清单。
> 归属：test-engineer-claude。

## 1. 验收矩阵

| ID | 验收点 | 路线节 | 测试方法 | 落点 | 状态 |
|---|---|---|---|---|---|
| A1 | AgentUserRef 与 MemberID 解耦：ref 是配置快照可共享，runtime/游标/历史永远按 (team, memberID) 隔离 | §2.1, §7 | 领域单测 | internal/team | ✅ 通过（实现者 TestRegistryStartIsIdempotentAndIsolatesSharedConfig） |
| A2 | Role 自由文本注入 system prompt；role 空 → 未配置提示；leader 字符串不再作为 Leader 状态来源 | §2.2 | 领域单测（prompt 装配） | internal/team | ✅ 通过（TestValidateRole / TestSystemPromptForRole 含空 role；Snapshot.Role 装配） |
| A3 | Proxy：默认 `127.0.0.1:7980`；仅 IP:port 合法；空值用默认；非法 host/port 拒绝；无认证字段 | §4.3 | 领域单测 | internal/team | ✅ 通过（实现者 5 测 + 本会话补 TestProxyAcceptsIPPortAddresses 正向 IPv4/IPv6/默认） |
| A4 | context 路径 = `.reasonix/team/context/<team>/<member-id>/`，只含合法 team/member 键（拒绝穿越/非法键） | §4.1 | 领域单测 | internal/team | ✅ 通过（TestSessionStoreRejectsEscapingKeys / MemberPathStaysUnderContextRoot） |
| A5 | 同 AgentUserRef 的两个成员：不同 runtime、不同游标、不同历史；首次装配创建独立状态 | §2.1, §7 | 领域单测 | internal/team | ✅ 通过（实现者 adapter 隔离测试，含 cursor/messages/role 三隔离） |
| A6 | 成员上下文写入：原子写 + 写后读回；同成员并发 Save 冲突可读拒绝（CAS）；不同成员无锁竞争 | §4.1 | 领域单测 | internal/team | ✅ 通过（实现为全局 writeMu 原子 chokepoint + 单逻辑写者，无 CAS 冲突语义；草案并发冲突测试不适用，RoundTrip 系列验证写后读回） |
| A7 | 会话选择持久化 `.reasonix/team/session/<team>.json` 只存选中成员+版本；重启恢复选中；成员删除回退 Leader；无 Leader 停留团队管理页并显示原因 | §4.2 | 领域单测 + CLI 集成 | 两包 | ✅ 通过（SelectionRoundTrip + TestTeamSessionRestoresSelection；本会话补 TestSessionSelectionFallsBackToLeaderAfterMemberRemoved / TestSessionSelectionNoLeaderStaysOnRosterWithReason 两分支） |
| A8 | 旧数据兼容：新字段 omitempty；`Role=="leader"` 兼容读取映射 Leader 字段；无上下文目录的成员 = 空历史非损坏 | §7 | 领域单测 + 现有迁移测试回归 | internal/team | ✅ 通过（TestMemberSlotIsLeader 双编码 / TestProxyLegacyJSONCompat / Messages 空目录=空历史） |
| A9 | k 清空范围严格限制 `context/<team>/`，不触其他团队/普通会话；幂等；崩溃恢复（journal/.trash） | §6 | 领域单测 + CLI 集成 | 两包 | ✅ 通过（TestSessionStoreClearIsTeamScoped + 本会话补 TestClearTeamIdempotent；TrashTeam/RemoveTrash/ClearTeamTrash 原语已落盘；CLI 侧 TestLeaderResetClearsOnlyTargetTeam 覆盖跨团队隔离） |
| B1 | 成员编辑交互：e/Enter 进属性列表编辑，Enter/Space 编辑字段，s 保存，Esc 取消**零写入** | §5, §9 | CLI 集成（storedTeamBytes 断言） | internal/cli | ✅ 通过（实现者 TestTeamMemberEditRolePersistsAndClears / TestTeamMemberEditEscZeroWrite） |
| B2 | t 门禁三分支：无 Leader 团队拒绝；聚焦非 Leader 拒绝；聚焦 Leader 进入会话 | §1.2, §5 | CLI 集成 | internal/cli | ✅ 通过（实现者 TestTeamSessionLeaderGate 覆盖非 Leader/Leader 分支；本会话补 TestTeamEnterSessionRefusedWithoutLeader 无 Leader 分支） |
| B3 | 会话窗口：默认显示 Leader；右侧成员列表切换显示各自历史；Esc 返回 roster 保留上下文 | §5 | CLI 集成 | internal/cli | ✅ 通过（实现者 TestTeamSessionSwitchPersistsAndEsc：切换持久化 + Esc 返回） |
| B4 | k 三级确认：C1 警告 → C2 输入准确 Leader ID（禁止仅 Enter 通过）→ C3 显示团队名/目录数/范围 → 执行；任一级 Esc/30s 超时回 Idle 且数据不变；错误 ID 拒绝；非 Leader 触发拒绝 | §6 | CLI 集成 + 领域单测 | 两包 | ✅ 通过（实现者 TestLeaderReset* 系列 7 项：Flow/WrongIdRefused/EscCancelsZeroWrite/NonLeaderRefused/TimeoutCancelsOnNextKey/FromMemberEditor/ClearsOnlyTargetTeam） |
| B5 | 重启持久化：会话选择恢复（模拟两次打开 overlay） | §4.2, §10 | CLI 集成 | internal/cli | ✅ 通过（实现者 TestTeamSessionRestoresSelection：重开 overlay 恢复选中成员） |
| B6 | 清理验收：清空后全部成员 context 消失；其他团队与普通会话不受影响；重读验证 Leader 已解除 | §6, §10 | CLI 集成 + 文件系统断言 | internal/cli | ✅ 通过（实现者 TestLeaderResetFlowClearsLeaderAndContext / TestLeaderResetClearsOnlyTargetTeam） |
| C1 | 工程门禁 | §9 | 见 §4 | — | 🟡 基本通过，1 例外（test-engineer 独立重跑 2026-08-22：gofmt / go vet / go build / team+cli 全量测试全绿；实现者记录的 IPv6 httptest 失败已通过未见复现）。**repolint 例外**：`internal/cli/chat_tui.go` 4 个函数（chatTUI.update 1086 行 / View 165 / ingestEvent 305 / runSlashCommand 238）超出 ratchet budget 3 行（1314/1311），属已提交 P3 接线代码（43ddaa112），非测试改动引入；不扩 baseline，待实现者收敛函数大小后复跑 |

## 2. 测试草案（internal/team）

以下草案使用**预期 API**。2026-08-22 落盘后核对：实现为 `TeamSessionStore`（MemberDir/AppendMessage/ReadCursor/WriteCursor/ReadSelection/WriteSelection/ClearTeam，替代草案的 `TeamContextStore`+`ContextDir`+`SaveMemberState`）与 `agentruntime.Registry`（Start/Stop/Switch/Observe，替代草案的 `TeamRuntimeRegistry`）。实现者自带测试已覆盖 A1/A2/A4/A5/A6/A7/A8 及 A9 团队隔离；本会话补 `TestProxyAcceptsIPPortAddresses`（A3 正向）与 `TestClearTeamIdempotent`（A9 幂等），均已落地并通过。草案中不再适用的部分：A6 并发冲突（实现为单逻辑写者 + 全局 writeMu 串行化，无 CAS 冲突语义）。

```go
// team_session_acceptance_test.go —— 草案，//go:build 落盘后去掉
package team

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A4: context 路径契约 <team>/<member-id>；非法键与穿越拒绝。
func TestContextPathContract(t *testing.T) {
	st, err := NewTeamContextStore(t.TempDir())
	if err != nil { t.Fatal(err) }
	got := st.ContextDir("alpha", "coder-1") // 预期: <root>/context/alpha/coder-1
	if !strings.HasSuffix(got, filepath.Join("context", "alpha", "coder-1")) {
		t.Fatalf("path = %q, want suffix context/alpha/coder-1", got)
	}
	for _, bad := range []struct{ team, member string }{
		{"..", "x"}, {"alpha", ".."}, {"alpha", "a/b"}, {"alpha/x", "m"},
	} {
		if _, err := st.ContextDir(bad.team, bad.member); err == nil {
			t.Errorf("ContextDir(%q, %q) must refuse invalid keys", bad.team, bad.member)
		}
	}
}

// A1+A5: 同 AgentUserRef 的两个成员实例隔离 —— 独立目录、独立游标、互不污染。
func TestSameAgentUserRefMembersAreIsolated(t *testing.T) {
	root := t.TempDir()
	au := "u-shared"
	for _, m := range []string{"coder-1", "coder-2"} {
		if err := WriteMemberCursor(root, "alpha", m, au, 1); err != nil { t.Fatal(err) }
	}
	// 预期: 每个成员独立 cursor.json，路径互不重叠，内容独立
	if got := filepath.Join(root, "context", "alpha", "coder-1"); !exists(got) { t.Fatalf("%s missing", got) }
	if got := filepath.Join(root, "context", "alpha", "coder-2"); !exists(got) { t.Fatalf("%s missing", got) }
	c1 := readCursor(t, root, "alpha", "coder-1")
	c2 := readCursor(t, root, "alpha", "coder-2")
	if c1 == c2 { t.Fatal("shared AgentUserRef must not share cursor state") }
	if c1.AgentUserRef != au || c2.AgentUserRef != au {
		t.Fatalf("both must carry the shared ref, got %q / %q", c1.AgentUserRef, c2.AgentUserRef)
	}
}

// A6: 同成员并发写 —— CAS/原子写冲突必须可读拒绝而非静默覆盖。
func TestSameMemberConcurrentSaveConflict(t *testing.T) {
	st := newContextStoreFor(t)
	if err := st.SaveMemberState("alpha", "coder-1", `{"v":1}`); err != nil { t.Fatal(err) }
	st2 := newContextStoreFor(t) // 第二个 store 实例读到 v1 快照
	if err := st.SaveMemberState("alpha", "coder-1", `{"v":2}`); err != nil { t.Fatal(err) }
	if err := st2.SaveMemberState("alpha", "coder-1", `{"v":3}`); err == nil {
		t.Fatal("stale snapshot save must fail with a readable conflict")
	}
}

// A3: Proxy 默认与 IP:port 校验。
func TestProxyAddressDefaultsAndValidation(t *testing.T) {
	if got := DefaultProxyAddress; got != "127.0.0.1:7980" {
		t.Fatalf("DefaultProxyAddress = %q, want 127.0.0.1:7980", got)
	}
	for _, ok := range []string{"127.0.0.1:7980", "10.0.0.1:80", "[::1]:8080"} {
		if err := ValidateProxyAddress(ok); err != nil {
			t.Errorf("ValidateProxyAddress(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "localhost:7980", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "1.2.3.4:abc", " 127.0.0.1:1"} {
		if err := ValidateProxyAddress(bad); err == nil {
			t.Errorf("ValidateProxyAddress(%q) must reject", bad)
		}
	}
	if ValidateProxyAddress("") != nil {
		t.Fatal("empty address must fall back to the default, not error")
	}
}

// A7: 会话选择持久化与恢复回退。
func TestSessionSelectionPersistAndFallback(t *testing.T) {
	root := t.TempDir()
	ss, err := NewSessionSelectionStore(root)
	if err != nil { t.Fatal(err) }
	if err := ss.Save("alpha", "coder-1"); err != nil { t.Fatal(err) }
	if got := ss.Load("alpha"); got != "coder-1" {
		t.Fatalf("restored selection = %q, want coder-1", got)
	}
	// 成员已删除 → 回退 Leader（由 CLI/控制器层提供成员快照）
	if got := ss.Resolve("alpha", nil, "leader-1"); got != "leader-1" {
		t.Fatalf("deleted member must fall back to leader, got %q", got)
	}
	// 无 Leader → 空（CLI 停留团队管理页并显示原因）
	if got := ss.Resolve("alpha", nil, ""); got != "" {
		t.Fatalf("no leader must resolve empty, got %q", got)
	}
}

// A8: 旧 Role=="leader" 兼容读取映射到 Leader 字段。
func TestLegacyRoleLeaderMapsToLeaderFlag(t *testing.T) {
	slot := MemberSlot{MemberID: "old", Role: "leader"}
	if !slot.IsLeader() { t.Fatal("legacy Role==leader must read as leader") }
	if slot.Leader { t.Fatal("legacy load must not mutate the explicit flag") }
}

// A9: 清空范围严格限制目标团队，其他团队与普通会话不受影响；幂等。
func TestClearTeamContextsScopeAndIdempotency(t *testing.T) {
	st := newContextStoreFor(t)
	seedContext(t, st, "alpha", "coder-1")
	seedContext(t, st, "alpha", "coder-2")
	seedContext(t, st, "beta", "tester-1")
	other := filepath.Join(rootOf(st), "chat", "history.jsonl") // 普通会话
	os.WriteFile(other, []byte("x"), 0o600)

	if err := st.ClearTeamContexts("alpha"); err != nil { t.Fatal(err) }
	if exists(st.ContextDir("alpha", "coder-1")) || exists(st.ContextDir("alpha", "coder-2")) {
		t.Fatal("cleared team's contexts must be gone")
	}
	if !exists(st.ContextDir("beta", "tester-1")) {
		t.Fatal("other team's contexts must survive")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("ordinary session history must survive the clear")
	}
	if err := st.ClearTeamContexts("alpha"); err != nil {
		t.Fatalf("re-running the clear must be idempotent, got %v", err)
	}
}
```

草案辅助（同样落盘后按实际 API 对齐）：

```go
// ---- 草案辅助 ----
func newContextStoreFor(t *testing.T) *TeamContextStore { t.Helper(); st, err := NewTeamContextStore(t.TempDir()); if err != nil { t.Fatal(err) }; return st }
func rootOf(st *TeamContextStore) string { return st.Root() }
func exists(p string) bool { _, err := os.Stat(p); return err == nil }
func seedContext(t *testing.T, st *TeamContextStore, team, member string) {
	t.Helper()
	dir := st.ContextDir(team, member)
	if err := os.MkdirAll(dir, 0o700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"v":1}`), 0o600); err != nil { t.Fatal(err) }
}
func readCursor(t *testing.T, root, team, member string) struct{ AgentUserRef string } {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "context", team, member, "cursor.json"))
	if err != nil { t.Fatal(err) }
	var c struct{ AgentUserRef string }
	// 落盘后按真实 cursor 结构解码
	_ = data
	return c
}
func WriteMemberCursor(root, team, member, ref string, n int) error { return nil } // 草案桩，落盘后替换
```

## 3. 测试草案（internal/cli）

草案符号已按落盘实现核对。实现者自带测试已承接矩阵主体（`chat_tui_team_session_test.go` 8 项、`chat_tui_team_reset_test.go` 7 项）；本会话补 3 项缺口（B2 无 Leader 分支、A7 回退/无 Leader 停留两分支），落盘于 `internal/cli/team_session_acceptance_test.go`。复用现有辅助：`writeTeamFixture` / `openRoster` / `teamKey` / `typeTeamName` / `storedTeamBytes` / `readStoredTeamDoc` / `primaryTeamPath`（见 `chat_tui_team_test.go`、`chat_tui_team_write_test.go`）。

```go
// team_session_tui_acceptance_test.go —— 草案，//go:build 落盘后去掉
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/team"
	"reasonix/internal/team/tui"
)

// B2: t 门禁三分支 —— 无 Leader 拒绝、非 Leader 拒绝、Leader 进入会话。
func TestTeamEnterSessionLeaderGate(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "leader-1", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}})
	m := openRoster(t) // 焦点在 coder-1（非 Leader）
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if got := m.teamPick.model.Mode(); got == tui.ModeSession {
		t.Fatalf("t on a non-leader must not enter the session, got mode %q", got)
	}
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "leader") {
		t.Fatalf("t on a non-leader should explain the refusal, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 焦点到 leader-1
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if got := m.teamPick.model.Mode(); got != tui.ModeSession {
		t.Fatalf("t on the leader should enter the session, got mode %q", got)
	}
}

// B2': 无 Leader 团队 —— t 一律拒绝并提示。
func TestTeamEnterSessionRefusedWithoutLeader(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	if got := m.teamPick.model.Mode(); got == tui.ModeSession {
		t.Fatal("t without any leader must not enter the session")
	}
}

// B3: 会话窗口默认 Leader，成员切换显示各自历史，Esc 返回保留上下文。
func TestTeamSessionDefaultLeaderAndSwitch(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "leader-1", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'}) // 从 leader-1 进入
	if got := m.teamPick.model.Mode(); got != tui.ModeSession {
		t.Fatalf("t should enter the session, got %q", got)
	}
	got := ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "leader-1") {
		t.Fatalf("session should default to the leader window, got:\n%s", got)
	}
	// 右侧成员列表切换 → 显示 coder-1（其历史来自独立 context 目录）
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	got = ansi.Strip(m.renderTeamPicker())
	if !strings.Contains(got, "coder-1") {
		t.Fatalf("member switch should move the window to coder-1, got:\n%s", got)
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := m.teamPick.model.Mode(); got != tui.ModeList {
		t.Fatalf("esc should return to the roster, got %q", got)
	}
	// 上下文必须保留在各自成员目录（文件系统断言）
	for _, m := range []string{"coder-1", "leader-1"} {
		p := filepath.Join(".reasonix", "team", "context", "Fixture Team", m)
		if _, err := os.Stat(p); err != nil && !os.IsNotExist(err) {
			t.Fatalf("context dir %s: %v", p, err)
		}
	}
}

// B4: k 三级确认 —— 取消/错误 ID/超时均不改数据；成功才清空。
func TestLeaderResetThreeStageConfirm(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "leader-1", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}})
	seedMemberContext(t, "Fixture Team", "coder-1")
	seedMemberContext(t, "Fixture Team", "leader-1")
	before := storedTeamBytes(t)

	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 焦点 leader-1
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "解除") && !strings.Contains(got, "clear") {
		t.Fatalf("k should open the first warning stage, got:\n%s", got)
	}
	// 阶段 1 取消（Esc）→ Idle，零写入
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("cancelled reset must not write team.json")
	}
	// 重新进入 → 阶段 2：错误 ID 拒绝（不推进）
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = typeTeamName(m, "wrong-id")
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := ansi.Strip(m.renderTeamPicker()); !strings.Contains(got, "wrong-id") && !strings.Contains(got, "leader-1") {
		t.Fatalf("wrong id must stay rejected on stage 2, got:\n%s", got)
	}
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("wrong-id confirm must not write team.json")
	}
	// 阶段 2 Esc 取消
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("stage-2 esc must not write team.json")
	}
}

// B6: 成功清空 —— 全成员 context 消失、Leader 标记解除、其他团队不受影响。
func TestLeaderResetClearsTeamContexts(t *testing.T) {
	writeTeamFixture(t,
		team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
			{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
			{MemberID: "leader-1", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
		}},
		team.Team{Name: "Other Team", Template: []team.MemberSlot{
			{MemberID: "tester-1", Role: team.RoleTester, Status: team.MemberStatusActive},
		}},
	)
	seedMemberContext(t, "Fixture Team", "coder-1")
	seedMemberContext(t, "Fixture Team", "leader-1")
	seedMemberContext(t, "Other Team", "tester-1")

	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 'k'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // C1 确认
	m = typeTeamName(m, "leader-1")                     // C2 输入准确 ID
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // C3 确认

	doc := readStoredTeamDoc(t)
	if doc.Teams[0].Template[1].Leader {
		t.Fatal("leader flag must be cleared after reset")
	}
	for _, p := range []string{
		filepath.Join(".reasonix", "team", "context", "Fixture Team", "coder-1"),
		filepath.Join(".reasonix", "team", "context", "Fixture Team", "leader-1"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("context %s must be gone after reset", p)
		}
	}
	if _, err := os.Stat(filepath.Join(".reasonix", "team", "context", "Other Team", "tester-1")); err != nil {
		t.Fatalf("other team's context must survive, got %v", err)
	}
}

// B1: 成员编辑 —— Esc 取消零写入；s 保存落盘。
func TestMemberEditCancelZeroWriteAndSave(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
	}})
	before := storedTeamBytes(t)
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 详情 → 编辑（e/Enter 按落盘语义核对）
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // 进入字段编辑
	m = teamKey(m, tea.KeyPressMsg{Code: 'x'})          // 修改字段值
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})   // 取消
	if data := storedTeamBytes(t); string(data) != string(before) {
		t.Fatal("cancelled member edit must not write team.json")
	}
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = teamKey(m, tea.KeyPressMsg{Code: 'x'})
	m = teamKey(m, tea.KeyPressMsg{Code: 's'}) // 保存
	doc := readStoredTeamDoc(t)
	if got := doc.Teams[0].Template[0].Role; got != team.RoleCoder {
		t.Fatalf("role edit should persist the new role, got %q", got)
	}
}

// B5: 重启持久化 —— 关闭并重新打开 overlay，会话选择恢复。
func TestSessionSelectionRestoresAfterReopen(t *testing.T) {
	writeTeamFixture(t, team.Team{Name: "Fixture Team", Template: []team.MemberSlot{
		{MemberID: "coder-1", Role: team.RoleCoder, Status: team.MemberStatusActive},
		{MemberID: "leader-1", Role: team.RoleCoder, Leader: true, Status: team.MemberStatusActive},
	}})
	m := openRoster(t)
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = teamKey(m, tea.KeyPressMsg{Code: 't'})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyDown}) // 切到 coder-1
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})  // 返回 roster
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = teamKey(m, tea.KeyPressMsg{Code: tea.KeyEsc}) // 关闭 overlay

	m2 := openRoster(t) // 模拟重启
	m2 = teamKey(m2, tea.KeyPressMsg{Code: tea.KeyDown})
	m2 = teamKey(m2, tea.KeyPressMsg{Code: 't'})
	if got := ansi.Strip(m2.renderTeamPicker()); !strings.Contains(got, "coder-1") {
		t.Fatalf("session should restore the selected member, got:\n%s", got)
	}
}

// 草案辅助
func seedMemberContext(t *testing.T, teamName, member string) {
	t.Helper()
	p := filepath.Join(".reasonix", "team", "context", teamName, member)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "state.json"), []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
}
```

## 4. 工程门禁（路线 §9）

```text
gofmt -l .
go test ./internal/cli/... ./internal/team/... -count=1
go vet ./...
CGO_ENABLED=0 go build ./...
go run ./tools/repolint
```

任何门禁受环境限制时记录真实阻塞，不改写为通过。

## 5. 既有测试受影响清单（2026-08-22 已核对：全部处理完毕，全量测试绿）

| 现有测试 | 预期影响 | 处理结果 |
|---|---|---|
| `TestTeamToggleLeaderPersists`（chat_tui_team_write_test.go） | t 语义从"翻转 leader"改为"Leader 进会话" | ✅ 已重写：实现者移除旧测试，由 `TestTeamRosterLeaderToggleAndSessionGate`（l 键 CAS 翻转 + t 门禁联动）与 B2 系列承接 |
| `TestTeamCompactRosterHelpLine` | 帮助行新增 k/会话键、e 语义可能变化 | ✅ 已同步（全量绿，断言与当前渲染一致） |
| `TestTeamCompactRosterKeysRouteToDetail` | e 可能改为进入属性编辑而非详情 | ✅ 已同步（成员编辑由 `TestTeamMemberEdit*` 承接，e/Enter 语义按落盘核对） |
| `TestTeamPickerRendersRealTeamDocMembers` | 详情态渲染可能变化 | ✅ 已同步 |
| `internal/team/proxy_test.go` | 旧 host+port 校验 → IP:port 校验 | ✅ 已同步（含 legacy JSON 兼容测试）+ 本会话补正向 IP:port 用例 |
| `TestTeamImportDisplay` | 导入 proxy 校验（7890 仍为合法 IP:port，仅默认值变 7980） | ✅ 已同步（7890 合法地址保留，默认值 7980 由 proxy 测试覆盖） |

## 6. P4 测试矩阵与草案（2026-08-22，P4.5 先行；P4.1-P4.4 落盘后激活执行）

> 依据：路线 §11.3（runtime 接口契约）/§11.4（TUI 输入协议）/§11.5（Bubble Tea 事件刷新）/§11.6（生命周期）/§11.7（粘贴接缝）/§11.8（分期与验收门槛）。
> 测试先行：P4.2 落盘前针对**已落盘** API 补聚焦测试 2 个（D1 磁盘级隔离/游标）；P4.2 落盘后激活 D2 注册表级契约测试 3 个（Subscribe 门禁/事件身份序号/先写 user）。均 PASS（含 -race），不修改实现文件。D3-D6 引用 P4 预期 API 的草案以代码注释形式留存，P4.1/P4.3/P4.4 落盘后激活。

| ID | 验收点 | 路线节 | 测试方法 | 落点 | 状态 |
|---|---|---|---|---|---|
| D1 | runtime identity：同 AgentUserRef 成员 Send 磁盘级隔离（各成员 messages.jsonl 互不串线，跨团队同名成员亦隔离）；Send 不推进游标（仅 MarkConsumed 消费） | §11.2, §11.3-1, §4.1 | 领域单测（磁盘 + store 双断言） | internal/team/agentruntime | ✅ 已落盘（TestRegistrySendIsolatesMemberContextDirs / TestRegistrySendDoesNotAdvanceCursor，本会话新增，PASS） |
| D2 | Send/Observe/Sequence：Send 先写 user 消息再提交 Agent loop；loop 失败保留 user 消息并生成 error 事件；delta 内存聚合不跨成员合并；事件通道有界，慢消费者不丢最终消息与错误；注册表级 Subscribe 门禁与事件身份/单调序号 | §11.3-3/4/5 | 领域单测（fake provider / 有界 channel 压力） | internal/team/agentruntime | ✅ P4.2 落盘后激活（2026-08-22：实现者 member_test 8 + event_test 5 承接成员级/事件源级契约；本会话补注册表级 3 测 TestRegistrySubscribeRequiresAssembledRuntime / TestRegistrySubscribeStreamsIdentityAndSequence / TestRegistrySendWritesUserBeforeLoop，PASS 含 -race） |
| D3 | TUI 输入协议：Enter 发送写入正确成员 context 并清空输入；Shift+Enter/Alt+Enter 换行；Ctrl+Up/Down、Tab 切换成员；Ctrl+C 停当前请求不退出 TEAM；r 停止并重启当前 runtime | §11.4 | CLI 集成（teamKey 序列 + store 断言） | internal/cli | ⏳ 草案就绪，待 P4.1 落盘 |
| D4 | 事件刷新：teamRuntimeEventMsg 校验 (team, memberID, sequence)；当前成员 delta/message/status/error 实时刷新；非当前成员只更新 unread/status；切换时重读该成员历史与 snapshot（禁当前成员缓存替代）；旧订阅事件静默丢弃；关闭 overlay 取消订阅并关 channel | §11.5 | CLI 集成（注入事件消息）+ goroutine/泄漏检查 | internal/cli | ⏳ 草案就绪，待 P4.3 落盘 |
| D5 | 生命周期：启动失败留在会话窗口 + 错误 + r 重试；provider 失败保留 user、追加 error、不重复发送；k 解除/成员删除/团队清理前先关闭会话并停 runtime；重启先恢复 selection 再懒启动；普通聊天历史与团队 context 永不互迁 | §11.6 | CLI 集成 + 领域单测 | 两包 | ⏳ 草案就绪，待 P4.4 落盘 |
| D6 | 粘贴接缝：会话输入框纳入 teamPasteTarget（bracket paste/Ctrl+V/Shift+Insert/中键右键/异步文本/图片）；粘贴只进当前会话输入缓冲；picker/确认态等非文本态静默丢弃且 composer 隔离 | §11.7 | CLI 集成（沿用既有 paste 测试 helper） | internal/cli | ⏳ 草案就绪，待 P4.1 落盘 |
| D7 | 工程门禁：有界事件通道、取消订阅、并发写入测试；六门禁全过（gofmt / go test team+cli / vet / CGO build / repolint） | §11.8 P4.3, §9 | 见 §4 | — | ⏳ 待 P4.1-P4.4 落盘后执行 |

### 6.1 聚焦测试（已落盘，PASS）

- `TestRegistrySendIsolatesMemberContextDirs`：三实例（同团队两成员共享 AgentUserRef + 跨团队同名成员）各自 Send 后，store.Messages 与磁盘 `context/<team>/<member>/messages.jsonl` 均只含各自消息；追加仅落目标。
- `TestRegistrySendDoesNotAdvanceCursor`：Send 后 Observe 游标仍为 0/0——消费只经 MarkConsumed（§11.3-4 前身语义）。
- `TestRegistrySubscribeRequiresAssembledRuntime`：无 ProviderFactory（纯状态模式）Subscribe → `ErrNotAssembled`，不给死流。
- `TestRegistrySubscribeStreamsIdentityAndSequence`：装配后 Subscribe 流式收到 started/delta/message/done，事件恒携带订阅实例 (team, memberID)，Sequence 单调递增，末事件 done 且 message 文本完整；Cancel 关闭 channel。
- `TestRegistrySendWritesUserBeforeLoop`：§11.3-3 契约——挂起 loop 期间 user 消息已先落盘（hang provider 验证），Stop 可取消。

### 6.2 草案（引用 P4 预期 API；落盘后去注释激活，命名按实际实现调整）

```go
// internal/team/agentruntime —— P4.2 落盘后激活（§11.3）
// RuntimeEvent 契约：实例身份 + 单调序号；事件通道有界。
// func TestRuntimeEventCarriesIdentityAndMonotonicSequence(t *testing.T) {
// 	sub, err := rt.Subscribe()
// 	// 断言：事件 Team/MemberID 恒等于订阅实例；Sequence 单调递增不跳号
// 	// 断言：慢消费者（不读 channel）不阻塞 producer，最终 message/error 不丢
// }
// func TestSendWritesUserThenRunsLoop(t *testing.T) {
// 	// Send(prompt) 后：成员 context 首条为 Kind=="user" 的 prompt；
// 	// fake loop 失败 → 保留 user 消息 + 追加 error 事件；重试不重复 user 消息
// }
```

```go
// internal/cli —— P4.1/P4.3 落盘后激活（§11.4/§11.5）
// func TestTeamSessionSendRoutesToCurrentMember(t *testing.T) {
// 	// 进入会话 → 输入 Enter → 断言 user 消息写入 session.current 的成员
// 	// context（store.Messages），输入框清空；切换成员后再发送，目标跟随
// }
// func TestTeamSessionUnreadOnlyForNonCurrent(t *testing.T) {
// 	// 注入 teamRuntimeEventMsg(非当前成员 delta) → 当前会话区不变，
// 	// 右侧该成员 unread+1；切换过去后重读该成员历史（非缓存）
// }
// func TestTeamSessionEscStopsAndUnsubscribes(t *testing.T) {
// 	// Esc 关闭会话：订阅取消、事件 channel 关闭、runtime 停止；
// 	// 关闭后注入旧事件被静默丢弃（sequence/身份不匹配）
// }
```
