# 严格模式工具兼容修复计划

> 实施状态：已按本计划完成插件侧实现；未修改 CPA 或管理面板仓库。

## 目标

修复 Claude 严格模式下模型偶发把工具参数数组、对象、布尔值或数字编码成
JSON 字符串的问题，并补齐 OpenAI Responses 输出格式。修复只作用于本插件已
成功接管的严格模式请求，不改变普通注入规则、CPA Cloak 或其他 Claude 请求。

已知故障包括：

- `todowrite.todos`：数组被返回为字符串；
- `ast_grep_search.paths`：数组被返回为字符串；
- `ast_grep_replace.paths` / `globs`：数组被返回为字符串；
- `ast_grep_replace.dry_run`：布尔值被返回为字符串。

## CPA 代码结论

CPA 当前实现具备以下相关能力：

- OpenAI Chat / OpenAI Responses 到 Claude 的请求转换会搬运工具名称和 JSON
  Schema；
- Claude 到 OpenAI Chat 的流式转换会先累积 `input_json_delta`，在
  `content_block_stop` 输出完整 `function.arguments`；
- Claude 到 OpenAI Responses 会输出完整的
  `response.function_call_arguments.done`、`response.output_item.done` 和
  `response.completed` 事件；
- Claude OAuth 执行链存在工具名正反向映射，但只服务 OAuth 指纹处理，不会按
  客户端 Schema 修复响应参数类型；
- 最终 `ResponseInterceptor`、`StreamChunkInterceptor` 和
  `RequestLifecyclePlugin` 都携带同一个 `RequestID`。

CPA 没有实现基于客户端工具 Schema 的响应参数类型校正。现有插件 API 已足以
完成严格请求关联、最终响应修复和生命周期清理，因此本次不修改 CPA 或管理面板
仓库。

## 实现边界

1. 严格接管成功后，以 `RequestID` 记录该请求的工具名称映射。
2. 普通规则、未命中规则、严格接管失败的请求不进入响应修复。
3. 将工具名恢复和参数修复移到最终响应拦截阶段，避免缺少凭证上下文的
   `response_after_translator` 全局修改其他请求。
4. 请求完成、失败、拒绝或取消后通过生命周期回调删除请求状态。
5. 不记录参数值、请求正文、凭证或工具输出。
6. 不把无关 OpenCode 工具伪装成 Claude Code 核心工具；保留现有六个语义明确的
   名称映射。

## 响应格式

### OpenAI Chat Completions

- 非流式：`choices[].message.tool_calls[].function`；
- 流式：`choices[].delta.tool_calls[].function`；
- 先恢复工具名，再使用原始客户端请求中的 `function.parameters` 校正
  `function.arguments`。

### OpenAI Responses

- 非流式：`output[]` 以及 `response.output[]` 中的 `function_call`；
- 流式：记录 `response.output_item.added` 的 `item_id -> tool name`，修复
  `response.function_call_arguments.done`，并同时修复
  `response.output_item.done` 与 `response.completed` 中的完整调用；
- 忽略 `response.function_call_arguments.delta`，不修改不完整 JSON 分片。

## 类型校正规则

仅当客户端 Schema 明确要求以下单一类型，且字符串可完整、无歧义地解析为该
类型时转换：

- `array`
- `object`
- `boolean`
- `integer`
- `number`

声明为 `string`、联合类型、无 Schema、非法 JSON、多段 JSON 或目标类型不匹配时
保持原值。递归处理对象属性和数组元素。

## 诊断与管理页

增加按工具聚合的运行期诊断：

- 已检查调用数；
- 工具名恢复数；
- 参数字段修复数；
- 无法安全修复的问题数；
- 最近问题的字段路径、实际类型、目标类型和原因。

日志只包含工具名、协议、字段路径、类型变化和原因。设置页展示同样的聚合数据，
不展示参数值。

## 测试矩阵

- 非严格请求完全不变；
- 严格请求状态按 `RequestID` 隔离并在生命周期结束时清理；
- OpenAI Chat 非流式和 SSE：修复 `todos`、`paths`、`globs`、`dry_run`；
- OpenAI Responses 非流式和四类完整流式事件；
- 工具名请求侧映射与最终响应恢复；
- 联合类型、字符串字段、非法 JSON 和多段 JSON保持不变；
- 并发请求运行 `go test -race ./...`；
- 管理状态和资源页不泄漏参数值。

## 部署

1. `gofmt` 并运行 `go test ./...`、`go test -race ./...`；
2. 使用插件仓库现有 Windows CGO 构建脚本生成 DLL；
3. 校验生产 8317 PID 和 EXE 路径，备份 EXE、DLL、配置和管理页；
4. 将新 DLL 放到 `.next.dll`，使用生产重启助手替换并尽快恢复单实例；
5. 验证 `/healthz`、插件响应头、插件注册状态和 DLL SHA-256；
6. 在 OpenCode 中先做严格规则定向测试，再做不指定工具的自然行为测试。
