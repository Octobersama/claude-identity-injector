# Claude Identity Injector

CLIProxyAPI 插件。对匹配的 Anthropic Messages 上游请求，在最终请求体的 `system[0]` 注入：

```text
You are Claude Code, Anthropic's official CLI for Claude.
```

规则可单独启用严格模式。严格模式在 CPA Cloak 前声明接管，并在最终上游阶段重建 Claude Code 风格的 system 三段结构、metadata、beta 列表和 Stainless/Claude Code 请求头。API Key/Authorization 认证头仍由 CPA 生成，插件不会读取、保存或记录密钥。

对于 OpenAI/OpenAI Responses 等非 Claude 原生客户端，严格模式会保留客户端工具的 description、schema、参数和额外字段，只把语义明确对应的工具名确定性映射为 `Bash`、`Edit`、`Glob`、`Grep`、`Read`、`Write`。`todowrite`、`ast_grep_*` 等其他客户端工具保持原名，不会伪装成无关的 Claude Code 核心工具。最终客户端响应会按同一个 `RequestID` 恢复已映射名称，并依据原工具 Schema 保守恢复唯一可判定的字段名、修复参数类型。Claude 原生客户端的工具定义保持不变。

行为与 cc-switch 的 Codex -> Anthropic 且启用 `impersonate_claude_code` 链路一致：只有首个 system 文本块已经精确等于上述文本时才跳过。如果相同文本位于后续块，仍会在首位再注入一份。

## 前置条件

- 使用包含 ABI schema v3、`request.intercept_upstream`、最终响应拦截器和请求生命周期回调的 CLIProxyAPI 构建。
- Windows 构建需要 Go、CGO 和 GCC。仓库脚本会优先使用 `PATH` 中的 `gcc`，也可通过 `-GccBin` 指定 MinGW `bin` 目录。
- 插件是进程内动态库，只安装可信构建产物。

## 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    claude-identity-injector:
      enabled: true
      priority: 100
      active: false
      cloak_handling: compatible
      log_matches: true
      rules:
        - id: sample
          enabled: false
          strict_mode: false
          strict_profile: full
          match_providers: true
          match_auths: false
          match_requested_models: false
          match_upstream_models: true
          providers: [claude]
          auth_ids: []
          auth_indexes: []
          requested_models: ["claude-*"]
          upstream_models: ["claude-*"]
