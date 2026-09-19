# 本地客户端接入

> 文档定位：开发、发布和真实客户端回归的使用说明；接口与数据契约以 `plans/oneai-proxy-solution-plan.md` 为准。
> 最近同步：2026-09-02

本说明只规定本地启动、客户端接入和回归时的操作与记录方式，不单独定义日志产品契约。请求日志、操作日志、运行日志、正文加密、API 凭证 `***` 脱敏、Cookie 页面展示、SSE 和保留期均以权威计划第 11 章和阶段 6R 为准。CLI 回归输出和人工记录仍不得把 API Key、请求正文或响应正文复制到终端、报告或仓库。

OneAI Proxy 默认提供两个本地监听器：

- 代理：`http://127.0.0.1:9988`
- 管理页：`http://127.0.0.1:9989`

渠道编辑中的 Base URL 填上游 API 根地址，可包含上游要求的 `/v1` 路径；代理会规范化并避免重复拼接。客户端访问本地代理时才使用本地监听器的 `/v1` 地址。

首次启动可直接使用默认令牌：管理令牌为 `oneai-local-admin`，代理令牌为 `oneai-local-proxy`。打开管理页后可在“访问令牌”中保存自己的管理令牌和代理令牌；保存后对应默认令牌立即失效。令牌哈希保存在本地数据库，明文放在与渠道凭证相同的秘密存储中。

发布版本或执行前端构建后，直接访问 `http://127.0.0.1:9989` 使用管理页。开发调试时运行 `npm run dev`，访问 `http://127.0.0.1:5188`；开发服务器会将 `/api` 和 `/healthz` 请求转发到管理监听器。不要使用其他项目占用的 `5173` 端口。

在管理页修改代理或管理监听器的 IP/端口并保存后，需要重启应用。重启后，管理页应访问新的管理地址，Codex、Claude Code 等客户端也要把代理地址改为新的代理地址；旧端口不会继续提供服务。发布版页面使用相对 API 路径，端口修改后无需重新构建前端。开发页的 Vite 代理默认仍指向 `127.0.0.1:9989`，管理端口变更后应改用构建后的管理页，或同步修改 `vite.config.ts` 的代理目标。

## 本地后端启停

在仓库根目录以前台方式启动后端：

```sh
go run ./cmd/oneai-proxy serve
```

后端会使用平台默认数据目录，并读取其中已保存的监听地址；默认启动后访问 `http://127.0.0.1:9989/healthz` 验证管理服务，代理入口为 `http://127.0.0.1:9988/v1/*`。前端资源有改动时，应先运行一次 `npm run build`，再启动后端；只有 Go 代码改动时无需重复构建前端。

- 关闭：在运行后端的终端按 `Ctrl+C`，等待程序完成优雅退出并释放两个监听端口。
- 重启：先按上述方式关闭，再重新执行 `go run ./cmd/oneai-proxy serve`。设置页修改监听 IP 或端口后，必须使用这套流程重启，新地址才会生效。
- 后端不在当前终端时，先运行 `lsof -nP -iTCP:9988 -sTCP:LISTEN` 和 `lsof -nP -iTCP:9989 -sTCP:LISTEN` 确认进程，再对确认出的后端 PID 执行 `kill -TERM <PID>`；不要按名称批量终止其他 Go 或 Node 进程。

前端开发服务器是独立进程：`npm run dev` 启动 `http://127.0.0.1:5188`，后端重启不需要同时关闭 Vite。

## Codex Responses

Codex 的自定义 Provider 使用 Responses 协议。将下面的 Provider 配置合并到 Codex 配置文件，并将 `<代理令牌>` 替换为本地代理令牌：

```toml
[model_providers.oneai]
name = "OneAI Proxy"
base_url = "http://127.0.0.1:9988/v1"
wire_api = "responses"
requires_openai_auth = true

[profiles.oneai]
model_provider = "oneai"
```

在 Codex 使用该 Provider 时，通过其支持的 OpenAI 认证配置提供代理令牌。令牌只发送到 `127.0.0.1:9988`，OneAI Proxy 不会把本地代理令牌转发给上游。

### Codex 临时非交互验证

需要验证本地代理而不改写现有 Codex 配置时，可使用临时 `CODEX_HOME`：

