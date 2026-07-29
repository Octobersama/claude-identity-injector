# Claude System Identity Injector

CLIProxyAPI 插件。对匹配的 Anthropic Messages 上游请求，在最终请求体的 `system[0]` 注入：

```text
You are Claude Code, Anthropic's official CLI for Claude.
```

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
    claude-system-identity-injector:
      enabled: true
      priority: 100
      active: false
      cloak_handling: compatible
      log_matches: true
      rules:
        - id: sample
          enabled: false
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

产物位于 `dist/claude-system-identity-injector.dll`。脚本会先运行 `go test ./...`。

## 安装

将 DLL 放入 CPA 配置目录下的：

```text
plugins/windows/amd64/claude-system-identity-injector.dll
```

启用全局插件和该插件配置后重启 CPA。设置页路径：

```text
/v0/management/plugins/claude-system-identity-injector/settings
```

页面需要 CPA 管理密钥。它可以读取非敏感凭证摘要及各 Auth 文件的已注册模型，直接选择提供商、Auth 文件和模型，保存配置并触发热更新。

## 日志与状态

插件通过 CPA `host.log` 写日志，包括：

- 配置应用或拒绝
- 规则未命中
- 注入成功或已经存在
- 检测到 Cloak 后跳过
- 无效请求体的 fail-open 错误

运行计数可从以下接口读取：

```text
GET /v0/management/plugins/claude-system-identity-injector/status
```

返回 `seen`、`matched`、`injected`、`already_present`、`cloak_skipped` 和 `errors`。

## 验证

1. 在设置页连接管理 API，新增并启用一条只匹配测试凭证和模型的规则。
2. 保持默认 `cloak_handling: compatible`，再启用插件的 `active`。
3. 通过 CPA 发出一条匹配请求，流式和非流式都支持。
4. 检查 CPA 日志出现 `Claude identity system prompt injected`。
5. 检查状态接口的 `matched` 和 `injected` 增加。
6. 未启用 Cloak 时确认身份位于 `system[0]`；启用 Cloak 时确认 billing header 位于 `system[0]` 且身份只出现一次。

未命中规则、插件未激活、目标格式不是 Claude、请求体无效时，插件不会阻断上游请求。
