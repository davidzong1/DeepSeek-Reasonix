# 原子读写工具对 vs bash：token 消耗实测对比

> 生成方式：`go test ./internal/tool/builtin/ -run TestAtomicVsBash -v`
> 测试文件：`internal/tool/builtin/atomicfs_bash_token_test.go`
> 主机：Linux/amd64，夹具为 `t.TempDir()` 下的临时文件。

---

## 0. 先读这一段：这份文档证明了什么，没证明什么

**证明了**：在固定夹具上，把同一个作业渲染成「bash 命令」和「原子工具调用」两种形状，
逐字节测量并对比。数字是可复现的，且带有防回归的变异探针。

**没证明**：

- **不是真实 token 计量。** 仓库没有本地 tokenizer（`internal/agent/estimateTokensFromBytes`
  用的是 4 字节/token 的启发式）。下表所有 `tok(est)` 都是 `⌈字节/4⌉` 的估算，
  **断言全部钉在字节上**，token 列仅供参考。
- **不是端到端会话对比。** 没有跑 `cmd/e2ebench` 的两臂（`atomic_fs=off` vs `team`）对照，
  所以「真实会话里 token 确实更少」这句话**没有证据**。
- **bash 没有被移除。** 团队面上 bash 仍在（实测 schema 1729B + 描述 658B）。
  换掉的只有 `read_file`/`write_file`/`edit_file`。因此本对比是
  「模型在两者都能选时会选哪个、各自多贵」，不是「原子替代了 shell」。

---

## 1. 测量方法

三条规则，写在测试文件头部：

| 规则 | 做法 |
| --- | --- |
| 载荷用真实尺寸 | heredoc 正文按真实会话实测值构造，不用五行存根 |
| 两个方向都算 | 读也计入：bash 改文件前通常要先看，`patch` 同样需要锚点 |
| token 是估算 | 字节为准，token 并排标注；断言只钉字节 |

### 夹具来源（真实会话库实测）

解析 `~/.reasonix/projects/*/sessions-v4/.content-v1/objects`，6639 个消息对象，
2381 条 bash 命令：

| 类别 | 中位 | p90 | 最大 |
| --- | --- | --- | --- |
| 全部 bash 命令文本 | 193B | 469B | 13312B |
| heredoc 命令（`<<'EOF'` 等） | **976B** | **2610B** | 13312B |
| 重定向写文件命令 | 550B | 2240B | 13312B |

下文 heredoc 夹具即取 **976B（中位）** 与 **2610B（p90）** 两档，
python 读-改-写取最大档 **11671B**。

---

## 2. 工例 1：2000 行文件里改 5 行

夹具：1996 行 / 129396 字节的 Go 文件，改中间一个 5 行函数。
**读和写都计入**（bash 用 `sed -n` 先看一眼，原子用 `atomic_read window` 取锚点）。

| 形状 | 参数 | 结果 | 调用合计 | tok(估) | 含前缀 |
| --- | ---: | ---: | ---: | ---: | ---: |
| bash heredoc（中位 976B 正文） | 1150 | 692 | **1842** | 461 | 4229 |
| bash heredoc（p90 2610B 正文） | 2824 | 692 | **3516** | 879 | 5903 |
| bash python 读-改-写 | 11848 | 812 | **12660** | 3165 | 15047 |
| **原子对（window + patch）** | **136** | **734** | **870** | **218** | 2541 |
| **原子 `symbol` patch（无事先 window）** | **99** | **124** | **223** | **56** | 1894 |

> 本节五行来自同一次 `go test ./internal/tool/builtin/ -run TestAtomicVsBashTokenComparison -v` 运行。
> 夹具正文与文件行数确定，但 bash 与 `sed` 行的命令里嵌了 `t.TempDir()` 的绝对路径，
> 那几行会随临时目录路径长度漂几个字节；原子行不含该路径，可用回执形状对账。

`symbol` 行是 `patch` 新增的符号定位：模型已经知道要改哪个符号时，锚点由宿主在
**同一次调用**里从自己读到的字节解析，因此省掉的正是原子对那一行为了可引用行号
而付的窗口（§3 记录窗口读本身就比 `sed -n` 贵 25%）。这一行 **小于** 原子对的 870B，
且回执里既没有被替换函数的旧正文、也没有新 `content` 正文（`TestAtomicVsBashTokenComparison` 断言）。

原子对相对三者：

| 对比 | 差值 |
| --- | --- |
| vs heredoc（中位） | **−52%** |
| vs heredoc（p90） | **−75%** |
| vs python 读-改-写 | **−93%** |

> 结论：**在这个作业上原子对明显更省**。省在两处 ——
> 读只要一个窗口而不是整文件，写只要一个 patch 而不是整文件重写。
> 注意 python 那一档之所以差这么多，是因为它的命令文本里**嵌了脚本**，
> 而脚本越长越贵；这正是 bash 作为文件 API 的根本成本。

---

## 3. 读形状单独对比（162KB 文件）

夹具：162013 字节的 Go 文件。

| 形状 | 结果 | tok(估) | 占整文件 |
| --- | ---: | ---: | ---: |
| `atomic_read auto` | 5824B | 1456 | 3.6% |
| `atomic_read outline` | 4234B | 1059 | 2.6% |
| `atomic_read window 100` | 2947B | 737 | 1.8% |
| `atomic_read tail` | 2408B | 602 | 1.5% |
| `bash cat big.go` | **162013B** | 40504 | **100%** |
| `bash sed -n window` | 2347B | 587 | 1.4% |