```sh
set -eu
temp_codex_home=$(mktemp -d)
trap 'rm -r "$temp_codex_home"' EXIT
cat > "$temp_codex_home/config.toml" <<'EOF'
model = "<已配置的模型>"
model_provider = "oneai"

[model_providers.oneai]
name = "OneAI Proxy"
base_url = "http://127.0.0.1:9988/v1"
wire_api = "responses"
requires_openai_auth = true
env_key = "OPENAI_API_KEY"
EOF
CODEX_HOME="$temp_codex_home" OPENAI_API_KEY="<代理令牌>" \
  codex exec --ephemeral --skip-git-repo-check --color never \
  --model "<已配置的模型>" "Reply with exactly OK and nothing else."
```

`<已配置的模型>` 必须已经配置在该渠道的模型目录或 `fallbackModel` 中；当前阶段 CLI 回归不依赖模型页全局映射。命令结束后临时配置目录会删除。

## Claude Code Messages

Claude Code 使用 Anthropic Messages 协议。在启动 Claude Code 的同一终端设置本地地址和代理令牌：

```sh
export ANTHROPIC_BASE_URL=http://127.0.0.1:9988
export ANTHROPIC_AUTH_TOKEN='<代理令牌>'
claude
```

Windows PowerShell：

```powershell
$env:ANTHROPIC_BASE_URL = "http://127.0.0.1:9988"
$env:ANTHROPIC_AUTH_TOKEN = "<代理令牌>"
claude
```

### Claude Code 临时非交互验证

Claude Code 可通过临时 `CLAUDE_CONFIG_DIR` 和 `-p`（`--print`）执行一次性验证，不读取或修改现有项目配置：

```sh
set -eu
temp_claude_config=$(mktemp -d)
trap 'rm -r "$temp_claude_config"' EXIT
CLAUDE_CONFIG_DIR="$temp_claude_config" \
  ANTHROPIC_BASE_URL=http://127.0.0.1:9988 \
  ANTHROPIC_AUTH_TOKEN="<代理令牌>" ANTHROPIC_API_KEY='' \
  claude -p --no-session-persistence --settings '{}' \
  --setting-sources user --tools '' --output-format text \
  --model "<已配置的模型>" "Reply with exactly OK and nothing else."
```

`<已配置的模型>` 必须已经配置在该渠道的模型目录或 `fallbackModel` 中；当前阶段 CLI 回归不依赖模型页全局映射。命令结束后临时配置目录会删除。

## 本机真实环境回归流程

下面是阶段 9 使用的可复跑流程。它把代理、SQLite 数据目录、Codex 配置和 Claude Code 配置全部放在临时目录与临时端口中，不会改写默认实例（`9988/9989`）或用户现有配置。流程需要在仓库根目录执行，并且只在明确授权后读取 `api.json` 中的真实凭证；第 1 至第 5 节应在同一个 Shell 会话中按顺序执行。运行主机还必须具备可用的系统密钥环和 `jq`、`curl`。

### 1. 启动隔离代理

```sh
set -eu
repo_dir=$(pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/oneai-proxy-cli.XXXXXX")
data_dir="$test_root/data"
codex_home="$test_root/codex"
claude_config="$test_root/claude"
proxy_bin="$test_root/oneai-proxy"
mkdir -p "$data_dir" "$codex_home" "$claude_config"

go build -o "$proxy_bin" ./cmd/oneai-proxy
"$proxy_bin" serve \
  --proxy-listen 127.0.0.1:39988 \
  --admin-listen 127.0.0.1:39989 \
  --data-dir "$data_dir" &
proxy_pid=$!
cleanup() {
  if [ -n "${admin_auth_header:-}" ]; then
    for channel_id in real-openai real-anthropic; do
      curl --fail --silent --max-time 2 \
        -H "$admin_auth_header" -X DELETE \
        "http://127.0.0.1:39989/api/admin/v1/channels/$channel_id" >/dev/null 2>&1 || true
    done
  fi
  kill "$proxy_pid" 2>/dev/null || true
  wait "$proxy_pid" 2>/dev/null || true
  rm -r "$test_root"
}
trap cleanup EXIT INT TERM

for attempt in 1 2 3 4 5; do
  if curl --fail --silent http://127.0.0.1:39989/healthz >/dev/null; then
    break
  fi
  [ "$attempt" = 5 ] && exit 1
  sleep 0.2
done

admin_token=oneai-local-admin
proxy_token=oneai-local-proxy
```

### 2. 从 `api.json` 注入真实渠道

