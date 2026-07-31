# Claude Identity Injector

CLIProxyAPI 插件。对匹配的 Anthropic Messages 上游请求，在最终请求体的 `system[0]` 注入：

```text
You are Claude Code, Anthropic's official CLI for Claude.
```

规则可单独启用严格模式。严格模式在 CPA Cloak 前声明接管，并在最终上游阶段重建 Claude Code 风格的 system 三段结构、metadata、beta 列表和 Stainless/Claude Code 请求头。API Key/Authorization 认证头仍由 CPA 生成，插件不会读取、保存或记录密钥。

对于 OpenAI/OpenAI Responses 等非 Claude 原生客户端，严格模式会保留客户端工具的 description、schema、参数和额外字段，只在上游传输时把工具名确定性映射为 `Bash`、`Edit`、`Glob`、`Grep`、`Read`、`Write`。客户端工具不足六个时，插件复用真实工具定义生成可还原别名，不注入客户端无法执行的新工具；Anthropic 响应进入 CPA 翻译器前再把 `tool_use.name` 还原成客户端原名。Claude 原生客户端的工具定义保持不变。

行为与 cc-switch 的 Codex -> Anthropic 且启用 `impersonate_claude_code` 链路一致：只有首个 system 文本块已经精确等于上述文本时才跳过。如果相同文本位于后续块，仍会在首位再注入一份。

## 前置条件

- 使用包含 ABI schema v3 和 `request.intercept_upstream` 的 CLIProxyAPI 构建。
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
- 响应翻译前还原的客户端工具名和数量
- 检测到 Cloak 后跳过
- 无效请求体的 fail-open 错误

运行计数可从以下接口读取：

```text
GET /v0/management/plugins/claude-identity-injector/status
```

返回 `seen`、`matched`、`unmatched`、`injected`、`already_present`、`strict_takeover`、`tool_mapped`、`tool_names_restored`、`tool_arguments_fixed`、`effective`、`cloak_skipped` 和 `errors`。`tool_mapped` 按实际发生工具名映射的请求计数，`tool_names_restored` 按响应中的 `tool_use` 块计数，`tool_arguments_fixed` 按依据客户端工具 schema 修复的参数字段计数；`effective = injected + already_present`，表示最终具备身份提示词的命中请求。

对于 Anthropic 上游返回给 OpenAI 兼容客户端的工具调用，插件会在响应翻译完成后依据客户端原始工具 schema 做保守类型修复。只有 schema 明确要求 `array`、`object`、`boolean`、`integer` 或 `number`，且字符串能完整、无歧义地解析为该类型时才转换；声明为 `string` 或联合类型的字段保持原样。日志只记录工具名、字段路径和类型变化，不记录参数值。

## 验证

1. 在设置页连接管理 API，新增并启用一条只匹配测试凭证和模型的规则。
2. 保持默认 `cloak_handling: compatible`，再启用插件的 `active`。
3. 通过 CPA 发出一条匹配请求，流式和非流式都支持。
4. 严格模式下用带工具的 OpenCode 请求确认日志出现 `tool_strategy=client_tools` 和 `tool_mapping`。
5. 让模型实际调用一个工具，确认 OpenCode 收到的是自己的原工具名并能成功执行。
6. 检查状态接口的 `matched`、`strict_takeover`、`tool_mapped` 和 `tool_names_restored` 增加。
7. 未启用 Cloak 时确认身份位于 `system[0]`；启用 Cloak 时确认 billing header 位于 `system[0]` 且身份只出现一次。

未命中规则、插件未激活、目标格式不是 Claude、请求体无效时，插件不会阻断上游请求。