**两个必须分开说的结论：**

1. **整文件读**：原子 `auto` 5824B vs bash `cat` 162013B —— 差 96%。
   这一栏的收益是**拒绝把文件回显**，不是格式更紧凑。
2. **窗口读**：原子 2947B vs bash `sed -n` 2347B —— **原子贵 25%**。
   同样的行，bash 更便宜。多出来的字节是 `N→` 行号前缀，而正是这些行号
   让窗口**可被引用**（后续 patch 的 `old` 才能锚定）。

> **这条要如实记**：窗口读这一档，bash 在纯字节上赢。
> 原子的价值在可引用性与审计，不在字节。

---

## 4. 两个原子工具输掉的场景

| 场景 | bash | 原子 | 差值 |
| --- | ---: | ---: | ---: |
| 追加 20 行证据 | 990B | 1117B | **原子贵 127B** |
| 改 3 个文件（参数） | 291B | 246B | 原子省 45B |
| 改 3 个文件（回合 / 取租约） | 3 / 3 | **1 / 1** | **省 2 回合 + 2 次取租约** |

**追加**：载荷相同，原子多出的 127B 是 JSON 引号与 `mode` 字段的开销。
字节上**是输的**，收益在并发面（单次 `write(2)` 不交错、路径级租约）。

**多文件**：`ops` 参数反而更小（246B vs 291B），但真正省的是**回合**。
长会话里回合是最贵的单位 —— 不过这一句是推断，本表只证明了回合数差 2。

---

## 5. 前缀账单（每轮都付）

前缀 = 工具 schema + 描述，每个回合都在上下文里。

| 面 | 字节 | tok(估) |
| --- | ---: | ---: |
| `bash` 单独 | 2387 | 597 |
| 现状：三件套 + bash | 4232 | 1058 |
| 新：原子对 + bash | **4058** | **1015** |
| **每轮净差** | **−174** | **−44** |

> **注意这是 4→3 的替换，不是 4→2。** bash 两边都在，
> 减掉的是 `read_file`+`write_file`+`edit_file`（1845B）换成
> `atomic_read`+`atomic_write`（1671B）。
> 没有任何 bash schema 被移除。
>
> 这笔净省从 277B 收紧到 174B，是 `patch` 增 `symbol` 定位的代价：
> `atomic_write` 单件从 926B 涨到 1029B；而 §2 的 `symbol` 行只用 223B，
> 比同一次运行里 window+patch 的 870B 少 647B。

---

## 6. 防回归：变异探针

这个对比不是「打印一张表就完事」——它带断言，我验证过它会在回归时变红：

| 探针 | 结果 |
| --- | --- |
| 去掉 `auto` 阈值 + 去掉字节上限 | `auto` 返回 **214653B（整文件的 132%）**，`TestAtomicVsBashReadShapes` **红** |
| 恢复 | 绿 |

单去掉字节上限、或单去掉 `auto` 阈值，不足以触发（另一个机制兜住了）——
两个都去掉才红。这说明两条防线各自独立生效。

---

## 7. 复现方式

```bash
# 完整对比（含每行明细）
go test ./internal/tool/builtin/ -run TestAtomicVsBash -v -count=1

# 单独跑某一组
go test ./internal/tool/builtin/ -run TestAtomicVsBashTokenComparison -v
go test ./internal/tool/builtin/ -run TestAtomicVsBashReadShapes -v
go test ./internal/tool/builtin/ -run TestAtomicVsBashAppendAndMultiFile -v
go test ./internal/tool/builtin/ -run TestAtomicVsBashPrefixCost -v
go test ./internal/tool/builtin/ -run TestAtomicVsBashBashIsNotRemoved -v
```

测试覆盖：

| 测试 | 内容 |
| --- | --- |
| `TestAtomicVsBashTokenComparison` | 工例 1，三档 heredoc + 原子对 |
| `TestAtomicVsBashReadShapes` | 四种读模式 vs `cat` / `sed -n` |
| `TestAtomicVsBashAppendAndMultiFile` | 两个原子输掉的场景（如实记录） |
| `TestAtomicVsBashPrefixCost` | 每轮前缀账单 |
| `TestAtomicVsBashBashIsNotRemoved` | 钉住前提：bash 仍在面上 |

---

## 8. 结论汇总

| 说法 | 证据强度 |
| --- | --- |
| 前缀比旧三件省 174B/成员/轮（`symbol` 落地前 277B） | ✅ 精确实测 |
| 改文件作业比 bash 省 52%–93% | ✅ 夹具实测（真实尺寸载荷） |
| 整文件读比 `bash cat` 省 96% | ✅ 夹具实测 |
| **窗口读比 `bash sed -n` 贵 25%** | ✅ 实测 —— **原子输** |
| **追加比 bash heredoc 贵 127B** | ✅ 实测 —— **原子输** |
| 多文件省 2 回合 + 2 次取租约 | ✅ 回合数实测；token 收益是推断 |
| **真实会话 token 下降** | ❌ **无证据，未测量** |

要补上最后一行，需要跑 `cmd/e2ebench` 的 `atomic_fs=off` / `team` 两臂对照
（真实 provider 调用 + `prompt_tokens`/`completion_tokens` 读数）。
那属于发布前验证，本文件不替代它。
