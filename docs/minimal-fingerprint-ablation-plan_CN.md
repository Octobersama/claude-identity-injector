# AnyRouter 最小 Claude Code 指纹消融计划

## 目标

在固定 AnyRouter 凭证、上游模型、客户端和测试提示词的前提下，找出能够通过
客户端指纹检测的最小请求改写集合。优先减少对 system、工具 Schema、thinking 和
metadata 的改写，避免身份模拟对模型行为和工具调用质量造成不必要影响。

本实验只针对规则已启用的严格模式请求。认证头仍由 CPA 管理，插件不读取或记录
密钥。

## 已确认的链路事实

- CPA 已默认生成 `Anthropic-Version`、`X-App`、Stainless、Claude Code session、
  `Accept`、`Accept-Encoding` 和 `Connection` 等头。
- 自定义 Claude API 网关默认使用 Bearer 认证，因此 AnyRouter 凭证不需要插件把
  `x-api-key` 转换为 Bearer。
- 当前 AnyRouter 凭证的 CPA Cloak 为 `never`，实验仍显式绕过 Cloak，避免其他
  配置变化干扰结果。
- 当前 AnyRouter 凭证没有启用 experimental CCH signing。保留或跳过 CPA 最终
  body transforms 在本次环境中预计没有差异，但档位仍明确记录该行为。
- 严格模式工具响应修复只依据客户端原始 Schema 恢复唯一字段名和明确类型，可与
  Claude Code 指纹模拟解耦。

## 递进档位

所有档位均绕过 CPA Cloak，并保留严格响应跟踪和 Schema 参数修复。

| 档位 | 请求头 | system / metadata / 行为参数 | 工具 |
| --- | --- | --- | --- |
| `minimal` | 仅覆盖抓包 beta，其余使用 CPA 默认值 | 完全保留 CPA 最终 body | 完全保留客户端工具 |
| `bearer` | `minimal` + CPA 管理的 Bearer 认证 | 完全保留 CPA 最终 body | 完全保留客户端工具 |
| `bearer_http1` | `bearer` + HTTP/1.1 | 完全保留 CPA 最终 body | 完全保留客户端工具 |
| `minimal_core` | 仅 beta，缺少工具时注入核心工具 | 其他 body 保留 | 已有工具保留，缺失时注入核心工具 |
| `identity` | 同 `minimal` | 仅在 system 首位加入身份句 | 完全保留客户端工具 |
| `system` | 同 `minimal` | 覆盖为官方三段 system | 完全保留客户端工具 |
| `body` | 同 `minimal` | 官方 system、metadata、thinking、context management、effort | 完全保留客户端工具 |
| `body_core` | 同 `minimal` | 同 `body` | 仅缺失时注入核心工具 |
| `headers` | 完整抓包请求头并强制 HTTP/1.1 | 完全保留 CPA 最终 body | 完全保留客户端工具 |
| `headers_soft` | 完整抓包非认证请求头，保留 CPA 认证和 HTTP 协议 | 完全保留 CPA 最终 body | 完全保留客户端工具 |
| `body_headers` | 完整抓包请求头并强制 HTTP/1.1 | 同 `body` | 完全保留客户端工具 |
| `body_headers_core` | 同 `body_headers` | 同 `body` | 仅缺失时注入核心工具 |
| `full` | 完整抓包请求头并强制 HTTP/1.1 | 同 `body` | 现有 Claude Code 工具映射/缺失工具注入 |

未配置 `strict_profile` 的旧规则按 `full` 处理，保证升级后行为不变。

## 实测结果（2026-08-01，AnyRouter `claude-opus-5`）

测试固定使用同一 AnyRouter 凭证、同一模型和短请求，并分别通过管理面板
`api-call` 与真实 `/v1/messages` 入口验证。面板直连不会经过 Claude executor 的
默认头和认证流程，因此只能作为辅助对照；真实结论以 `/v1/messages` 为准。

| 场景 | 档位 | 结果 | 说明 |
| --- | --- | --- | --- |
| 面板无工具探针 | `minimal` / `identity` / `system` / `headers` | 503 | 只有 beta 或 system/头单侧改写不足以通过 |
| 面板无工具探针 | `body` / `body_headers` | 520 | 完整 body 单独或与头组合仍失败 |
| 面板无工具探针 | `full` | 200 | 缺工具时注入六个核心工具后通过 |
| 真实 Claude 入口、无工具 | `minimal` / `bearer` / `bearer_http1` | 401 | 仅 beta、认证转换或 HTTP/1.1 不足 |
| 真实 Claude 入口、无工具 | `body_core` | 401 | 完整 body + 缺失时核心工具仍缺少完整固定头 |
| 真实 Claude 入口、无工具 | `body_headers_core` | 200，连续 3 次 | 当前无工具请求的最低稳定组合 |
| 真实 Claude 入口、无工具 | `full` | 200 | 与 `body_headers_core` 等价通过，但保留旧映射逻辑 |
| 真实 Claude 入口、已有单个自定义工具 | `body_headers_core` | 520 | 仅保留任意客户端工具不能通过检测 |
| OpenCode 风格六个小写工具 | `body_headers_core` | 429 | 保留小写工具未通过；日志显示完整头/body 已到达上游 |
| OpenCode 风格六个小写工具 | `full` | 200 | 已知别名映射为 Claude Code 核心名称后通过 |

### 精细消融补充

在固定凭证、模型、短文本提示和认证值的独立 HTTP/1.1/HTTP/2 探针中继续逐字段删除，得到：