不要把下面变量打印出来，也不要启用 `set -x`。示例使用 `jq` 在内存中读取 URL、模型和密钥，然后通过管理 API 创建渠道；凭证会由代理写入系统密钥环。

```sh
openai_url=$(jq -r '.OpenAI.niuwa88.url' "$repo_dir/api.json")
openai_model=$(jq -r '.OpenAI.niuwa88.model[0]' "$repo_dir/api.json")
openai_key=$(jq -r '.OpenAI.niuwa88.key' "$repo_dir/api.json")
anthropic_url=$(jq -r '.Anthropic.niuwacc.url' "$repo_dir/api.json")
anthropic_model=$(jq -r '.Anthropic.niuwacc.model[0]' "$repo_dir/api.json")
anthropic_key=$(jq -r '.Anthropic.niuwacc.key' "$repo_dir/api.json")

admin_auth_header="Authorization: Bearer $admin_token"
curl --fail --silent -H "$admin_auth_header" -H 'Content-Type: application/json' -X POST \
  --data "$(jq -n --arg url "$openai_url" --arg key "$openai_key" --arg model "$openai_model" \
    '{channel:{id:"real-openai",name:"real-openai",protocol:"openai_responses",baseUrl:$url,capabilities:[],adminState:"enabled",failureAction:"cooldown",failureThreshold:3,priority:100,fallbackModel:$model,credential:{type:"bearer",secret:$key}}}')" \
  http://127.0.0.1:39989/api/admin/v1/channels/bundle >/dev/null
curl --fail --silent -H "$admin_auth_header" -H 'Content-Type: application/json' -X POST \
  --data "$(jq -n --arg url "$anthropic_url" --arg key "$anthropic_key" --arg model "$anthropic_model" \
    '{channel:{id:"real-anthropic",name:"real-anthropic",protocol:"anthropic_messages",baseUrl:$url,capabilities:[],adminState:"enabled",failureAction:"cooldown",failureThreshold:3,priority:100,fallbackModel:$model,credential:{type:"api_key",secret:$key}}}')" \
  http://127.0.0.1:39989/api/admin/v1/channels/bundle >/dev/null

# 代理直接按渠道优先级选择候选；两个渠道都已配置 fallbackModel，
# 因此当前 CLI 回归不需要写入全局映射或额外的渠道映射。
```

### 阶段 10 全局映射格式

设置页的“全局模型映射”提供两个独立的 JSON 编辑区。OpenAI 编辑区同时覆盖 `openai_chat` 与 `openai_responses`，Anthropic 编辑区覆盖 `anthropic_messages`。两个编辑区分别只填写自己的扁平对象，不在条目中重复写协议；下面先展示配置导出/API 返回时的总体结构：

```json
{
  "openai": {
    "modelA": "modelB"
  },
  "anthropic": {
    "claude-client": "logical-claude"
  }
}
```

OpenAI 编辑区的内容直接是：

```json
{
  "modelA": "modelB"
}
```

Anthropic 编辑区的内容直接是：

```json
{
  "claude-client": "logical-claude"
}
```

该格式属于阶段 10 的模型管理接口和配置导出 schema v5。CLI 回归仍可使用渠道目录或兜底模型，这是为了验证代理链路不依赖模型目录白名单，并不表示模型页接口未实现。运行时按请求协议选择对应协议族，只执行一次 `A -> B` 映射，不继续解析 `B -> C`。

### 3. 执行真实 CLI 请求

将上一步读取到的模型变量传给本节命令。成功条件是输出严格为 `OK` 且退出码为 `0`；同时检查代理日志和管理页请求记录，确认请求只到达 `127.0.0.1:39988`。

```sh
cat > "$codex_home/config.toml" <<EOF
model = "$openai_model"
model_provider = "oneai"

[model_providers.oneai]
name = "OneAI Proxy"
base_url = "http://127.0.0.1:39988/v1"
wire_api = "responses"
requires_openai_auth = true
env_key = "OPENAI_API_KEY"
EOF
codex_output=$(CODEX_HOME="$codex_home" OPENAI_API_KEY="$proxy_token" \
  codex exec --ephemeral --skip-git-repo-check --color never \
  --model "$openai_model" 'Reply with exactly OK and nothing else.')
[ "$codex_output" = "OK" ]

claude_output=$(CLAUDE_CONFIG_DIR="$claude_config" \
  ANTHROPIC_BASE_URL=http://127.0.0.1:39988 \
  ANTHROPIC_AUTH_TOKEN="$proxy_token" ANTHROPIC_API_KEY='' \
  claude -p --no-session-persistence --settings '{}' \
  --setting-sources user --tools '' --output-format text \
  --model "$anthropic_model" 'Reply with exactly OK and nothing else.')
[ "$claude_output" = "OK" ]
```

