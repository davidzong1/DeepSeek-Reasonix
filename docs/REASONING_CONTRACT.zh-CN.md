# 适配器拥有的思考选项

每个协议适配器随工厂注册纯函数 `ReasoningForConfig`，返回当前连接和模型可用的
选项、顺序、显示名称及默认值。核心不定义统一的力度枚举。已创建客户端通过
`ReasoningProvider` 返回独立的能力副本，外部修改不会改变客户端行为。

配置、桌面菜单、CLI 补全、本地模型目录和请求校验使用同一套声明。查询能力前必须
先解析模型级覆盖。扩展供应商的选项由其 `Efforts` 声明提供，在 sidecar 请求前校验。

显式选择必须与声明的 ID 完全一致。不支持的值在网络请求前返回
`UNSUPPORTED_REASONING_EFFORT`，不再转换到相邻档位。非法能力声明也会报错。
只有开关能力的协议，不能通过填写 `supported_efforts` 虚构低、中、高能力。

## 词汇表在哪些边界生效

只要词汇表列出的是端点确实可以接收的档位，同一份解析出的词汇表就在所有可能携带力度的
边界上给出一致结论：在任一边界被拒绝的档位，在其他边界同样被拒绝，且没有任何边界会把
它改写成相邻档位。命名端点固定模式的词汇表不含这类档位：它是唯一一处存储判决与选择判决
不同的情形，也不是力度菜单。

| 边界 | 入口 | 未声明的档位 |
| --- | --- | --- |
| 显式选择 | `/effort`、桌面菜单、`--effort`、ACP 会话配置、子智能体 profile | 拒绝 |
| 已存配置 | provider 组装，随后适配器构造 | 拒绝 |
| 请求级 override | `provider.Request.EffortOverride` | 在 HTTP 之前拒绝 |

`supported_efforts` 是决定词汇表的 provider 条目字段。非空列表会**替换**内置词汇表：
端点若确有低/中/高档，就在这里声明；已识别端点的内置档位不会再叠加进来。
列表为空或缺失表示该端点完全没有力度控制，任何档位都会被拒绝。

声明值须为小写。TOML loader 会对列表做归一化与去重，之后档位按精确匹配比较；绕过该
归一化、手工拼装的原始列表不属于受支持的输入。

只有一条路径保留已存值而不拒绝，且只针对磁盘上已有的值，绝不适用于刚刚做出的选择：

- 未声明词汇表时，已保存的 DeepSeek `medium`/`xhigh` 沿用历史请求值 `high`
  （`migrateStoredDeepSeekEffort`）；在 `/effort` 重新输入同一别名仍会被拒绝。

对于两种未声明任何力度控制的设置，适配器还会跳过自身的构造期校验：
`thinking = "disabled"` 与 `reasoning_protocol = "none"`。这两处跳过都救不回已存档位：
provider 组装会先用该条目的词汇表校验它——`none` 为空、`disabled` 只有 `disabled`——
因此二者上的已存 `max` 会在那里被拒绝，而不是被映射。两处跳过只对绕过 provider 组装、
直接构造适配器的调用者有意义，也都不会放宽新选择可见的档位。

固定模式与可选档位也在这里分道：固定 `thinking = "disabled"` 的条目只报告 `disabled`
一个 ID，因为它正是校验已存值的依据——存储边界继续接受它，磁盘上已有的值判定保持不变；
选择边界则更窄——`/effort`、桌面菜单、`--effort`、ACP 会话配置与子智能体 profile 只接受
`auto` 与该条目声明的档位，绝不接受这个 ID（任何 `supported_efforts` 改动都不会让它可选）。
该 ID 只为已存值保留，不作为菜单提供。

`reasoning_protocol = "none"` 是另一种固定模式，其词汇表为空，因此两个边界都拒绝所有
档位，只有 `auto` 能选中任何东西。若端点的适配器确实会把 `disabled` 当作力度发送——
官方 DeepSeek、deepseek 协议、GLM、LongCat、MiniMax、anthropic 的开关，以及任何显式声明
的条目——该档位保持可选，固定 thinking 的条目会接受而不是拒绝 `/effort disabled`。

选中的模型无法表达该力度时，ACP 的会话级 override 会退化为 `auto`（`""`），
这是契约中“继承 provider 默认”的含义，而不是某个其他档位。

`auto` 保留原有“清除覆盖、使用默认”的界面与 CLI 含义，不等于自适应思考。
请求级 override 使用空字符串表示继承，而不是发送字面值 `auto`。保留旧配置加载时
对大小写及已退役 `off` 的兼容处理；已有合法 ID 和 TOML 字段名不变。未声明自定义
档位时，已保存的 DeepSeek `medium`、`xhigh` 沿用历史请求值 `high`，不重写配置。
新的显式选择和请求覆盖仍拒绝未声明的别名。其他非法值明确报错；非法默认值保留供校验，不替换为第一档。

| 边界 | 兼容行为 |
| --- | --- |
| Provider TOML | 字段与合法 ID 不变，不自动重写文件 |
| 桌面 `EffortInfo.options` | 新增可选元数据，同时保留旧 `levels` |
| 新前端连接旧后端 | 回退读取 `levels` |
| 远程模型目录 | 继续使用原有 `Efforts` 声明 |
| 模型历史 | 不改提示词、工具定义或历史思考内容 |

默认请求保留原有序列化。主动修改力度仍可能影响服务端缓存；契约本身不增加提示词
内容。实验性 governor 在自动使用 low 前检查适配器声明。本次不引入自动跨模型
力度迁移，也不移植 Harness 的请求日志架构。

参考 [DeepSeek Harness 设计](https://github.com/deepseek-ai/deepseek-harness/blob/d347e703908d0406b7a7ef80e3a0e594d86b2215/.agents/notes/implemented/architecture/2026-07-24-adapter-owned-reasoning-effort-capabilities.zh.md)
独立实现，未复制上游代码。