- 请求头硬信号是 Claude CLI `User-Agent`；删除它或替换为 OpenCode User-Agent 都返回 520。`x-app`、Stainless 全套、Claude session header 和 dangerous-browser-access 均可删除。HTTP/1.1 与 HTTP/2、流式与非流式都可通过。
- `context-1m-2025-08-07` beta 是必要协议信号，删除后返回明确的 400；`anthropic-version` 在独立探针中可删除仍返回 200，但 `minimum` 仍保留它作为标准 Anthropic Messages API 兼容头。
- Bearer 与 `x-api-key` 在同一组前后基线稳定为 200，因此 `minimum` 不再强制转换认证方式；认证头继续完全由 CPA 管理。
- 精简 body 需要身份句精确位于 `system[0]`，以及字符串形式的 `metadata.user_id`；该字符串中的 JSON 至少包含 `device_id` 与 `session_id`，`account_uuid` 可删除。billing、完整 harness、cache control、thinking、context management 和 output effort 均可删除。身份后的客户端 system 块可保留。
- 精简 body 只需要 `context-1m-2025-08-07` beta；删除它会返回明确的 1m 未启用错误。其余抓包 beta 在该精简 body 下均非通过所必需。
- 工具 Schema 和 description 不参与严格比对，但至少需要三个 Claude Code 核心工具名称。两个核心名称、三个任意自定义名称或两个核心加一个自定义都会 520；任意三种核心名称可通过，额外自定义工具也可通过。

据此新增 `minimum` 档位，保留 `full` 作为旧行为和回退方案。

### 2026-08-01 成对复测

为排除 AnyRouter 偶发 520，本轮每组都使用“基线在前、基线在后”的短序列，且直接请求上游、绕过插件：

| 变量 | 结果 |
| --- | --- |
| 最小基线（身份、metadata、3 个核心工具、Claude CLI UA、1m beta） | 前后均 200 |
| 删除身份句 | 503 |
| 删除 `metadata.user_id` | 503 |
| 删除工具 | 520 |
| 删除 Claude CLI User-Agent | 520 |
| 改为 OpenCode User-Agent | 520 |
| `stream=true` | 200 |
| 删除 1m beta | 400 |
| 删除 `anthropic-version` | 200 |
| Bearer 认证 | 200 |
| `x-api-key` 认证 | 200 |

OpenCode 环境说明文本的逐字段探针出现相同内容在不同轮次分别 200/520 的情况，未形成稳定判据，因此插件不改写客户端环境说明或 AGENTS 内容。

生产部署后的真实 OpenCode title 请求确认最终非认证头已缩减为
`anthropic-beta`、`anthropic-version`、`content-type` 和 Claude CLI
`user-agent`，认证由 CPA 保留为 Bearer，旧的伪装 401 已消失。该请求随后收到
`429 Service Unavailable`；同形正文的直连探针对 `max_tokens=1` 前后均为 200、
`max_tokens=32000` 为 520，但继续复测时 64000/32000/64000 全部 520，表明上游状态
会随时间变化。现阶段不据此改写客户端 `max_tokens`，以免用不稳定证据改变模型输出
预算；待 AnyRouter 恢复稳定后再做同组前后基线复测。

因此推荐采用按请求形态的策略：

1. 没有客户端工具的普通 Claude 请求可以使用 `body_headers_core`，它不做客户端
   工具别名映射，只在完全缺少工具时注入核心工具。
2. 带 OpenCode 工具的请求继续使用 `full`，因为当前 AnyRouter 会检查工具集合的
   Claude Code 形态；插件只映射已识别的 Claude Code 工具别名，不会把无关工具强行
   改名。响应阶段仍按原始 Schema 恢复客户端名称和参数字段。
3. 对任意自定义工具，尚无证据表明可以在不映射或不补充 Claude Code 工具的情况
   下通过 AnyRouter；不应把 520/429 直接归因于模型能力，需结合上游状态和日志判断。

这组实验没有修改 CPA 本体，也没有记录认证值或请求正文；日志仅记录档位、头键、
beta 摘要、body 字节数、工具策略、HTTP 协议和状态码。

## 测试顺序

1. 从 `minimal` 开始，用无工具的固定短请求验证状态码和普通响应。
2. 分别测试 `identity`、`system`、`body` 和 `headers`，确定 body 与 header 哪一侧
   是必要条件；需要组合时再测试 `body_headers`，最后才测试 `full`。
3. 对最低成功档连续运行三次普通请求，排除偶发上游故障。
4. 使用同一档运行固定工具任务，检查工具名称、`paths`、`globs`、`dryRun` 和
   `todos`。
5. 与 `full` 对照工具选择、参数修复次数、总耗时和失败信息。
6. 若某档成功而上一档失败，仅对两档的新增组件继续细分，避免无意义地扩大配置面。

## 通过标准

- AnyRouter 不返回客户端身份、1m beta、520、502 或空 body 相关错误。
- 同一档三次普通请求至少三次成功；429/明确的模型停用不计为指纹失败。
- 工具任务使用客户端提供的工具，不注入无关工具。
- 工具参数满足客户端 Schema；插件无法安全修复计数为 0。
- 请求日志确认 `anthropic-beta` 包含 `context-1m-2025-08-07`。
- 最终推荐选择通过检测的最低档，而不是默认保留更高档改写。

## 观测与恢复

- CPA 主日志记录档位、控制位、header key、beta 摘要和 body 组件布尔值，不记录
  请求正文或凭证。
- 每次只热更新插件配置，不替换 CPA EXE，也不反复重启 CPA。
- 实验结束后恢复用户原规则；若最低档尚未达到连续通过标准，继续使用 `full`。