### 4. 故障切换回归（可选）

准备一个只返回 HTTP 500 的本地上游，将高优先级主渠道的 `baseUrl` 临时改为该地址，并新增一个优先级数值更低、指向真实上游且配置相同模型或兜底模型的备渠道。重复本节 CLI 命令，验收以下结果：主渠道按策略执行 3 次 `http_5xx` 重试，第 4 次切换备渠道并仍输出 `OK`；流式请求首事件后断流则只能保留主渠道已提交内容，不能拼接备渠道。故障上游、渠道和临时数据目录必须在 `cleanup` 中删除或停止。

### 5. 记录与清理

- 只记录版本、HTTP 方法/路径、非敏感 Header 名、模型字段名、SSE 事件类型、状态码、重试原因和退出码；不要记录 Authorization、API key、请求正文或响应正文。
- Codex 记录 `codex --version`，Claude Code 记录 `claude --version`；阶段 9 已验证 Codex `0.148.0`、Claude Code `2.1.220`。
- 保留 `stderr` 中的兼容性诊断，但区分 CLI 成功与代理失败；例如 `OutputTextDelta without active item` 不影响本次 `OK` 结果时应单独登记。
- 退出命令后先删除本流程创建的渠道，让代理清理系统密钥环中的真实凭证；再确认 `39988/39989` 和故障上游端口已无监听，最后由 `trap` 删除临时目录。不要删除或重启默认 `9988/9989` 实例。

## 故障排查

- 先访问 `http://127.0.0.1:9989/healthz`，确认管理监听器返回 `status: ok`。该接口无需认证，回包含 `status`、`database`、`proxyListener`、`adminListener` 和 `schemaVersion`，不含数据目录。
- 代理监听器只接受 `/v1/*`，直接访问根路径返回 404 是预期行为。
- 返回 401 时检查是否使用了代理令牌，而不是管理令牌或上游密钥。
- 端口冲突或同一数据目录已有实例时，启动命令会直接退出并显示冲突原因，不会自动改用随机端口。
- 修改监听 IP 或端口后，在管理页保存并重启发布包才会生效。

## 客户端兼容性快照

本节只记录已经验证的客户端行为。每次调整协议转发、鉴权、渠道选择、重试或流式处理后，按本文件的隔离流程复跑，并只记录非敏感请求形态。

| 客户端 | 协议 | 方法与路径 | 流式 | 当前证据 |
| --- | --- | --- | --- | --- |
| Codex CLI 0.148.0 | OpenAI Responses | `POST /v1/responses` | SSE | 隔离 `CODEX_HOME` 下真实最小请求返回 `OK`；备用渠道故障切换返回 `OK`。 |
| Claude Code 2.1.220 | Anthropic Messages | `POST /v1/messages` | SSE | 隔离 `CLAUDE_CONFIG_DIR` 下真实最小请求返回 `OK`；备用渠道故障切换返回 `OK`。 |

已验证：非流式、SSE、usage 元数据、首事件前故障切换均通过；首事件后断流只保留主渠道已提交内容；Responses 的 `previous_response_id` 保持原渠道亲和。阶段 9 最小请求未观察到 `/v1/models`、Responses 资源查询/取消、Messages `count_tokens` 或额外健康检查，这些接口暂不属于 V1 承诺。

错误边界：Responses 的 `response.failed`、`response.incomplete` 和 SSE `error`，Messages 的 `event: error` 均视为失败；Chat 的 `choices=[]` usage 帧不作为提交点，`data: [DONE]` 才是正常终止。HTTP 429、5xx、连接失败、非法 JSON、非法 SSE 和客户端取消由本地协议反馈环覆盖。

回归记录至少包含客户端版本、操作系统/架构、方法和路径、非敏感 Header 名、请求 JSON 字段名、SSE 事件类型、状态码、重试原因、CLI 退出码和伴随接口观察结果；禁止记录 Authorization、API key、请求正文或响应正文。
