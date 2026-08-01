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
| `identity` | 同 `minimal` | 仅在 system 首位加入身份句 | 完全保留客户端工具 |
| `system` | 同 `minimal` | 覆盖为官方三段 system | 完全保留客户端工具 |
| `body` | 同 `minimal` | 官方 system、metadata、thinking、context management、effort | 完全保留客户端工具 |
| `headers` | 完整抓包请求头并强制 HTTP/1.1 | 完全保留 CPA 最终 body | 完全保留客户端工具 |
| `body_headers` | 完整抓包请求头并强制 HTTP/1.1 | 同 `body` | 完全保留客户端工具 |
| `full` | 完整抓包请求头并强制 HTTP/1.1 | 同 `body` | 现有 Claude Code 工具映射/缺失工具注入 |

未配置 `strict_profile` 的旧规则按 `full` 处理，保证升级后行为不变。

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
