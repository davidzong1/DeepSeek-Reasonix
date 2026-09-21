# 成员用量发布契约（`.usage.json`）

范围：CLI 团队会话窗口在绑定某个成员时，把**该成员的用量**（上下文占用、压缩阈值、最近一次
provider usage、会话累计 cache、后台 job）显示在状态栏。当该成员的会话**写者**是本进程时，窗口
手里就是真实 controller；当写者是**别的进程**（或本进程的 ambient 聊天）时，窗口挂的是只读
`*memberFollowerBackend`，它自己没有 provider、也没有上下文可报——所以数字必须由写者发布。

## 通道

`<teamDataRoot>/<teamID>/<memberID>/.usage.json`，与 `.meta.json`、transcript 同级的 host-only
控制文档（schema 版本化，走 `Document{SchemaVersion}` 约定）：

| 字段 | 含义 |
| --- | --- |
| `published_at` | 发布时刻（RFC3339Nano UTC），读者以它判定"写者是否还在" |
| `context_used` / `context_window` | 上下文 gauge |
| `compact_ratio` | 自动压缩阈值（gauge 的余量基准） |
| `last_turn` | 最近一次 provider usage（cache hit/miss 来自这里） |
| `session_cache_hit` / `session_cache_miss` | 会话累计 cache |
| `jobs[]` | 写者正在跑的后台 job 展示数据 |

**为什么是兄弟文件而不是 `.meta.json` 的一个字段**：`.meta.json` 一旦读不出来，所有读者
（`Fingerprint` → `Corrupt`）都会 fail-closed，跨窗口 history-sync 直接停摆；它还承载 adoption
争用的 CAS revision；而遥测的写入频率远高于历史身份。兄弟文件把这三件事隔开。

## 规则

1. **只有写者发布**：`memberUsagePublisher` 只在 `newMemberBackendBuilder` 的可写分支（拿到会话
   写权限之后）启动，follower 路径根本没有这个对象——这是调用图的性质，不是运行时判断。
2. **写者自持节奏**：每 2s 一次（`memberUsagePublishInterval`），初次启动立即发布一次。节奏**不挂**
   TUI 的 1s roster tick：离开团队（Ctrl+T）会保留成员后端继续跑，tick 却停了。
3. **ambient 写者同样发布**：leader 窗口里 `leader-agent` 这个成员的 canonical 会话就是本进程的
   ambient 聊天，因此它的写者在本进程。tick 里比对"绑定成员的 owner stem == ambient `HistoryStamp()`"
   成立时，对该 owner key 起同一个 publisher；解绑/退出团队即停。
4. **读者按 TTL 隐藏**：`followerUsageTTL = 30s`。写者停止后数字 ≤30s 内消失，不做"最后已知值"
   展示——状态栏的数是"当前真实"，宁缺勿假。时钟超前按 age 0 处理；stamp 不可解析一律视为过期。
5. **读侧永不 fail-closed**：缺失、超大、坏 JSON、schema 不符、owner 目录不存在，一律
   `(zero, false, nil)`。读节流 1s（状态栏每帧都读这五个方法），但新鲜度每次调用都判，所以数字在
   TTL 边界立刻消失。五个方法必须 total：状态栏的 `status_footer.go` 里有无 recover 的直读。
6. **退休顺序**：`memberLeasedBackend.Close()` 先停 publisher 并等待在写的那次落盘，再关
   controller、最后释放租约——不允许在交出会话之后还有发布落地。不在关闭时删除文档：淘汰后重新
   绑定会立刻重新发布，删除会和更新的发布竞争。

## 前提：成员必须先是"可写"的（owner 权威接管）

发布的前提是那个成员有一个写者。成员能否可写，取决于绑定层认哪份历史：

- **owner 文档是权威**：它记录的 `history.stem` 是 peer 轮询的对象，也是每次 bind 用来判定
  "是不是陈旧导入"的依据。
- 当成员**自己目录里没有 transcript**、而 owner 指向的 v3 会话**可读且非空**时，
  `ownerSessionTakeover`（`internal/cli/team_member_owner.go`）直接 `OpenSession` **接管那个会话**
  ——这就是该成员的历史。
- 拒绝条件：owner 未发布身份 / 会话不存在 / 会话为空 / 读取失败 ⇒ 静默退回历史路径（首次条目），
  绝不在同一个身份下凭空造一个成员。
- owner 指向的会话**被别的进程持有** ⇒ 走既有的只读 follower 路径（写者争用，`ErrWriterOwned`）。

**为什么需要它**：历史行为下，owner 指向一个目录里没有对应 transcript 的会话时，bind 会走"首次条目"
分支、播下一个**新**会话，而 owner 的 stem 不会跟着变——于是每次 bind 都是"陈旧导入"，成员永久
只读、且**任何进程都不是它的写者**，用量通道自然永远没有发布者（这就是"进了 team session 反而
看不到上下文"的成因）。实测修复后：

```
team member usage publisher started team=GPU_MPC_Team member=leader-agent
~/.reasonix/team/GPU_MPC_Team/leader-agent/.usage.json
  {context_used: 11205, context_window: 128000, compact_ratio: 0.8, ...}
```

## 代码位置

- 文档与读写：`internal/team/ownerusage.go`（`OwnerUsage`、`WriteUsage`、`ReadUsage`、`Fresh`）
- 写者：`internal/cli/team_usage_publish.go`（publisher + provider/jobs 双向映射）
- 读者：`internal/cli/team_follower_usage.go` + `internal/cli/team_follower.go` 的五个遥测方法
- 接线：`internal/cli/team_backend_build.go`（写者）、`internal/cli/chat_tui_team_session.go`
  （ambient 写者）
- 绑定层接管：`internal/cli/team_member_owner.go`（`ownerSessionTakeover`）

## 合并提示（Team-agent ↔ main-v2）

本功能本身是 Team-agent 独有的新文件。它对**上游也存在的文件**只做了少量插入，均在改动处带
`// Team-agent:` 注释（`tui_diagnostics.go`、`chat_tui_watchdog.go`、`inbox_queue.go`），
合并 main-v2 时保留这些标记所在的块即可；标记的命名约定是 `// Team-agent: <为什么> <上游没有它时
会怎样>`，grep `Team-agent:` 可一次列全。