```

规则按配置顺序匹配，首条命中规则生效。每个条件由对应的 `match_*` 开关独立控制：

- `providers`：不区分大小写的精确匹配。
- `auth_ids`、`auth_indexes`：区分大小写的精确匹配。
- `requested_models`、`upstream_models`：支持 `*` 和 `?` glob。

`active` 默认是 `false`，安装后不会立即修改请求。`enabled` 控制 CPA 是否加载插件，`active` 控制插件是否执行注入。

`strict_mode` 是规则级开关。命中严格规则时，插件跳过 CPA 内置 Claude Cloak，避免 Cloak 的 system 搬移、伪造 metadata 或敏感词混淆先行产生不可逆改动；未启用严格模式的规则继续使用下面的 `cloak_handling` 兼容策略。

严格模式可通过 `strict_profile` 选择请求改写档位。留空按 `full` 处理以兼容旧配置：

- `minimum`：AnyRouter 消融验证后的精简指纹。非认证请求头使用明确白名单，仅保留 Claude CLI `User-Agent`、`context-1m-2025-08-07` beta、标准 `anthropic-version` 和 `content-type`；认证方式与密钥仍完全由 CPA 管理，Bearer 与 `x-api-key` 都不由插件强制切换。正文把身份句放到 `system[0]`，写入只含 `device_id`/`session_id` 的 `metadata.user_id` JSON 字符串。对于 OpenAI 等非 Claude 客户端，原顶层 system 会无损包成 `<system-reminder>` 并前置到第一个 user turn，避免非 Claude agent 指纹留在 Anthropic 顶层 system；原 messages、thinking、context management、effort、工具 Schema 和 HTTP 协议保持不变。Claude 原生请求继续保留原顶层 system。工具只映射已知核心别名，并在不足三个 Claude Code 核心名称时补充不可执行的只读兼容标记。
- `minimal`：只补抓包中的 `anthropic-beta`，其余请求头、请求体和客户端工具保持 CPA/客户端原样。
- `bearer`：在 `minimal` 基础上只把 CPA 管理的 `x-api-key` 转换为 `Authorization: Bearer`。
- `bearer_http1`：在 `bearer` 基础上只强制使用 HTTP/1.1，不替换其他请求头。
- `minimal_core`：在 `minimal` 基础上仅在请求缺少工具时注入六个 Claude Code 核心工具；已有客户端工具完全保留。
- `identity`：在 `minimal` 基础上仅补入 Claude Code 身份句。
- `system`：在 `minimal` 基础上使用抓包中的三段 `system`。
- `body`：在 `system` 基础上补入 metadata、adaptive thinking、context management 和 effort。
- `body_core`：在 `body` 基础上仅在缺少工具时注入核心工具，不做客户端工具别名映射。
- `headers`：保留 CPA 最终 body，只覆盖完整 Claude Code 请求头、Bearer 和 HTTP/1.1。
- `headers_soft`：覆盖抓包中的完整非认证请求头，但保留 CPA 的认证方式和 HTTP 协议。
- `body_headers`：组合完整 body 与完整请求头，但仍保留客户端工具名和 Schema。
- `body_headers_core`：组合完整 body 与请求头，仅在缺少工具时注入核心工具；已有客户端工具不改名。
- `full`：旧版完整模拟，额外执行客户端工具映射或缺失工具注入。

所有档位仍保留严格请求的客户端 Schema 响应修复；该修复不依赖工具映射。

`cloak_handling` 支持三种策略：

- `compatible`：默认。保留 Cloak billing header 在 `system[0]`，识别 Cloak 已注入的 Claude Code 身份并避免重复；若只有 billing header，则把身份插入其后。
- `skip`：检测到 Cloak billing header 时跳过插件注入。
- `prepend`：始终把身份放到 `system[0]`，可能破坏 Cloak 的 billing header 顺序。

## 构建

```powershell
.\build.ps1
```

指定便携 MinGW：

```powershell
.\build.ps1 -GccBin "C:\path\to\mingw64\bin"
```

产物位于 `dist/claude-identity-injector.dll`。脚本会先运行 `go test ./...`。

## 安装

将 DLL 放入 CPA 配置目录下的：

```text
plugins/windows/amd64/claude-identity-injector.dll
```

启用全局插件和该插件配置后重启 CPA。设置页路径：

```text
/v0/management/plugins/claude-identity-injector/settings
```

页面需要 CPA 管理密钥。它可以读取运行时已启用的 AI 提供商、非敏感 Auth 文件摘要及全局当前可用模型，直接选择提供商、Auth 文件和上游模型，保存配置并触发热更新。不同条件之间使用 AND，同一条件内的多选值使用 OR。

## 日志与状态

插件通过 CPA `host.log` 写日志，包括：

- 配置应用或拒绝
- 规则未命中
- 注入成功或已经存在
- 严格模式的工具映射策略、客户端/上游工具数量和具体名称映射
- 严格请求最终客户端响应中的工具名还原、Schema 字段名恢复和参数类型修复
- 无法安全修复时的工具名、字段路径、实际类型、目标类型和原因
- 检测到 Cloak 后跳过
- 无效请求体的 fail-open 错误

运行计数可从以下接口读取：

```text
GET /v0/management/plugins/claude-identity-injector/status
```

返回 `seen`、`intercept_calls`、`matched`、`unmatched`、`injected`、`already_present`、`strict_takeover`、`strict_requests_active`、`tool_mapped`、`tool_names_restored`、`tool_arguments_fixed`、`tool_diagnostics`、`effective`、`cloak_skipped` 和 `errors`。`seen`、`matched`、`unmatched` 及最终 outcome 均按唯一生命周期 `RequestID` 计数；`intercept_calls` 单独记录 `pre_cloak`/`final` 等实际拦截阶段调用次数。重试请求只计一次，若后续最终尝试命中则从 `unmatched` 升级为 `matched`。`tool_mapped` 按实际发生工具名映射的请求计数；`strict_requests_active` 是尚未收到终止回调的严格请求数；`tool_diagnostics` 按工具聚合已检查调用、名称还原、字段修复、无法修复及最近问题。缺少生命周期 `RequestID` 的管理 API 探针仍可完成上游请求，但不会进入响应跟踪，也不计入 `errors`。`effective = injected + already_present`，表示最终具备身份提示词的命中请求。

对于插件已成功接管的严格请求，Anthropic 上游返回给 OpenAI Chat 或 OpenAI Responses 客户端的工具调用会在响应翻译完成后依据客户端原始工具 Schema 做保守修复。字段名仅在忽略大小写及 `_`/`-` 后唯一对应一个 Schema 字段且目标字段不存在时恢复，例如把模型返回的 `dry_run` 恢复为 OpenCode Schema 的 `dryRun`；候选歧义或目标冲突时保持原样并记入诊断。随后，只有 Schema 明确要求 `array`、`object`、`boolean`、`integer` 或 `number`，且字符串能完整、无歧义地解析为该类型时才转换；声明为 `string`、联合类型、非法 JSON 或目标类型不匹配的字段保持原样。插件通过 `RequestID` 隔离并在成功、失败、拒绝或取消后清理状态，普通请求不进入这条链路。日志和管理页均不记录参数值。

## 验证

1. 在设置页连接管理 API，新增并启用一条只匹配测试凭证和模型的规则。
2. 保持默认 `cloak_handling: compatible`，再启用插件的 `active`。
3. 通过 CPA 发出一条匹配请求，流式和非流式都支持。
4. 严格模式下用带工具的 OpenCode 请求确认日志出现 `tool_strategy=client_tools` 和 `tool_mapping`。
5. 让模型实际调用 `todowrite`、`ast_grep_search` 和 `ast_grep_replace`，确认数组参数正确、`dry_run` 已恢复为 `dryRun` 布尔参数且工具能成功执行。
6. 检查状态接口和设置页的 `matched`、`strict_takeover`、`tool_mapped`、`tool_names_restored`、`tool_arguments_fixed` 与按工具诊断。
7. 未启用 Cloak 时确认身份位于 `system[0]`；启用 Cloak 时确认 billing header 位于 `system[0]` 且身份只出现一次。

未命中规则、插件未激活、目标格式不是 Claude、请求体无效时，插件不会阻断上游请求。
