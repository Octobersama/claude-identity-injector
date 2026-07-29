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
      skip_when_cloaked: true
      log_matches: true
      rules:
        - id: sample
          enabled: false
          providers: [claude]
          auth_ids: []
          auth_indexes: []
          requested_models: ["claude-*"]
          upstream_models: ["claude-*"]
```

规则按配置顺序匹配，首条命中规则生效。空列表表示不限制该维度：

- `providers`：不区分大小写的精确匹配。
- `auth_ids`、`auth_indexes`：区分大小写的精确匹配。
- `requested_models`、`upstream_models`：支持 `*` 和 `?` glob。

`active` 默认是 `false`，安装后不会立即修改请求。`enabled` 控制 CPA 是否加载插件，`active` 控制插件是否执行注入。

CPA Cloak 会先插入 billing header 和完整 Claude Code system prompt。要保证身份文本严格位于 `system[0]`，匹配凭证的 Cloak 必须设为 `never`。默认 `skip_when_cloaked: true` 会在检测到 billing header 时跳过注入并记录日志。

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

页面需要 CPA 管理密钥。它可以读取非敏感凭证摘要、编辑规则、保存配置并触发热更新。

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
2. 将对应 Claude 凭证的 Cloak 设为 `never`，再启用插件的 `active`。
3. 通过 CPA 发出一条匹配请求，流式和非流式都支持。
4. 检查 CPA 日志出现 `Claude identity system prompt injected`。
5. 检查状态接口的 `matched` 和 `injected` 增加。
6. 使用本地上游捕获或 CPA 请求日志确认最终 Anthropic JSON 的 `system[0].text` 精确等于目标文本。

未命中规则、插件未激活、目标格式不是 Claude、请求体无效时，插件不会阻断上游请求。
