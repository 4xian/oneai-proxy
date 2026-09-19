# 访问分组与代理密钥实施计划（2026-09-13）

> 文档定位：[`plans/oneai-proxy-solution-plan.md`](../plans/oneai-proxy-solution-plan.md) 是项目唯一权威实施计划；本文是其阶段 13 的详细规格，承载字段、接口、状态机、内部工作包和验收细节，不独立改变阶段优先级。两者冲突时必须先同步主计划，再修改本文。
> 状态：**阶段 13 可进入本地实施、尚未实现，实施契约须逐项验证**。阶段 9/11 剩余跨平台验收后置到 13E 最终发布 gate，不阻塞本地开发；本文不表示功能已编码、测试或可发布。
> 约束：简体中文；前端样式不用 `grid` / `gap`；前端方法上方中文注释；后端 Javadoc 风格注释；单文件约一千行上限；仓库内不留测试文件（临时验证后删除）。

本文覆盖范围：访问分组、列表密钥、渠道标签与访问组拆分、选路收窄、密钥与渠道美元额度、顶栏菜单、导出导入、渠道手动/自动禁用与探针恢复核对、全局/渠道敏感词拦截。
不覆盖：用户/RBAC/充值、渠道上游凭证池、复活 `route_groups`、渠道编辑「只能从模型目录加模型」（只记为后续，本计划不实现）、第三方内容审核云、用户正则规则。

### 数据兼容边界

- 本方案**只支持全新数据目录初始化出的 schema 28 数据库和新规则产生的数据**。实施与验收均使用空数据目录，不迁移、不读取、不修复 schema 27 及更早数据库中的渠道、令牌、请求或设置。
- 启动按第 2 节唯一 preflight 契约，在实例锁内以操作系统只读文件操作取得隔离副本，SQLite 只在副本恢复并查询 `schema_migrations`，不得直接以 `mode=ro` 打开原路径。确认空库后才允许从 version 0 建库；非空库缺少版本表或最高版本不等于 28 时，在原库 writable `Open`、PRAGMA 或迁移前拒绝，提示更换空数据目录。既不得自动升级旧库，也不得用旧二进制打开未来更高版本数据库；不得通过旧数据形态猜测并补写新字段。
- 已按本方案完成初始化且版本为 28 的数据库可以正常重启、备份、恢复和导入 schema 9 / migration v2 数据。配置 schema 7/8、migration v1、备份 schema 0/27 及其它旧格式一律拒绝。
- `is_history_placeholder` 只服务于**新方案运行期间**整包导入替换配置时保留已有请求日志外键，本文称为「日志占位渠道」。它不是旧版本数据库兼容入口，也不允许导入旧版渠道数据。

---

## 0. 已锁定产品规则（实现时不要改口）

### 0.1 主体

- 没有用户。一把**列表密钥**就是一个访问主体。
- 三种钥匙必须分开：

| 名称 | 作用 | 入口 |
| --- | --- | --- |
| 管理令牌 | 管后台 | 设置 → 访问令牌，保持现状 1 个 |
| 管理员通用代理令牌 | 打 `/v1/*`，**全部渠道**，**不限额度** | 仍是设置页当前那把「代理令牌」；**不进密钥列表**；默认值改为 `sk-oneai-admin-proxy` |
| 列表密钥 | 打 `/v1/*`，只看见它所属分组里的渠道，按目录单价累加美元 | 顶栏「密钥」 |
| 渠道凭证 | 转给上游 | 渠道编辑器，仍是 1 渠道 1 凭证 |

- 设置页代理令牌不进入密钥表；它始终是独立的管理员通用代理令牌。
- 新数据库中的 `channels.group_name` 结构改名为标签 `tag`，不表示访问组。所有通过新方案创建、编辑或导入的普通渠道都必须加入 `default`（见 0.2）。

### 0.2 关系

```
列表密钥 ──1:1── 访问分组 ──M:N── 渠道
                      ↑
         非默认组：分组页可多选渠道；default 组：分组页只读
         渠道编辑器可多选分组（必含 default）；两边写同一张成员表
```

- 密钥只能选 **一个** 分组；**可以不选**。分组被删后，该密钥的分组变为空，可见渠道为空。不改密钥 `status`。
- 渠道可属于多个分组。**新增和编辑保存时访问组必选，且必须包含 `default` 组**（可再勾其它组）。UI 上 `default` 预勾且不可去掉。
- 因此渠道列表不需要「未分组 / 未加入任何访问组」筛选项：所有普通渠道至少在 `default` 里。只有整包导入为保留日志外键生成的日志占位渠道可以没有成员，且它们不参与普通筛选和路由。
- 分组页：非默认组可增删改查，并用**可搜索多选下拉**维护该组渠道；与渠道编辑器写同一张成员表，必须同步。
- **非默认组替换 `channelIds` 时，每个渠道必须已经是 `default` 成员且不是日志占位渠道。** 不满足时返回 409「渠道数据不满足访问组约束」，分组页不得隐式补成员或清除占位标记。
- **`default` 组在分组页不能改渠道成员，只能查看。** 新建/编辑渠道保存时必含 `default`，分组页没有「往 default 里加渠道」的入口，也不允许从 default 里摘掉。
- 组上还有密钥时**允许删除组**（默认组除外）：密钥保留，`group_id` 置空，该密钥没有可用渠道。
- 删除组**不删除渠道**，只去掉成员关系。删的不是 default 时，不要顺手把该渠道的 default 成员删掉。
- **禁止停用 `default` 组。** 停用后所有只绑 default 的列表密钥会突然无路由。分组页 default 的启用开关只读且恒为开；PUT 带 `enabled: false` → 400。
- 系统预置 `default` 分组，作为兜底：
  - 名称只展示，**不能改名、不能删除、不能停用**
  - 备注可改
  - 渠道成员只读
  - 分组页不提供删除/改名/改成员/停用控件
- 普通渠道不存在“稍后回填 default”的过渡状态；创建、编辑、复制和导入必须在各自事务内同时写入 default 成员。

### 0.3 选路

选路改为读取 `BeginProxyRequest` 固定的同一不可变目录快照。**收窄必须带 `RouteScope`，贯穿整个请求生命周期**，不能只改第一次 `ResolveRoute`，也不能在后续 Attempt 混入 live 目录的另一代配置：

```
type RouteScope struct {
  PrincipalType string // "proxy_key" | "admin_proxy" | "none"
  PrincipalID   string // 列表密钥 id；通用令牌固定 "admin-proxy"；探针/测试 ""
  GroupID       string // 列表密钥当前组；可空。admin_proxy / none 不按组过滤
}
```

必须把同一个 `RouteScope` 传入：

- 初始 `ResolveRoute`
- `ResolveRouteTarget`（每次 Attempt 前只在同一 snapshot 内刷新目标）
- `ResolveRouteAffinity`
- fallback 候选
- 探针 / 编辑器测试 / 列表测试：`PrincipalType=none`，**不按组过滤、不扣额度、不跑敏感词**
- 不再做管理端模拟路由（已删除）

列表密钥：候选 ∩ 该密钥那一个**启用中**分组；`group_id` 空 / 组停用 → 无候选。
通用令牌：`PrincipalType=admin_proxy`，不按组收窄，**不扣密钥额度**，渠道额度仍生效。
请求取得 snapshot 后再删除组成员、停用组或修改映射，只影响后续新请求；本请求继续使用原 snapshot 的静态目录，不得切到其它代次，也不得放宽回全部渠道。渠道人工/健康/额度/删除和并发状态仍由每次 `PrepareAttempt` 动态复核；已删除或当前不可承接的渠道直接跳过。

Responses 亲和按主体隔离，见 2.5。

请求体和 Header **不带 group**。只有真实代理 Attempt 记账。

### 0.4 额度（密钥 + 渠道）

计价双方同一套公式，只读模型目录。金额用**整数微美元**（1 美元 = 1_000_000），禁止 SQLite `REAL` 做额度判断。

账本仍保存上游**原始** usage（`input_tokens` 对 OpenAI 是总量）。计费必须先按协议归一化成四项**互不重叠**的分量，再各自计价。禁止把 OpenAI 的总量 `input_tokens` 与其中的 `cached_tokens` / `cache_write_tokens` 直接相加。

```
micros = round(billable_tokens * price_per_million / 1e6 * 1_000_000)
       = round(billable_tokens * price_per_million)
```

四项（归一化后的 input / output / cacheRead / cacheWrite）各自四舍五入到微美元再相加。`round` = 远离零的 half-up。单价使用规范十进制字符串参与乘法，**禁止先把单价舍入为整数微美元**；每个分量只在 `billable_tokens × price_per_million` 的最终结果上舍入一次。口径见 0.4.1。

- `limit_micros = 0`：**不限额**。
- 所有管理 HTTP JSON、配置 schema 9 和 migration v2 中以 `Micros` / `_micros` 结尾的金额字段，wire 一律是**规范非负十进制字符串**：只允许 `0` 或无前导零的十进制整数，解析后必须落在 `int64`；禁止 JSON number。SQLite 与 Go 内部仍用 `int64`。前端美元输入最多 6 位小数，使用十进制字符串运算精确换算成 micros，禁止先转 `Number` 再乘 `1_000_000`；展示时同样从 micros 字符串精确格式化。这样 `int64` 全范围往返都不会越过 JavaScript `2^53-1` 后丢精度。
- 流式允许超额：开始时 `used >= limit`（且 limit > 0）才拦截；结束后再累加。
- 每个终态 Attempt 都结算（`billing_applied` 必置 1）；仅 `billed_micros > 0` 时加 used。同一金额同时加渠道，以及列表密钥（若有）。无 usage、不唯一的价格匹配都不扣额度。
- 推理 token 不加第二遍。
- 通用令牌不扣密钥额度；有可计金额时只扣渠道额度。计价状态必须先判断 usage、再判断价格、最后才标注主体：usage missing / invalid / 协议无法判定时为 `usage_unknown`；usage valid 但目录无价、价格歧义、非 USD、未知单位或当前价格模型无法表达时为 `unpriced`；只有 usage valid 且价格可计时，通用令牌才写 `admin`（列表密钥写 `priced`）。主体类型不得覆盖前两层诊断状态（见 2.4）。
- 密钥超限：`status=disabled` 且 `disabled_reason=quota_exhausted`。已是 `manual` 禁用时**只加 used，不改 reason**。
- 渠道超限：只写 `quota_blocked=1`，**不得改写** `health_state`、`last_error_class`、`last_http_status`。额度与上游健康是两套正交状态；列表和摘要用 `quota_blocked` 派生「自动禁用 · 额度用尽」。业务准入先看 `quota_blocked`，再看健康；探针/测试忽略额度门闩但不记账。探针和 `ResetChannelHealth` 都不得清额度门闩。
- 先到先停：密钥开始时已禁用则整请求拒绝；渠道额度满则跳过该渠道。

这里的美元额度按**目录价格推算的软限制**，不是上游实际支出的硬上限：在途请求可结算超额，usage 缺失或无可用价格时不扣额度。UI 与使用说明不得承诺「绝不会超过预算」或把该数字等同上游账单。

#### 0.4.1 按协议归一化 usage（计费前必做）

当前 `parseUsage`（`internal/server/protocol.go`）对 OpenAI 同时保存总量 `input_tokens`/`prompt_tokens` 和明细 `cached_tokens`，**还不读** `cache_write_tokens`。Anthropic 的 `input_tokens`、`cache_read_input_tokens`、`cache_creation_input_tokens` 是独立分量。官方口径：

- OpenAI Prompt Caching：`input_tokens` 是全部输入；`cached_tokens` 是其中的缓存读；`cache_write_tokens` 是其中新写入缓存的量。普通输入 = `input_tokens - cached_tokens - cache_write_tokens`。缓存读、缓存写、普通输入分属三个价目，**不得**把总量再加一遍明细。参考：https://developers.openai.com/api/docs/guides/prompt-caching
- Anthropic Prompt Caching：`total_input_tokens = input_tokens + cache_creation_input_tokens + cache_read_input_tokens`。三个字段直接相加，**不要**从 `input_tokens` 里再扣缓存。`usage.cache_creation` 还可能把同一次响应的写入拆成 `ephemeral_5m_input_tokens` 与 `ephemeral_1h_input_tokens`，两类可以同时为正；官方价格分别是基础输入价的 1.25 倍与 2 倍，不能把合计 token 无条件套入一个无 TTL 语义的 cache-write 单价。参考：https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching 、https://docs.anthropic.com/en/docs/about-claude/pricing 、https://docs.anthropic.com/en/api/rate-limits

实现：

1. usage 字段使用**三态解析**：字段未出现为 missing；OpenAI Responses 流式事件的 `response.usage:null`、Chat 流式 chunk 的顶层 `usage:null` 是协议允许的「尚未提供」，同样为 missing，不得设置 `UsagePresent` 或覆盖已有合法 usage；后续合法最终帧仍可合并计费。**仅上述 OpenAI 流式 usage 容器允许 null**，数值字段或明细对象出现 `null` 仍为 invalid；非 null 的 usage 容器格式错误、字段类型错误、负数或超出 `int64` 也为 invalid。invalid 在 SSE 合并中是粘性的，后续合法帧不得覆盖；最终仍缺必要字段为 `usage_unknown`。`parseUsage` / `usageInt64` 不能再把 missing 与 invalid 都折叠为 `(0,false)`。依据：[Responses 流式事件](https://developers.openai.com/api/reference/resources/responses/streaming-events.md)、[Chat 流式事件](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events.md)。
2. OpenAI（Responses / Chat）解析时，除现有字段外必须读取 `input_tokens_details.cache_write_tokens`（Chat 则 `prompt_tokens_details.cache_write_tokens`）。缓存明细字段**缺失**视为 0；明细对象或字段**出现但非法**则整次 usage invalid。账本 `input_tokens` 仍写上游总量，便于详情展示。
3. Anthropic 非流式继续读取顶层 `usage`；流式必须在 `message_start.message.usage` 读取 input / cache read / cache creation，并与后续 `message_delta.usage` 的 output 按字段合并。最终快照必须同时观察到合法 input 和 output；缓存字段缺失可按 0，出现但非法则整次 usage invalid。若 `usage.cache_creation` 出现，识别其中 5m/1h 明细用于协议校验与诊断；明细缺失项按 0，已出现项必须是非负 `int64`，且两项之和必须等于 `cache_creation_input_tokens`，否则 usage invalid。当前阶段不把这两项误塞进单一 `cache_write_input_tokens` 后猜测计价。
4. 计费分量：

| 协议 | billable input | billable cacheRead | billable cacheWrite | billable output |
| --- | --- | --- | --- | --- |
| OpenAI | `input - cacheRead - cacheWrite` | `cached_tokens` | `cache_write_tokens` | `output_tokens` |
| Anthropic | `input_tokens`（原样） | `cache_read_input_tokens` | `cache_creation_input_tokens` | `output_tokens` |

5. 下列任一情况 **不得猜测收费**，整次 Attempt `billing_status=usage_unknown`、`billed_micros=0`：
   - 上游没有 usage
   - OpenAI：`cached_tokens + cache_write_tokens > input_tokens`
   - 任一计费分量算出来为负
   - 协议无法判定（不是 OpenAI / Anthropic）
   - 最终缺少协议必要字段，或任一已出现的 usage 容器/字段为 invalid
6. 推理 token 不加第二遍（仍只作展示）。
7. **Anthropic 例外**：只要合法 `cache_creation_input_tokens > 0`，当前四价模型只有一个不带 TTL 语义的 cache-write 价格，无法唯一表达 5m、1h 或混合写入；整次 Attempt 必须是 `usage_validity=valid`、`billing_status=unpriced`、`billed_micros=0`，不得只给其它 input/output 分量收费。管理详情返回稳定派生原因 `anthropic_cache_write_ttl_unexpressible`。`cache_creation_input_tokens=0` 不触发本规则。若未来要精确计价，必须在同一版本同时增加两类 usage、两类目录价格、两类 Attempt 价格快照与对应 wire/UI，不能只多解析两个字段后继续套单一价格。
8. 除上条 Anthropic 例外外，目录无对应 cache 单价时：该分量为 0，其余分量照常；不是整单 unknown。四个单价都空才是 `unpriced`。

### 0.5 密钥明文

- 列表密钥：**总长 24**（含前缀 `sk-oneai-`）。实现：`sk-oneai-`（9 字符）+ 15 位密码学随机 `[a-zA-Z0-9]`。展示字段 `token_prefix` 固定取完整 token 的前 **15** 字符，即固定前缀加前 6 位随机串；只取前 9 字符没有区分度，禁止继续使用。
- 列表默认遮罩；「查看」显示明文，「复制」写入剪贴板。创建、轮换、查看三个接口统一返回 HKDF+AES-GCM 加密 envelope，由前端使用当前管理令牌解密；管理 API、审计和运行日志均不出现明文 token。响应设置 `Cache-Control: no-store`。
- 明文放密钥环，库内只留哈希 + `secret_ref` + 前缀。
- 管理员通用代理令牌默认值固定为 `sk-oneai-admin-proxy`。新数据库首次初始化直接写入该值；不存在旧默认值识别、回显或迁移分支。
- 浏览器令牌 localStorage key 同步换成 v2 命名（`oneai-proxy-v2-admin-token`、`oneai-proxy-v2-proxy-token`、`oneai-proxy-v2-pending-admin-token`）。pending key 只允许通过共享 `PendingAuthTokenHandoffV1` 类型和唯一严格编解码入口读写，wire 固定为 `{"version":1,"opId":"…","adminToken":"…","proxyToken":"…"}`；解析时要求对象、`version=1` 及三个非空字符串字段，缺少/未知 version、未知字段或错误类型都拒绝使用并进入重新输入令牌的失联处理，禁止退回无 version 对象或裸字符串。该记录用于管理令牌保存和导入/恢复 handoff 的浏览器崩溃收口。旧 key 不读取、不迁移，也不主动删除；这样全新数据库不会被同源浏览器残留的旧代理令牌覆盖。
- 设置页**去掉「轮换管理令牌 / 轮换代理令牌」按钮**。换令牌只走输入框保存：用户填新值并保存，旧值立即失效。轮换按钮只是服务端随机生成一串再写回输入框，和自己改完保存是同一件事，本机场景不需要。后端 `RotateAuthToken`（POST 不带 `token`）本计划不强制删除，但管理 UI 不再调用。

### 0.6 密钥启停 vs 渠道启停 vs 探针

| 对象 | 状态 | 谁改 | 探针能否恢复 |
| --- | --- | --- | --- |
| 列表密钥 | `status` = `enabled` / `disabled`，另用 `disabled_reason` 区分 `manual` / `quota_exhausted` | 人工启停；额度用尽 → `disabled` + `quota_exhausted` | **否**。探针不碰密钥 |
| 渠道人工禁用 | `admin_state = disabled` | 列表「禁用」按钮 / 编辑器 | **否** |
| 渠道自动禁用 | `health_state = auto_disabled` | 仅由失败策略 `failure_action = auto_disable`（上游错误）写入 | **是**（定时探针且 `auto_recover`） |
| 渠道额度门闩 | `quota_blocked = 1` | Attempt 结算或降低限额 | **否**。探针完全不碰额度门闩 |
| 渠道冷却等 | `cooldown` / `half_open` / `degraded` | 健康状态机 | 按现有探针策略 |

**当前代码已经符合「探针只恢复自动禁用、不恢复手动禁用」**，见 1.2。额度用独立字段，**不要只靠 health_state 表达额度**。

密钥：

下表「自动恢复 enabled」仅适用于已经物化（非空 `secret_ref`、真实 token hash）的密钥。`pending:<id>`、空 ref 的 safe 占位密钥在额度操作后必须保持 `status=disabled`：仍超额时保留 `quota_exhausted`，额度解除时改成 `manual`，不能清空 reason。额度金额照常更新；轮换补真实 token/ref 不改 status/reason，随后才允许单独 `/state` 显式启用。

| 动作 | 行为 |
| --- | --- |
| 额度用尽 | `status=disabled`，`disabled_reason=quota_exhausted`；若已是 `manual`，保持 `manual`，只加 used |
| 人工停用 | `status=disabled`，`disabled_reason=manual` |
| 提高 limit 且 used < 新 limit | 若 reason 是 `quota_exhausted`，已物化密钥改回 `enabled`、清空 reason；safe 占位密钥改成 `disabled + manual`；原 `manual` 不变 |
| 改为 limit=0（不限额） | 若 reason 是 `quota_exhausted`，已物化密钥改回 `enabled`、清空 reason；safe 占位密钥改成 `disabled + manual`；原 `manual` 不变 |
| 降低 limit 且新 limit > 0 且 used ≥ 新 limit | 若当前 enabled → `disabled` + `quota_exhausted`；`manual` 不改 reason |
| 重置 used=0 | 若 reason 是 `quota_exhausted`，已物化密钥 → enabled；safe 占位密钥 → `disabled + manual`；原 `manual` 仍禁用 |
| 人工启用 | 只有已物化密钥的 `manual` 可启用；占位密钥 → 400、提示先轮换；若 used ≥ limit > 0 → 400，先重置或提高限额 |
| 在途 Attempt | 已领取的 Attempt 跑完仍记账；下一请求才拒绝 |

密钥元数据与人工状态使用两个接口，避免 `limit` 自动状态变化和显式人工操作互相覆盖：

- `PUT /proxy-keys/{id}` 只更新 name / note / groupId / limit；在同一事务内按上表用**新 limit**重算 `quota_exhausted` 状态，不接受 `status` 字段。
- `PUT /proxy-keys/{id}/state` 只执行人工启停。停用固定写 `disabled + manual`；启用时必须先确认 token/hash/ref 已物化且仍可读取，否则返回 400「请先轮换」，再检查 `used >= limit > 0` 并返回 400，否则写 `enabled` 并清空 reason。
- 两个请求各自原子执行；UI 保存元数据和点击启停是两个独立动作，不发送组合请求，因此不存在字段优先级。

渠道限额与门闩：

| 动作 | 行为 |
| --- | --- |
| 额度用尽 | `quota_blocked=1`；不改 `admin_state`、`health_state`、`last_error_class`、`last_http_status` |
| 提高 limit 且 used < 新 limit | `quota_blocked=0`；健康状态保持原值 |
| 降低 limit 且 used ≥ 新 limit > 0 | `quota_blocked=1`；健康状态保持原值 |
| 改为 0（不限额） | 立即 `quota_blocked=0`；健康状态保持原值 |
| 在途 Attempt | 已领取的 Attempt 跑完仍记账；可能再次把门闩打上 |
| 探针恢复 | 只处理上游健康，**必须保留 `quota_blocked`**；业务准入仍跳过，探针/测试准入忽略额度门闩 |
| `ResetChannelHealth` | 只清健康计数/状态；**不清额度门闩**，展示仍由 `quota_blocked` 判定为额度用尽 |
| 重置 used | `quota_blocked=0`；健康状态保持原值 |
| 人工 `admin_state=disabled` | 重置额度**不**自动启用 |

目录 `available`：**必须** `quota_blocked=0`。`quota_blocked=1` 时即使 `health_state=healthy` 也不算可用，摘要 `Available` 不加。展示原因以 `quota_blocked` 为独立来源，优先于健康状态。摘要按有效状态统计：`AutoDisabled = quota_blocked=1 OR health_state=auto_disabled`；`Disabled = admin_state=disabled OR quota_blocked=1 OR health_state=auto_disabled`。同一渠道在每个摘要中最多计一次，不能因为多个条件同时成立而重复，也不能因为探针把健康恢复为 `healthy` 就漏掉额度停用渠道。

### 0.7 界面

顶栏顺序：

`概览` · `渠道` · `分组` · `密钥` · `模型` · `请求` · `日志` · `设置`

设置 → 访问令牌：管理令牌 + 管理员通用代理令牌（文案标明：可访问全部渠道，不受分组和密钥额度限制；渠道额度仍生效）。只有输入框 + 保存，没有轮换按钮。

渠道列表增加「已用额度 / 总额度」列。限额为 0 时显示「不限制」和已用金额。
自动禁用必须带**原因**（额度用尽 / 上游 502 / 上游 404 等），不能只写「自动禁用」。

设置 → 新增侧栏「敏感词」：全局开关（默认关）+ 关键词列表。
渠道编辑器新增「敏感词拦截」卡片：开关（默认关）+ 同一套关键词编辑。

---

### 0.8 敏感词拦截

两层，**默认都关闭**。关闭或词表为空 = 不拦截。

| 层 | 入口 | 何时检查 | 命中后 |
| --- | --- | --- | --- |
| 全局 | 设置 → 敏感词 | 正文快照之后、选路之前 | 整次请求拒绝，不选路、不打上游 |
| 渠道 | 渠道新增/编辑 | `PrepareAttempt` 原子取得可取消的半开/并发 reservation 与精确代次凭证之后、提升为 `AttemptLease` 之前 | **整次请求拒绝**；立即无副作用释放 reservation，不消费半开结果、本候选不建 Attempt、不再 fallback；此前已结算 Attempt 保留 |

渠道词表只约束已经原子取得 reservation、在该线性化点真正可承接本次请求的候选：人工禁用、健康禁用、额度耗尽、凭证不可用或并发已满的渠道先返回 `Skipped(reason)`，**不得执行该渠道词表，也不得凭词表拦截请求**。reservation 只在模块内短暂占位；词表命中时立即释放，不创建 Attempt、不写健康成功/失败、不消费半开结果。可承接候选的渠道词表一旦命中则整次请求 400；这不是“换下一个渠道”的失败。

探针、编辑器测试、列表测试：**不跑敏感词、不记账额度**。

**匹配方式（v1 锁定：规范化关键词子串，不用正则）**

正则不适合本机词表：用户易写错、有 ReDoS、和「输入框加词」对不上。v1 固定：

1. 词：输入框回车添加，Tag 展示，点 × 删除；保存前 trim、去空、按规范化结果去重。单条最短 **2 个 Unicode 字符**、最长 64；每张表最多 **200** 条。
2. 规范化（词和正文都做）：NFKC（依赖 `golang.org/x/text/unicode/norm`，实现时写入 `go.mod`）→ Unicode 小写 → **删除全部空白**（空格、制表、换行、NBSP）→ trim。
   因此词「微 信」与正文「微信」规范化后都是「微信」，**可以命中**。不是「折叠成一个空格」。
3. 命中：规范化后的正文里 **包含** 规范化后的词（子串）。大小写不敏感；`WeChat` 能拦住 `wechat`。
4. **不**把用户输入当正则，**不**做拼音/谐音。**不做英文单词边界**：`bad` 会命中 `badge`。
5. 扫描对象：`readProxyBody` 已得到的 JSON 对象里除第 6 条协议图片载荷外的**所有字符串值**（递归 `messages` / `content` 数组 / `input` / `system` / tool 参数）。不扫对象键名、不扫请求头。非法 JSON 在读 body 阶段已 400，敏感词扫描不会碰到 malformed JSON。
6. 仅按**当前请求协议、合法内容块位置和类型**跳过明确图片载荷；该判定是 engine 的共享协议字段规则，不由调用方另传可任意指定的跳过路径：
   - OpenAI Responses：`input[].content[]` 中 `type=input_image` 的 `image_url` 字段；只有该值为图片 data URL 时跳过。
   - OpenAI Chat：`messages[].content[]` 中 `type=image_url` 的 `image_url.url` 字段；只有该值为图片 data URL 时跳过。
   - Anthropic Messages：`messages[].content[]` 及其协议允许的 `tool_result.content[]` 内，`type=image` 且 `source.type=base64`、`source.media_type` 为合法 `image/*` MIME 的内容块，仅跳过 `source.data` 字符串。
   - 图片 data URL 必须通过 URL/MIME 结构解析，具有 `data:` scheme、合法 `image/*` MIME、`;base64,` 分隔和合法 base64 载荷；仅匹配开头不够。Anthropic data 同样校验 base64 编码形态。此处只识别编码载荷，不解码图片像素、不新增 OCR。
   - 普通 `text`、字符串形式 `input`/`system`、tool 参数及未知结构始终扫描；即使长度超过 256、全是 base64 字符、可以成功 base64 解码，或以 `data:image/` 开头，也不得跳过。不因某个对象有 `type=image` 就跳过整个对象或其相邻文本，合法结构以外的同名字段不取得豁免。不符合上述白名单时正常扫描，原有协议校验错误仍按原规则返回。
7. `ContentFilterEngine` 在每次不可变运行快照发布时，把全局与全部已启用渠道词表编译成一个多 owner 的 Aho-Corasick 多模式匹配器；相同规范化词只进入自动机一次。编译结果同时保存紧凑的 `owner -> patternIds` 倒排索引，但扫描命中时只记录有界的 `matchedPatternIds` 及每个模式最早位置，**不得把一个共享模式展开成所有 channel owner**。每个请求只递归遍历 JSON 一次，每个字符串只规范化一次并独立重置自动机，禁止拼接字段；扫描结果是不可变 `FilterEvaluation{globalMatch, matchedPatternIds}`。全局过滤直接读取 global 结果；`PrepareAttempt` 只有在候选成功取得 reservation 后才用该渠道的 patternIds 与 matched set 求交，因此预计算不会让不可承接渠道改变行为，也不会随候选数重复扫描正文或按 owner 扇出结果。
8. 单请求参与过滤的规范化字符串总量上限为 **8 MiB UTF-8 字节**；统计按实际规范化输出累加，不含第 6 条按协议字段确认的图片载荷。超过上限在正文快照后返回 HTTP 413、code=`content_filter_input_too_large`，不选路、不建 Attempt。规范化和匹配至少每处理 64 KiB 检查一次请求 context；取消后立即停止，不得继续占用 CPU。JSON 遍历固定为深度优先：对象键按解码后键名的 UTF-8 字节升序、数组按原下标顺序；仅为参与扫描的字符串依次分配遍历序号，每个字符串仍独立重置自动机。每个模式保存最小的 `(字符串遍历序号, 规范化后字符串内 Unicode 起点)`，起点从 0 计数，不能用字节偏移或自动机首次输出顺序替代。全局或 `PrepareAttempt` 查询某个渠道 owner 时，先按该二元组升序选择该 owner 的命中词；同位置再按规范化词 Unicode 升序取第一条。不得依赖 Go map 迭代或 JSON 对象键书写顺序，确保同一 JSON 值的账本结果稳定。
9. 客户端正文快照必须在全局过滤**之前**写入（见 3.4）。命中后管理员在请求详情可看正文和命中词。客户端 400 不含命中词。

运行快照的编译容量也必须有界：全部启用词表合并去重后最多 **10,000** 个规范化模式，模式 UTF-8 字节总量最多 **1 MiB**；全局/渠道 owner 与模式的关联总数最多 **100,000**，规范化 owner id 加关联索引的序列化字节总量最多 **4 MiB**。渠道/全局词表保存、schema 9 导入、migration v2 导入和恢复都必须在提交前对候选快照完成规范化、四项容量校验与 Aho-Corasick/倒排索引编译；超过任一上限返回 409、code=`content_filter_capacity_exceeded`，保持旧快照与旧配置不变。禁止先提交无法编译的词表再在发布阶段失败，也禁止按渠道数创建 N 份正文、N 次扫描或命中后展开全部 owner。

**拒绝形态**

- HTTP **400**
- JSON：`{"error":"请求包含敏感内容，已被拦截","code":"content_filtered"}`
- **不要把命中的词回给客户端**（避免试探词表）。请求账本 / 详情给管理员看：哪一层、哪一条词。
- 已建请求行：终态 `error`、`errorClass = content_filtered`，并写结构化字段 `content_filter_scope`（`global`/`channel`）、`content_filter_matched_word`、渠道命中时的 `content_filter_channel_id`；另写路由事件 `request_rejected`（reason=`content_filtered`）。全局过滤在任何渠道尝试前命中时整次请求无 Attempt、无计费；渠道过滤只保证**命中的这个候选**不新增 Attempt、不增加本候选费用/健康结算且不再 fallback。若此前渠道已完成 Attempt 后 fallback 到本候选，原 Attempt、费用和健康结算全部保留，不能回滚；请求 `attempt_count` 和金额仍按现有 Attempt 账本汇总，原路由事件保留并追加拒绝事件。不要只靠 `error_message` 自由文本。

**请求账本何时创建（工作包 B 建立主体账本顺序，工作包 D 在同一顺序接入敏感词）**

| 情况 | 建 `requests` 行 | 写 `request_route_events` | 写审计 |
| --- | --- | --- | --- |
| Bearer 无效 | 否 | 否 | 否（现有 401 即可） |
| 列表密钥停用 / 额度用尽 | **是**（鉴权后、读 body 后） | `key_disabled` | 否 |
| 读 body 失败 | 否 | 否 | 否 |
| 全局敏感词命中 | 是 | `request_rejected` reason=`content_filtered` | 否 |
| 无组 / 组停用 / 组成员为空 | 是 | `group_mismatch` | 否 |
| 组有效且有成员，但协议/模型/能力不匹配 | 是 | `routing_exhausted` | 否 |
| 渠道敏感词命中 | 是（已存在） | `request_rejected` reason=`content_filtered` | 否 |
| 正常转发 | 是 | 现有事件 | 否 |

顺序（`proxyAuth` 与 `forwardAPI` 的 seam，见 3.1 / 3.4）：

```
入口：用不可变认证索引轻量鉴权 Bearer、解析 keyId 并检查维护状态，不取得 RequestLease、不以缓存 quota 作最终准入；无效 → 401，draining → 503，均不读 body、不建账本
forwardAPI：在无全局 lease 状态下限长、限时，只读一次 body
→ 缺 model 等 → 400，不建账本
→ BeginProxyRequest 按 keyId 查询当前 tombstone/hash/manual/quota/group，在同一步取得不可变主体/目录快照和 RequestLease；失败 → 401/503
→ 建请求账本（客户端模型；访问组名称用鉴权时快照）
→ 写客户端正文/请求头快照（失败则账本 error + 500，不再过滤/选路）
→ 密钥停用则 403
→ 全局敏感词（开且命中则 400，写 scope/词）
→ 选路（成功后补写 logical_model）
→ 每个候选：PrepareAttempt 预检查 → 原子取得可取消的准入 reservation + 精确代次凭证 → **仅对成功 reservation 的候选执行渠道敏感词** → 通过后提升为 AttemptLease，命中则无副作用释放并结束整次请求；此前已结算的 fallback Attempt 保留
→ 转发
```

---

## 1. 当前代码接缝（实现时对照，不要猜）

| 现状 | 处理 |
| --- | --- |
| `channels.group_name` + API `group` + 列表「分组」列/筛选 | **改名为标签 `tag`**，不再表示访问组 |
| `requests.group_name` / `attempts.group_name` | 继续作为**渠道标签快照**（随渠道 `tag` 改名）；访问组另加字段 |
| `ResolveRoute` 读 `c.group_name` 只为填 `RouteTarget.GroupName`，不参与过滤 | 标签快照可继续带；访问组过滤用成员表 |
| `proxyAuth` 只认全局 `ProxyHash` | 改为：列表密钥 **或** 现有代理令牌；**与组过滤同一发布** |
| `AuthTokens` / `auth-proxy-token` / `LocalDefaultProxyToken` | **保留结构**；全新数据库的默认常量固定为 `sk-oneai-admin-proxy`，不识别旧默认值 |
| 设置页「代理令牌」+ 两个轮换按钮 | 改文案与默认 placeholder；**去掉轮换按钮**，只保留输入框保存；浏览器令牌 key 切到 v2，忽略旧 localStorage |
| SQLite `schemaVersion = 27`，主 handle `SetMaxOpenConns(1)`，WAL，`busy_timeout = 5000` | 全新数据目录一次初始化到 schema **28**（标签、访问组、密钥、额度、敏感词、亲和、计费列全部建立）；已存在的非 28 数据库拒绝启动。业务主 handle 仍单连接；恢复点快照使用 `Snapshotter` 自己的第二个只读单连接，以真实读取固定源事务快照后执行 SQLite online backup，不占用主连接池 |
| 导出 `schemaVersion = 8` | 升到 **9**；迁移包 `migrationVersion` 升到 **2**。**只接受当前版本**：配置导入拒绝 ≠9；迁移导入拒绝 ≠v2 或 `databaseSchemaVersion ≠ 28`；备份拒绝 `manifest.schemaVersion ≠ 28`（去掉 version 0 兼容） |
| `backup.go` / `sqlite.go` 硬编码表清单 + `SELECT *` | 清单加入新表；删除子→父、插入父→子；`INSERT` 写**显式列名**，禁止 `SELECT *` |
| `response_affinity` 仅 `response_id` | 重建为 `(principal_type, principal_id, response_id)`；旧行删除，不归给任何主体 |
| 已删除的 `route_groups` | **禁止复活** |
| 渠道保存只走 bundle | 渠道侧分组成员、渠道额度字段随 bundle 提交 |
| 渠道凭证 GET 加密、前端解密 | 列表密钥回显复用同一套 wrap |
| `admin_state` vs `health_state = auto_disabled` | 保持现有分工；渠道额度只写独立 `quota_blocked`，展示层派生有效自动禁用，禁止污染健康状态 |
| 设置侧栏「全局渠道设置」 | 仍是 reasoning / service_tier；敏感词**另开**「敏感词」分区，不要塞进 ChannelSettings |
| `proxyAuth` 失败立刻 401 | middleware 只放 context；停用密钥交给 `forwardAPI` |
| `CreateRequest` 在选路成功之后 | 读 body 后、选路前建账本；逻辑模型后补 |
| 正文快照在选路成功之后 | 全局过滤前写入 |
| 导入响应含明文 `adminToken`/`proxyToken` | 删除明文字段；complete/migration/restore 成功后只返回绑定 opId/requestId、用提交前管理 Bearer 封装的一次性 `authTokenHandoffEnvelope` |
| `VerifyBackupManifest` 接受 schema 0 | 只接受 28 |
| 目录 `available` 只看健康 | 必须 `quota_blocked=0` |
| 现有备份无密钥环 | 统一为**同实例恢复点**：加密 secret bundle（含 content-master 与全部 `content_blobs.key_ref`），restore 走 sidecar journal，并补齐 List/Delete、`RecoverPendingBeforeRuntime` 与 `RecoverArtifactJournals` 生命周期 |
| `migrationPayload` 独立白名单 | v2 必须含组/密钥/敏感词，且 `configSchemaVersion=9` |
| `configImportAPI` / 备份 / 恢复只持 `dataMu` | 引入 `RuntimeCoordinator`：普通写走短提交；配置/迁移整包替换和恢复走显式 draining + 有时限等待 + 短提交，不向 handler 暴露锁 |
| `cleanupExpiredLogs` / 手动 `logs.cleanup` 先清正文、再独立删除父 Request；`content_blobs.request_id ON DELETE CASCADE` 可绕过正文 pin，且恢复可能在两步之间切换 live | capture 注册时先取得不持 mutex 的删除 fence；online backup 完成并从快照收集 refs 后原子收窄为精确正文/secret pin。自动和手动清理整个“正文→父 Request”流程均持 maintenance `OperationLease`，draining 等待其结束；父行必须经正文模块按当前集合条件删除，不能靠外键级联清理正文 |
| `CreateCompleteBackup` 在主单连接执行 `VACUUM INTO`，随后从 live DB 读 `content_blobs` | 新 `Snapshotter` 通过独立只读 SQLite handle 固定源读取事务后使用 modernc online backup 分页复制，并受 120 秒总时限约束；`RecoveryPointStore.Create` 的 secret / 正文 refs **只从产出的同一快照**收集，不占用主连接、不查询 live；创建采用 `.creating-<id>` journal 并原子发布 committed |
| `nextChannelID` 复用首个空闲 `channel-NNN`，现有 bundle 新建也接受显式新 ID | schema 28 为每行增加服务端生成、更新时不可变的 `channel_identity`；可读 id 即使复用也不能被旧 snapshot 误认。只有 `POST /channels/bundle` 可新建且拒绝非空 `bundle.channel.id`，复制同样不接受调用方指定 id/identity；`PUT /channels/{id}/bundle` 只认路径 id。配置/迁移只携带逻辑 id、导入时生成新 identity；同实例恢复保留快照 identity |
| `runtimeResponseWriter` / `auditResponseWriter` 包装后没有 `Unwrap()` | 两个 wrapper 及以后新增的 ResponseWriter wrapper 都实现 `Unwrap() http.ResponseWriter`，保证 `http.ResponseController.SetReadDeadline` 能穿透真实管理中间件链；设置/清除 deadline 失败按 3.1 fail-closed |
| restore 的 `oldContentDir` 删除失败后只记日志 | committed restore sidecar 持久记录旧目录清理状态；删除并 fsync 成功前不得删 marker/sidecar，启动和后台幂等续作，避免旧正文目录失去清理 owner |
| `settleChannelHealthTx` 在健康版本不匹配时仍无条件清 `half_open_claimed` | schema 28 为半开占用增加高熵 owner；领取、释放和结算都绑定 `channel_identity + channel_config_version + health_version + owner`，旧配置或旧健康 lease 都不能释放新代次占用 |
| 固定 snapshot 的旧 Base URL/凭证可在渠道编辑后才进入准入，而准入会领取当前 `health_version` | schema 28 增加独立 `channel_config_version`；snapshot、reservation、Attempt 与健康结算同时核对 identity/config/health 三类代次。旧配置业务 Attempt 可按既定规则完成和计费，但对当前配置健康中性且不得领取当前配置的半开 owner |
| `replaceFromBackup` `DELETE FROM app_settings` | restore journal **禁止**放 `app_settings`；放 `{dataDirectory}/restore.journal` |
| `cmd/oneai-proxy/main.go` Open 后立刻 LoadRuntimeSettings / LoadOrCreateAuthTokens | `RecoverPendingBeforeRuntime` 必须夹在 `storage.Open` 与普通启动收口之间；若返回 committed capability，则在 settings/auth/content 加载和初始 snapshot 发布后、绑定监听器前完成 finalization |
| `parseUsage` 把 OpenAI 总量与缓存明细并存 | 计费前按协议归一化；OpenAI 扣除缓存读写 |
| `ModelPricing` 使用 `float64`、Attempt 单价快照使用 `REAL` | 改为规范十进制字符串；目录解析、快照和计费全程不经过 IEEE 浮点 |
| `NewAuthTokens` 允许管理令牌 = 代理令牌 | 三类令牌全局两两 hash 唯一 |
| `adminAuth` 在 handler 提交配置前只鉴权一次 | 管理请求先做 Host/Origin + 初次鉴权并在锁外限长/限时读取解析；进入 `RuntimeCoordinator` 提交阶段后以当前管理令牌再次鉴权，禁止信任旧鉴权快照 |
| HTTP Server 未设置 `ReadHeaderTimeout`，请求体读取可阻塞 | 增加 `ReadHeaderTimeout`、请求体读取 deadline/大小上限和 `IdleTimeout`；SSE 不使用短全局 `WriteTimeout`，继续由总时限、流空闲时限和逐次写期限约束 |
| 系统密钥环 service/ref 对所有 `--data-dir` 相同，实例锁却只锁单个目录 | 每个全新数据目录创建稳定 `instanceSecretNamespace`，并用规范化数据目录身份 + SQLite 本机键绑定；所有逻辑 ref 先经命名空间适配器映射后才能进入系统密钥环或本地 fallback |
| `FallbackStore.Put` 降级写本地成功后，`Get` 仍优先读取系统密钥环旧值 | 为每个逻辑 ref 持久记录权威后端和随机 generation；秘密摘要只放在后端内部封装中；Get 只读权威副本，Put 读回验证并原子发布权威记录后才算成功 |

### 1.1 SQLite 与并发额度

当前打开库时 **最多 1 个连接**。所有 SQL 已经串行。

- `UPDATE ... SET dollar_used_micros = dollar_used_micros + ?`（密钥表、渠道表各一次）不会丢更新。金额是整数微美元，不是 `REAL`。
- 超额来源主要是多路流式同时结束再累加，已接受。
- **不要**为额度把 `MaxOpenConns` 调大。

### 1.2 渠道手动禁用 / 自动禁用 / 探针（已核对，实现时对齐不要改歪）

现有模型（`internal/storage/health.go`、`channel_admission.go`、`channel_directory.go`）：

1. **人工禁用**只写 `channels.admin_state = 'disabled'`（`UpdateChannelAdminState`）。不改 `health_state`。
2. **准入**（阶段 13 统一由 `PrepareAttempt` 调用底层 lease 原语）：`admin_state == disabled` → 跳过，原因 `admin_disabled`。业务请求遇到 `health_state == auto_disabled` → 跳过，原因 `auto_disabled`。
3. **健康结算**一开始：若 `adminState == disabled`，**不改健康状态**，只释放半开占用。因此探针结果打不醒人工禁用渠道。
4. **探针恢复**只发生在 `health_state == auto_disabled && probeEnabled && autoRecover && purpose == automatic_probe`，并且连续成功次数达到阈值，才把 `health_state` 拉回 `healthy`。
5. 列表「禁用/启用」走 `PUT .../state` 的 `adminState`；「人工恢复健康」走 `ResetChannelHealth`，注释写明**不会启用人工禁用渠道**。
6. 业务失败是否进入 `auto_disabled`，取决于渠道 `failure_action`（`cooldown` 或 `auto_disable`）。

结论：**当前逻辑已经是「探针只恢复自动禁用，手动禁用不恢复」**。本功能不需要重写状态机。要做的只是：

- 渠道额度用尽 → 只写 `quota_blocked=1`，**不动** `admin_state`、`health_state`、`last_error_class`、`last_http_status`。不能用额度原因覆盖既有上游故障。
- 展示上额度用尽算有效自动禁用：目录摘要分别按 0.6 的 `AutoDisabled` / `Disabled` 条件去重统计，而不是只看 `health_state`。
- `PrepareAttempt` 的 `purpose` 固定为 `business | automatic_probe | manual_probe | admin_discovery`。仅 `business` 时，`quota_blocked=1` 或（`dollar_limit_micros > 0 && dollar_used_micros >= dollar_limit_micros`）→ 跳过，原因 `quota_exhausted`。自动/手动探针、**已保存渠道**的编辑器测试和模型发现都忽略额度门闩，保证额度停用渠道仍可核对上游；这些非业务路径不计费、不得清除 `quota_blocked`，`admin_discovery` 也不结算渠道健康。未保存表单走 3.1 的 `draft_channel` operation，不进入 `PrepareAttempt`。重置额度不能把原本的上游 `auto_disabled` 改成 `healthy`。
- 动态准入第一步必须在 coordinator 提交序列点用 `(channel_id, channel_identity, channel_config_version)` 复核当前行并登记该 identity 的活跃 reservation；只按 id 或只按 id+identity 查行不合格。identity 不同仍按 `channel_removed` 跳过；identity 相同但 config version 不同进入 3.3 的旧配置分支，不能领取当前配置的半开验证。普通渠道删除/替换在同一序列点发现该 identity 仍有 reservation/`AttemptLease` 时返回 409，不等待。业务 `AttemptLease.RecordAttempt(attempt, PricingSnapshot)` 在该序列点内开启短事务插入 processing Attempt，并持久化 snapshot 的 config version 与健康归因；事务提交后才把进程内 reservation 转为由数据库历史引用保护。INSERT 失败则零上游字节并幂等释放 reservation。删除不能插在“Prepare 成功 → processing Attempt COMMIT”之间；COMMIT 后现有历史引用规则继续拒绝删除。渠道过滤命中、调用方取消或未 Record 时释放 reservation 后才允许删除。
- 半开 reservation 另携带一次性的 `halfOpenClaimOwner`，并与取得时的 `channel_identity + channel_config_version + health_version` 一起进入 `AttemptLease` / `HealthSettleInput`。领取必须在同一事务 CAS 当前 identity、配置版本、健康版本、半开状态及空 owner，并同时写 `half_open_claimed=1 + owner`；普通结算、not-sent、过滤取消和兜底释放只能条件清除完全匹配自己的 identity/config/health/owner 四元组。配置版本、健康版本、identity 或 owner 任一不匹配都不得清除当前 claim；旧请求的迟到结算仍可完成 Attempt 与计费，但健康部分 no-op，也不能释放新配置/新健康代次名额。只有启动或在线恢复已经确认无旧进程 lease 的 `CloseInterruptedWork`，以及显式改变配置/健康代次且负责使旧 claim 失效的同一事务，才可不依赖旧调用方无条件清空 owner。禁止在 `settleChannelHealthTx` 各分支散落 `WHERE channel_id=?` 的无 owner 清理。
- 密钥与探针无关：探针路径不读、不写 `proxy_keys.status`。
- 探针 / 编辑器测试 / 列表测试：**不记账、不累加渠道或密钥额度**；它们忽略额度门闩只代表可做上游检查，不代表渠道恢复业务可用。

### 1.3 自动禁用原因必须可区分（不新增 `health_state`）

现状缺口：`health_state` 只有 `auto_disabled`；`health_events.reason` 和 `health_runtime.last_error_class` 已写入 `http_5xx` 这类分类，但渠道目录**不返回**，列表只显示「自动禁用」。`classifyProxyError` 还把所有 ≥500 收成 `http_5xx`，404 收成 `upstream_http`，列表上看不出 502 还是 404。

实现约定：

- **不新增** `quota_disabled` 等健康状态。
- 在 `health_runtime` 增加并回填到目录：
  - `last_error_class`（已有，继续作为**上游健康策略分类**：`http_5xx` / `model_not_found` 等，供 fallback 与健康失败判断；额度不得写入这一列）
  - `last_http_status`（新列，没有则 0；这是**展示用真实 HTTP 状态**）
- **禁止**把 `classifyProxyError` 改成 `http_404` / `http_502` 来驱动 `shouldFallback` / `shouldCountHealthFailure`。策略分类保持现状。
- `HealthSettleInput` 增加 `HTTPStatus int`。结算时写入 `health_runtime.last_http_status`。目录 `disableReason.httpStatus` 读这一列。
- 额度用尽：只看 `channels.quota_blocked`，不改 `health_runtime` 任何列。
- 上游失败导致自动禁用：策略分类仍是 `http_5xx` / `model_not_found` / `upstream_http` 等；展示用 `last_http_status`：
  - 404 → 展示「上游 404」
  - 502 → 展示「上游 502」
  - 429 → 展示「上游 429」
  - 其它 4xx/5xx → 「上游 {status}」
  - 超时 → 「超时」
  - 鉴权失败 → 「上游鉴权失败」
- 健康状态机是否把某次失败算进「渠道故障」**保持现有规则**。
- 目录 API 增加例如 `disableReason: { code, httpStatus, label }`。无凭证的普通渠道优先派生 code=`missing_credential`、label=「缺少凭证」（不改持久 `admin_state` 或健康态）；其余人工禁用时 code=`admin_disabled`，label=「人工禁用」。
- 渠道列表健康列（`quota_blocked` 优先于健康状态）：
  - 普通渠道无凭证 → 「缺少凭证」；即使 safe 导入已人工停用也优先展示此原因。
  - 人工禁用 → 「人工禁用」
  - `quota_blocked=1`（即使健康已被探针拉回 healthy）→ 「自动禁用 · 额度用尽」
  - 自动禁用 + HTTP 502 → 「自动禁用 · 上游 502」
  - 自动禁用 + HTTP 404 → 「自动禁用 · 上游 404」
  - 自动禁用 + 其它 → 「自动禁用 · {label}」
- Tooltip 可带最近健康事件时间。筛选仍可用现有 `auto_disabled`，不必为每个原因加筛选项。

---

## 2. 数据模型

SQLite 全新初始化到 **28**。`storage.PreflightDataDirectory` 是普通可写 `Open`、namespace 初始化和所有 secret 访问之前的唯一数据库预检入口，契约如下：

1. 先取得并持续持有实例锁；原路径只能使用操作系统只读文件操作，禁止对原数据库建立任何 SQLite 连接，包括 `mode=ro`（WAL 模式仍可能创建或修改原路径 WAL/SHM）。数据库不存在或为 0 字节，且没有无法解释的非空 WAL/rollback journal，才可判定 `empty`，随后允许从 version 0 跑完建库链；残留恢复资产不能当作空库忽略。
2. 非空库连同存在的 `-wal`、`-journal` 复制到**原数据目录外**的私有临时目录，保持数据库与 sidecar 的相对命名；目录权限 `0700`、文件 `0600` 或平台等效权限。原 `-shm` 不复制，由 SQLite 在副本重建。复制期间维持实例锁，检查复制前后文件集合、身份、大小和修改时间；读取/复制失败、文件变化或无法取得可恢复的一致副本时直接拒绝，不回退到打开原库。实例锁约束本程序的写入者，不承诺支持外部程序绕过锁并发改库。
3. 只在副本打开 SQLite，允许副本内的 WAL/rollback journal 恢复，以**最新已提交状态**读取 `schema_migrations`。禁止仅复制主文件或用 `immutable=1` 忽略未 checkpoint 的 WAL。缺少/损坏版本表或最高版本不等于 28 时拒绝；仅版本为 28 时，再从同一个副本读取 `instance.secret_binding` 并随 `current` 结果返回，供 3.0 的文件/目录身份比较，不另开原库读取 binding。副本校验失败不得继续启动；成功或失败出口均关闭副本连接并清理临时副本。
4. 预检及拒绝路径的“零写入”指原 DB/WAL/SHM/journal、namespace 和 secret 资产的文件集合与字节内容不变，不创建原路径 WAL/SHM、不 checkpoint 原库。`instance.Acquire` 所需的实例锁文件创建/更新是明确例外；操作系统读取引起的 atime 变化不在此保证内。只有 preflight 通过后，才按 3.0 的 empty/current 分支及 5.4 唯一启动清单继续；current 必须先通过绑定校验，再打开原库。

**工作包 A 必须一次建完 2.1–2.6 的全部对象**（标签、访问组、成员、密钥 tombstone、微美元额度、`quota_blocked`、`disabled_reason`、敏感词列、Attempt 计费列、`response_affinity`、路由事件 CHECK），最后只写一行最高版本 28。B–E 禁止再次写 version=28。工作包只约束接线和验收，不拆 schema。未完成或结构不完整的开发库一律删除后用空数据目录重建，不提供修复或降级版本号重跑方案。

### 2.1 渠道标签（原 `group_name`）

```sql
ALTER TABLE channels RENAME COLUMN group_name TO tag;
```

若现有建库链仍先创建 `group_name`，在同一次全新初始化中 rename 或重建为空表为 `tag`；不处理旧行数据。
`Channel.Group` / JSON `group` 全部改为 `Tag` / `tag`。

```sql
ALTER TABLE requests RENAME COLUMN group_name TO tag;
ALTER TABLE attempts RENAME COLUMN group_name TO tag;
```

全新初始化时请求表为空，不定义旧请求行转换规则。

新请求快照（金额为微美元整数）：

```sql
ALTER TABLE requests ADD COLUMN proxy_key_id TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN proxy_key_name TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN access_group_id TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN access_group_name TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN billed_micros INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN billing_status TEXT NOT NULL DEFAULT ''; -- '' | priced | unpriced | mixed | admin | usage_unknown | amount_overflow
ALTER TABLE requests ADD COLUMN content_filter_scope TEXT NOT NULL DEFAULT ''; -- '' | global | channel
ALTER TABLE requests ADD COLUMN content_filter_matched_word TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN content_filter_channel_id TEXT NOT NULL DEFAULT '';
```

`RouteTarget.GroupName` 改为渠道 **tag** 快照（字段改名 `Tag`）。`ResolveRoute*` 增加 `RouteScope` 参数。

渠道额度与敏感词、健康 HTTP 状态、额度门闩：

```sql
ALTER TABLE channels ADD COLUMN dollar_limit_micros INTEGER NOT NULL DEFAULT 0 CHECK (dollar_limit_micros >= 0);
ALTER TABLE channels ADD COLUMN dollar_used_micros INTEGER NOT NULL DEFAULT 0 CHECK (dollar_used_micros >= 0);
ALTER TABLE channels ADD COLUMN quota_blocked INTEGER NOT NULL DEFAULT 0 CHECK (quota_blocked IN (0, 1));
ALTER TABLE channels ADD COLUMN is_history_placeholder INTEGER NOT NULL DEFAULT 0 CHECK (is_history_placeholder IN (0, 1));
ALTER TABLE channels ADD COLUMN channel_config_version INTEGER NOT NULL DEFAULT 1 CHECK (channel_config_version > 0);
ALTER TABLE channels ADD COLUMN stream_idle_timeout_mode TEXT NOT NULL DEFAULT 'inherit' CHECK (stream_idle_timeout_mode IN ('inherit', 'override'));
ALTER TABLE health_runtime ADD COLUMN last_http_status INTEGER NOT NULL DEFAULT 0;
ALTER TABLE health_runtime ADD COLUMN half_open_claim_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN content_filter_enabled INTEGER NOT NULL DEFAULT 0 CHECK (content_filter_enabled IN (0, 1));
ALTER TABLE channels ADD COLUMN content_filter_words_json TEXT NOT NULL DEFAULT '[]';
```

schema 28 的最终 `CREATE TABLE channels` 还必须直接包含 `channel_identity TEXT NOT NULL UNIQUE CHECK (channel_identity <> '')`、`channel_config_version INTEGER NOT NULL DEFAULT 1 CHECK (channel_config_version > 0)`、上述 `stream_idle_timeout_mode`，以及表级 `CHECK (stream_idle_timeout_mode = 'inherit' OR stream_idle_timeout_ms > 0)`；不要使用 SQLite 不支持的 `ALTER TABLE ... ADD COLUMN ... UNIQUE`。该跨列 CHECK 是最终建表契约，写入/导入的领域校验仍必须在 staging 前给出可解释错误，不能只依赖 SQLite 报错。本阶段只初始化空库，无旧行回填。identity 由服务端以至少 128 bit 密码学随机数生成，碰撞由 UNIQUE 拒绝并重新生成；更新渠道永不改变 identity。普通创建、复制、配置/迁移导入生成新 identity 时 config version 从 1 开始，同实例恢复保留两者。

**新 identity 的健康初值：**普通创建、复制及配置 schema 9（safe / complete_encrypted）/migration v2 导入都在写渠道的同一事务初始化 `health_state=healthy`、`health_version=0`；对应 `health_runtime` 的 `failure_count`、`probe_failure_count`、`probe_success_count`、`cooldown_level`、`half_open_claimed`、`last_http_status`、`last_probe_latency_ms` 均置 0，`cooldown_until`、`half_open_claim_owner`、`last_error_class` 及全部 `last_probe_*` 文本/时间字段置空，即与全新渠道的运行态默认值相同。相同逻辑 id 的 UPSERT 也必须显式重建/覆盖该健康行，不能依赖 `INSERT ... ON CONFLICT DO NOTHING`；identity 已改变，health version 从 0 开始不代表复用旧代次。只初始化当前运行态，不删除或改写历史 Request/Attempt、`health_events`、`probe_runs`，也不从这些历史记录重建当前探针摘要。人工状态、used/limit 及派生 `quota_blocked` 仍按各入口原规则处理，safe 仍人工停用且无凭证。5.4 中未被导入包替换、仅为日志外键保留的占位渠道不套用此规则，健康字段保持原值。同实例恢复保留快照健康状态、版本、计数和摘要，仅按 `CloseInterruptedWork` 清失主 claim/owner；同 identity 的普通编辑仍按下面 projection 规则换代，不执行新身份初始化。

`channel_identity`、`channel_config_version`、`health_version` 是三种不同证据：identity 识别删除重建后的 incarnation；config version 识别实际上游执行与健康归因语义；health version 识别同一配置内健康状态机的运行代次。共享 `ChannelExecutionProjectionV1(channel, globalChannelSettings, requestPolicy, familyModelMappings)` 表示渠道执行配置及其版本依赖：protocol、Base URL、secret ref、自定义 Header、请求/流超时、capabilities、模型/映射、fallback model、失败/cooldown/探针策略都进入 projection；`effectiveReasoningEffort` 在渠道为 passthrough 时取全局值，否则取渠道覆盖值；`effectiveServiceTierPassthrough = global.ServiceTierPassthrough && channel.ServiceTierPassthrough`。reasoning/service-tier 使用合成后的有效值，所属协议族全局模型映射则作为下述保守换代依赖。name、note、tag、访问组、priority、人工启停、额度、动态健康计数和时间字段不进入。bundle 更新必须在同一事务使用当前全局设置、RequestPolicy 和所属协议族映射比较旧/新 projection；projection 改变时递增 `channel_config_version`，并按既有规则递增/失效 `health_version`、清除旧半开 owner，未改变时不得误增。

超时依赖按实际执行区分：`RequestPolicy.ConnectTimeoutMs` 和 `FirstByteTimeoutMs` 经既有规范化后的有效值进入 projection。流空闲超时不再依赖原始数字的 `0` 或规范化副作用猜测继承关系，而由 schema 28 的 `stream_idle_timeout_mode` 显式决定：`inherit` 使用同一 Request snapshot 的全局 `RequestPolicy.StreamIdleTimeoutMs`；`override` 必须携带正数 `channels.stream_idle_timeout_ms` 并使用该值，非正数在写入/导入校验阶段直接拒绝。全新普通渠道默认 `inherit`；为方便用户切换模式，持久数字可保留最近一次合法 override 值，但 `inherit` 时不得参与实际传输。`ChannelExecutionProjectionV1` 同时包含 mode 与按该 mode 算出的 `effectiveStreamIdleTimeoutMs`；切换 mode 即使当下有效毫秒数相同也换代，修改全局 idle 只为 `inherit` 渠道换代，`override` 渠道不得误增。`RouteTarget` / `ChannelOperationSnapshot` 直接携带已经计算的有效值，传输层不得再次按 `<= 0` fallback。`TotalTimeoutMs` 和 `MaxChannelAttempts` 属整次请求预算，不进入渠道健康 projection；完整 RequestPolicy 仍冻结到 Request snapshot，新值只影响之后取得新 snapshot 的请求，总预算耗尽沿用健康中性结算，单独更改预算不清半开 owner、不重置健康。

`PUT /settings` 保存全局 `ChannelSettings` 和/或 `RequestPolicy` 必须是同一个版本化提交：在同一 SQLite 事务中对每个普通渠道比较修改前后的有效 projection，只为实际受影响的渠道递增 `channel_config_version`，同时失效对应 `health_version` 和旧 `half_open_claimed/owner`；一次请求同时修改多项设置时，每个受影响渠道只递增一次。不得重置健康状态、失败/探针计数或额度，也不得让固定 reasoning override 或已关闭渠道 service-tier passthrough 的渠道因无效全局变化误增版本。事务同时保存全局设置，提交后只发布一次包含新全局执行设置与新渠道版本的 `RuntimeSnapshot`。snapshot loader、bundle 保存、全局设置保存、复制和导入必须复用同一个 projection 构造器，禁止各自维护字段清单；以后新增会改变渠道上游执行或健康归因的全局设置，也必须先按上述渠道执行/请求预算边界纳入该构造器。

全局模型映射独立于 `ChannelSettings`，`PUT /models/global-mappings → SaveGlobalModelMappingFamily` 也必须走同一个版本化提交。projection 的 `familyModelMappings` 固定取渠道协议所属族（OpenAI Responses/Chat 共用 openai，Anthropic Messages 使用 anthropic）的规范映射，按客户端模型 key 字节序编码；两族映射同时显式进入 `RuntimeConfigProjection`，不能只存在于 resolver 的独立缓存。映射按既有规则规范化后未变化则不增版；有变化时，在替换该族映射的同一 SQLite 事务内，为该协议族全部普通渠道递增 `channel_config_version`、失效 `health_version` 并清旧 owner，保持健康状态、失败/探针计数和额度不变，提交后只发布一次新 snapshot。这里有意采用**协议族级保守失效**：即使某渠道的本地映射遮蔽了此次全局修改，也换代；不做逐客户端模型的复杂影响推断，另一协议族及日志占位渠道不换代。Request 始终使用原 snapshot 的全局映射，配置失配后复用 3.3 的 skip/stale neutral 规则，不能用旧映射领取或恢复新映射代次的半开状态。bundle、全局设置和全局映射提交复用相同 projection 构造器，不分别维护版本依赖。

schema 28 的最终 `CREATE TABLE health_runtime` 必须让 `half_open_claimed` 与 `half_open_claim_owner` 一致：未占用时固定为 `0 + ''`，占用时固定为 `1 + 非空 owner`，用表级 CHECK 拒绝其它组合。owner 是每次领取时生成的至少 128 bit 随机值，不进入管理 wire、配置/迁移包、恢复点 manifest 或 `RuntimeConfigProjection`；恢复点 SQLite 可携带捕获时的 owner，但启动/在线恢复只在确认无旧执行者后由 `CloseInterruptedWork` 清空。

全局敏感词**权威来源**是 `app_settings` 键 `content_filter`，默认：

```json
{ "enabled": false, "words": [] }
```

不要塞进 `config.Settings`（那是监听地址 / 请求策略 / 渠道思考等级）。热路径用独立 `ContentFilterSnapshot`：启动、`GET/PUT /settings`、导入导出都读写同一 `app_settings` 行，PUT 成功后发布到内存。渠道词随 bundle / `RouteTarget` 读取。

`0` 限额 = 不限制，新建渠道 `used = 0`。`is_history_placeholder` 只标识新方案运行期间整包导入为保留日志外键而留下的日志占位渠道；所有普通渠道均为 0。占位渠道必须人工禁用、无凭证、无访问组且不参与路由。

### 2.2 访问分组

```sql
CREATE TABLE access_groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  note TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (is_default = 0 OR (id = 'default' AND name = 'default' AND enabled = 1))
);

CREATE TABLE channel_group_members (
  group_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  PRIMARY KEY (group_id, channel_id),
  FOREIGN KEY (group_id) REFERENCES access_groups(id) ON DELETE CASCADE,
  FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
);

CREATE INDEX idx_channel_group_members_channel ON channel_group_members(channel_id);

-- 至多一个默认组
CREATE UNIQUE INDEX idx_access_groups_one_default ON access_groups(is_default) WHERE is_default = 1;
```

全新初始化结束时插入默认组：

- `id = 'default'`
- `name = 'default'`
- `is_default = 1`
- `enabled = 1`

默认组身份固定：`id='default'` **且** `name='default'` **且** `is_default=1`。导入/恢复若出现第二行 `is_default=1`、或默认组 id/name 对不上 → 拒绝。全新初始化没有既有渠道；之后每个普通渠道必须在自身创建事务内写入 default 成员。

### 2.3 列表密钥

```sql
CREATE TABLE proxy_keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL,
  secret_ref TEXT NOT NULL,
  group_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),
  disabled_reason TEXT NOT NULL DEFAULT '' CHECK (
    (status = 'enabled' AND disabled_reason = '') OR
    (status = 'disabled' AND disabled_reason IN ('manual', 'quota_exhausted'))
  ),
  dollar_limit_micros INTEGER NOT NULL DEFAULT 0 CHECK (dollar_limit_micros >= 0),
  dollar_used_micros INTEGER NOT NULL DEFAULT 0 CHECK (dollar_used_micros >= 0),
  last_used_at TEXT NOT NULL DEFAULT '',
  tombstoned_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK (
    (secret_ref = '' AND token_hash = ('pending:' || id) AND status = 'disabled') OR
    (secret_ref <> '' AND token_hash NOT GLOB 'pending:*')
  ),
  FOREIGN KEY (group_id) REFERENCES access_groups(id) ON DELETE SET NULL
);
```

- `id` 本身就是列表密钥不可变 identity：普通 `POST` 只能由服务端生成至少 128 bit 的密码学随机 ID，碰撞由主键拒绝并重新生成；调用方不得提供 ID，删除后永不复用。complete/migration 可携带源逻辑 ID，但整包替换必须先清空 affinity；同实例恢复保留快照 ID 与 affinity。不得用“当前最小空闲编号”、行数或时间戳生成可复用 ID。
- `dollar_limit_micros = 0`：不限额。
- `disabled_reason`：enabled 必须为空；disabled 必须是 `manual` 或 `quota_exhausted`。导入校验与 CHECK 相同。
- `secret_ref=''` 只允许严格 `pending:<本行 id>` 且 disabled 的 safe 占位行；已物化密钥必须有非空 ref 与非 pending hash。上述 CHECK 是存储兜底，不替代导入/恢复时验证明文、ref 可读、额度状态与占位 reason 的领域校验。额度操作不得绕开 CHECK 强行把占位行启用。
- `tombstoned_at != ''` 表示逻辑删除。鉴权、普通 GET/列表、名称唯一检查、配置/迁移导出和新请求主体查询都必须显式排除 tombstone；仅结算、恢复清理和内部诊断可以按 id 读取。不得把 tombstone 伪装成 `status=disabled`，也不得允许恢复、轮换或重新启用。
- 删组：`ON DELETE SET NULL`，status / reason 不变。
- **三类令牌全局两两唯一**：管理令牌、通用代理令牌、任一列表密钥的 hash **两两不得相同**。`NewAuthTokens`、Set、Rotate、列表密钥创建/轮换、complete 配置导入、迁移导入都必须做这组校验，缺一口径算没做完。当前 `NewAuthTokens` 允许 admin=proxy，必须改掉。冲突 → 400，不猜测谁覆盖谁。鉴权仍先查列表密钥，但冲突在写入时挡住。
- 物化密钥的 `token_prefix` 统一由实际 token 前 15 字符派生（`sk-oneai-` + 6 位随机串）。complete / migration 的包内 `proxyKeys[].prefix` 必须等于重新派生值，否则拒绝，不能覆盖或信任不一致值；safe 没有 token，只接受正则 `^sk-oneai-[A-Za-z0-9]{6}$` 的展示前缀，轮换后改为新 token 的派生值。创建、轮换、导出和导入不得各用一套长度。

密钥环：`secret.NewRef()`。创建/轮换统一经过 3.0 的 `SecretRefLifecycle`：先持久登记 provisional ref 并取得 operation lease，再写密钥环，最后在提交业务行的同一 SQLite 事务中移除 provisional 项；失败释放 operation lease 并幂等删除，删除失败保留账本供启动续作。不要把列表密钥明文写入 `app_settings`。

删除密钥使用 **tombstone + 延迟物理清理**，不得等待流式请求结束，也不得进入全站 draining：

1. `DELETE` 在短事务内写 `tombstoned_at=now`；提交后该密钥立即从鉴权、管理列表和导出中消失，返回逻辑删除成功。
2. 已取得 `RequestLease` 的请求继续使用 lease 内不可变主体快照；`requests.proxy_key_id` 和进行中的 Attempt 仍引用原行，因此结算可以原子更新该主体的 used 与 Attempt 账本。
   - `SettleAttemptHealth` 按主体 id 更新 used 时**不得过滤 tombstone**；tombstone 只阻止新鉴权和管理操作，不改变已经开始请求的计费责任。
3. 只有当 `RuntimeCoordinator` 不再存在该 key 的活跃 `RequestLease`，且数据库中没有该 key 所属 `requests.final_status='processing'` 或关联 `attempts.status='processing'` 时，后台清理才在同一事务先 `DELETE FROM response_affinity WHERE principal_type='proxy_key' AND principal_id=?`，再把 `secret_ref` 以 `proxy_key_deleted` 记入 retired cleanup 并物理删除密钥行；任一步失败整笔回滚。COMMIT 后 `Store.Delete`，失败则保留账本重试。即使 Request 尚未创建 Attempt，processing Request 也必须单独阻止清理；请求表**没有** `status` 列。这样迟到的旧请求无机会在清理后重建 affinity，而后来创建的新密钥也不会继承旧主体记录。
4. 启动时先按 2.4.1 收口未完成 Request/Attempt；若存在 committed restore，还必须先完成恢复集合验证、初始 snapshot 发布和 `FinalizePendingRestore`，持久进入 `cleanup_pending` 后才能运行 tombstone 清理。无 pending restore 时 finalization 为 no-op，再按同一顺序清理。清理必须幂等；物理 secret 删除失败只有在 retired 账本已持久接管时才可交给后台重试，数据库/引用校验失败仍阻止启动。普通删除接口不等待该后台阶段，也不扫描或猜测系统密钥环。

### 2.4 Attempt 计费快照（幂等）

计费挂在 Attempt 上，请求行只存合计。

```sql
ALTER TABLE attempts ADD COLUMN billed_micros INTEGER NOT NULL DEFAULT 0;
ALTER TABLE attempts ADD COLUMN billing_status TEXT NOT NULL DEFAULT ''; -- '' | priced | unpriced | admin | usage_unknown | amount_overflow
ALTER TABLE attempts ADD COLUMN billing_applied INTEGER NOT NULL DEFAULT 0 CHECK (billing_applied IN (0, 1));
ALTER TABLE attempts ADD COLUMN price_resolution TEXT NOT NULL DEFAULT '' CHECK (price_resolution IN ('', 'priced', 'unpriced'));
ALTER TABLE attempts ADD COLUMN price_stable_key TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN price_input_decimal TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN price_output_decimal TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN price_cache_read_decimal TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN price_cache_write_decimal TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN price_currency TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN price_billing_unit TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN usage_validity TEXT NOT NULL DEFAULT '' CHECK (usage_validity IN ('', 'missing', 'valid', 'invalid'));
ALTER TABLE attempts ADD COLUMN channel_config_version INTEGER NOT NULL DEFAULT 1 CHECK (channel_config_version > 0);
ALTER TABLE attempts ADD COLUMN health_attribution TEXT NOT NULL DEFAULT 'pending' CHECK (health_attribution IN ('pending', 'current_config', 'stale_config_neutral', 'not_applied'));
```

token 数值快照**复用**现有 `input_tokens` / `output_tokens` / `cache_read_input_tokens` / `cache_write_input_tokens` / `reasoning_tokens` / `total_tokens`，不另加一套数值列；`usage_validity` 单独保留 missing / valid / invalid，避免非法字段在写入可空整数列后丢失。计费使用结算时的数值与有效性快照，invalid 绝不能按缺省 0 计费。`channel_config_version` 记录实际用于本次上游调用的 snapshot 配置代次；`health_attribution` 在 processing 时为 `pending`，终态结算按配置版本匹配写 `current_config` 或 `stale_config_neutral`，崩溃收口等明确不做健康结算的路径写 `not_applied`。当前 schema 不新增 Anthropic 5m/1h 两套可计价列，因此正数 `cache_creation_input_tokens` 按 0.4.1 保守落为 usage valid 但 unpriced；详情接口从协议、聚合 cache-write token 与最终状态稳定派生原因，不把它误报为 usage invalid。

**价格线性化由两个时点共同锁定：Request 取得价格目录快照，Attempt INSERT 持久化本候选的匹配结果。** `RuntimeSnapshot` 必须携带与渠道目录同代的不可变 `PricingSnapshot`；同一 Request 的初始 Attempt 和全部 fallback Attempt 都只能从这个固定目录快照解析各自的协议、upstream/logical/client model 单价，不能在后续 Attempt INSERT 时重新读取 live 目录。`PrepareAttempt` 返回目标/凭证后、任何上游字节发出前，只能调用上段 `AttemptLease.RecordAttempt` 在同一 INSERT 持久化 `channel_id`、`channel_config_version`、初始 `health_attribution`、`price_resolution` 和全部 `price_*` 字段，成功提交后才允许发上游。不得绕过 lease 直接调用 storage `RecordAttempt`，否则会重新打开渠道删除窗口。无匹配、歧义、非 USD、未知单位或全空价格都冻结为 `price_resolution=unpriced`；可计价则冻结为 `priced`。目录改价只影响改价提交并发布后取得新 `RuntimeSnapshot` 的 Request；已经取得旧 snapshot 的 Request 即使改价后才进入第二个 fallback Attempt，也继续用旧目录快照。终态结算和启动崩溃收口**只能读取 Attempt 行已保存的价格快照**，禁止重新查询 live 模型目录或按结算时价格补写。

同一事务（扩展现有 `SettleAttemptHealth`，不要另开事务扣费）：

1. `updateAttempt` 必须返回 `transitioned bool`：`UPDATE ... WHERE status='processing'` 影响 1 行 → `true`；已是同一终态 → `false, nil`；已是不同终态 → 错误。
2. `transitioned == false`：**整次事务幂等 no-op**。不改 `billing_*`、used、`quota_blocked`、失败计数、健康版本、健康事件、路由事件、请求合计。直接提交空事务返回成功。当前实现（`internal/storage/requests.go`）同终态返回 nil 后仍会 `settleChannelHealthTx`，必须改掉。
3. `transitioned == true` 才继续：写入终态与 `billing_*`；**任何终态**都把 `billing_applied` 置 1（含 $0、无价、失败、无 usage）。仅当本行从 0→1 **且** `billed_micros > 0` 时累加 used / 更新停用与 `quota_blocked`。
4. 健康结算先读取 Attempt/lease 固定的 `channel_identity + channel_config_version + health_version + owner`，并与当前行做条件复核。config version 已在 Prepare 前失配，或 Prepare 后才失配时，写 `health_attribution=stale_config_neutral` 并跳过全部健康/owner 更新；版本一致时写 `current_config` 并按既有状态机结算。此分支不影响前一步已经确定的 usage、金额和双边额度责任，任何迟到旧配置都不得清当前配置 owner。
5. 回写请求合计时禁止直接依赖 SQLite `SUM(INTEGER)`。按稳定顺序读取本请求各 Attempt 的 `billed_micros`，在 Go 中逐项检查后相加；未溢出时写合计，`billing_status`：全 `usage_unknown`→`usage_unknown`；全 `amount_overflow`→`amount_overflow`；全 admin→`admin`；全 unpriced→`unpriced`；全 priced→`priced`；多种状态（含部分 unknown / overflow）→`mixed`。通用令牌整单无 usage 时请求详情也是 `usage_unknown`，不要显示 `admin` 把未计价盖住。
6. 若各 Attempt 金额都合法但**请求级合计**超出 `int64`，Attempt 终态和已经完成的渠道/密钥额度结算保持原值；请求行写 `billing_status=amount_overflow`、`billed_micros=0`，记录 `billing.request_amount_overflow`。不得让 SQLite SUM 报错回滚当前 Attempt，不得回绕或伪装为 mixed，也不得为显示合计再次增减任一主体额度。

计费分量必须先走 0.4.1 归一化，再用目录单价。账本 token 列仍写上游原始值。

模型目录 wire、`pricing_json` 和 Attempt 快照中的四项价格统一保存为**规范非负十进制字符串**（无指数、无前导 `+`、去掉无意义尾零；缺价为空字符串；单项最长 64 字符）。在线来源适配器必须用 `json.Number`、`json.RawMessage` 或等价方式先保留价格原文，再规范化；禁止先反序列化为 `float64`。`ModelPricing` 不再以 `float64` 作为计费权威值。入账用标准库 `math/big.Rat` 或等价的精确十进制定点 helper 计算 `billable_tokens × price_per_million`，然后对该分量做一次 half-up 得到整数微美元。禁止先舍入单价，也禁止把二进制浮点格式化后再参与计费。转为 `int64`、四项求和以及 `used + billed` 前都必须显式做范围检查，禁止 Go/SQLite 整数回绕。

若任一分量、四项合计或任一主体的 `used + billed` 超出 `int64`，该 Attempt 仍原子写入终态、`billing_applied=1`、`billing_status=amount_overflow`、`billed_micros=0`，不修改任何 used / quota 状态，并写不含秘密的高优先级日志 `billing.amount_overflow`。不得截断、饱和、回绕或只更新一侧主体；这是显式失败状态，不得伪装成 `usage_unknown` / `unpriced`。管理详情必须可见该状态，实施时再评估是否需要扩大金额存储类型，不能静默收费。

计价状态（**先判定 usage / 价格是否可计，再标注主体**。通用令牌不得覆盖 unknown / unpriced）：

| 情况 | billing_status | billed_micros |
| --- | --- | --- |
| 上游没有 usage / OpenAI 缓存明细大于总量 / 分量为负 / 协议无法判定 | `usage_unknown` | 0，不扣额度。通用令牌也是这条，**不要**写成 `admin` |
| 金额分量、总额或 `used + billed` 超出 `int64` | `amount_overflow` | 0，不扣额度；整次额度更新 no-op，记录高优先级诊断 |
| Anthropic `cache_creation_input_tokens > 0`，单一 cache-write 价格无法表达 5m/1h TTL | `unpriced` | usage 仍 valid；整次 0、不扣额度，详情原因 `anthropic_cache_write_ttl_unexpressible` |
| 目录无价 / 同一优先档命中多条目录记录 / 非 USD / 未知单位 | `unpriced` | 0。通用令牌也是这条；歧义价格禁止按排序任取 |
| 有价且上游给出 usage（含全 0），列表密钥 | `priced` | 按公式，可为 0；同时加渠道与密钥 used |
| 有价且上游给出 usage（含全 0），通用令牌 | `admin` | 仍按公式算，**只加渠道** used，不计入密钥 |
| 四项价格只到一部分 | 有的项按价算，缺的项当 0；整体仍 `priced`（通用令牌则 `admin`） | 已有项之和 |

### 2.4.1 崩溃后的 processing 账本收口

`storage.CloseInterruptedWork` 是 schema 28 新增的单事务收口原语，不存在可复用的旧语义。启动时在 HTTP 服务、后台探针和任何清理器之前执行；在线同实例恢复时在新库及固定 secret 已验证、仍处于 draining 且尚未重新放行代理/探针之前，对**恢复后的数据库**执行同一原语。每次调用先固定一个 UTC `closeTime`（启动时即启动时间，在线恢复时即本次收口时间）：

1. 在任何更新前先固定两个集合：调用开始时仍为 `requests.final_status='processing'` 的 `interruptedRequestIds`，以及本次实际命中的全部 `attempts.status='processing'` 所属 `interruptedAttemptParentIds`；随后以同一个 `closeTime` 把这些 processing Attempt 改成既有合法终态 `failed`，`error_class='process_interrupted'`、固定脱敏错误说明、`completed_at=closeTime`、`stream_ended=0`、`usage_validity='missing'`、`billing_status='usage_unknown'`、`billed_micros=0`、`billing_applied=1`、`health_attribution='not_applied'`。保留 Attempt 创建时冻结的 `price_*` 与 `channel_config_version` 仅作审计，不据此收费或结算健康；不得新增与现有 `validAttemptFinalStatus` 冲突的 Attempt `error` 状态。
2. 这些中断 Attempt 不累加渠道/密钥 used，不改变 `quota_blocked`，不调用健康结算、不增加失败计数、不写健康事件，也不创建 fallback；重启无法证明上游是否完成，必须采用零收费、健康中性规则。
3. 对 `interruptedRequestIds ∪ interruptedAttemptParentIds` 中的每个父 Request，按 sequence 稳定读取其全部终态 Attempt，并用 2.4 的 checked sum/状态聚合规则重算 `attempt_count`、`billed_micros`、`billing_status`；已在崩溃前完成结算的 Attempt 金额保留，中断 Attempt 贡献 `usage_unknown + 0`，混合时请求计费状态为 `mixed`。只有属于 `interruptedRequestIds` 的父行改成 `final_status='error'`、`error_class='process_interrupted'`、固定脱敏错误说明并写 `completed_at=closeTime`；仅因子 Attempt 被收口而进入并集、但父 Request 调用开始时已经终态的行，必须保留原 `final_status`、`completed_at`、响应结果、HTTP 状态及既有错误字段，只更新上述 Attempt 派生聚合。两个集合有交集时只处理一次。全过程不得再次增减任何主体额度。
4. 在**同一事务**对 `health_runtime` 中 `half_open_claimed=1` 的行清成 `half_open_claimed=0, half_open_claim_owner=''`：恢复点可能捕获已无执行者的半开占用，不能让它阻止下一次半开验证。只清 claim 与 owner，不改 `health_state`、`health_version`、失败/探针计数、冷却时间、错误原因或额度门闩；不得调用 `ResetChannelHealth`。这是唯一允许不提供 owner 的批量释放路径，无在途旧 lease 是调用前提：启动时旧进程不存在，在线恢复时必须先完成 drain。
5. 在同一事务收集**实际从 processing 变为终态**的 Request id，以及实际收口的 Attempt 所属 Request id；逐个读取关联 `requests.started_at`，按 `hourlyBucketKey` 的 UTC 小时去重，每个小时仅调用一次既有 `markHourlyMetricDirtyTx`。当前统计聚合按请求开始小时分桶，而 `AggregateMissingMetrics` 对已有聚合只重算标脏小时；不能只更新账本或按 `completed_at` 标脏。若仅清半开占用、没有 Request/Attempt 终态变化，不写 dirty；重复调用也不得增加 dirty revision。
6. 任一更新、checked aggregation 或小时标脏失败则整笔事务回滚；启动阻止监听，在线恢复保持 fail-closed 并按 restore journal 续作，不能开放旧内存快照。函数可重复执行，第二次没有 processing 行及半开占用时为 no-op。完成后数据库不得残留 processing Request/Attempt 或失去执行者的半开占用，受影响小时必须已标脏，之后才允许运行 secret/tombstone cleanup 或在线开放代理。

### 2.5 Responses 亲和隔离

重建 `response_affinity`：

```sql
CREATE TABLE response_affinity (
  principal_type TEXT NOT NULL CHECK (principal_type IN ('proxy_key', 'admin_proxy')),
  principal_id TEXT NOT NULL,
  response_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  PRIMARY KEY (principal_type, principal_id, response_id),
  FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
);
CREATE INDEX idx_response_affinity_expires_at ON response_affinity(expires_at);
```

必须保留现有 `ON DELETE CASCADE`（当前 DDL 在 `internal/storage/sqlite.go` schema 4）。删除渠道后该渠道亲和行自动消失。过期清理继续按 `expires_at`，走新索引。

- 列表密钥：`('proxy_key', key.id, response_id)`
- 通用令牌：`('admin_proxy', 'admin-proxy', response_id)`
- 探针/测试不写亲和
- schema 28 全新初始化：按主体复合主键创建表；初始化时没有需要继承的无主体行
- `GetResponseAffinity` 必须带 RouteScope；猜到别人的 `previous_response_id` 也读不到
- `proxy_keys.id` 按 2.3 是不可复用的主体 identity；密钥最终物理删除必须在同一事务清除该 principal 的全部 affinity。RouteScope 是访问范围复核，不得替代 identity 隔离或删除清理。

---

### 2.6 路由事件

SQLite 不能 `ALTER` CHECK。schema 28 **必须像 schema 27 那样**用临时表重建 `request_route_events`：

现有 event_type：`candidate_skipped` / `channel_switched` / `health_changed` / `routing_exhausted` / `custom_headers_ignored`

重建后 CHECK 增加：

- `group_mismatch`：仅密钥无组 / 组停用 / 组成员为空。**不要**把「组有效但协议/模型/能力不匹配」算进来
- `key_disabled`：密钥停用或额度用尽（开始时拒绝）
- `request_rejected`：请求在发上游前被拒绝（敏感词等）。reason=`content_filtered`；层和命中词写在 requests 结构化列，不塞进自由文本

额度跳过仍用 `candidate_skipped` + reason=`quota_exhausted`。

### 2.7 未决 provisioning 提交 journal

schema 28 新增专用表 `provisioning_batches`。它只保存“业务事务已经提交、运行快照尚未确认发布完成”的本机运行证据，不与 `SecretRefLifecycle` 的 provisional/retired ref 账本、`auth.token_journal` 或 restore sidecar 混用：

```sql
CREATE TABLE provisioning_batches (
  operation_id TEXT PRIMARY KEY CHECK (trim(operation_id) <> ''),
  version INTEGER NOT NULL CHECK (version = 1),
  kind TEXT NOT NULL CHECK (kind IN (
    'channel_secret_create',
    'channel_secret_replace',
    'channel_copy',
    'proxy_key_create',
    'proxy_key_rotate',
    'config_complete_import',
    'migration_import'
  )),
  target_digest TEXT NOT NULL CHECK (
    length(target_digest) = 64 AND
    target_digest NOT GLOB '*[^0-9a-f]*'
  ),
  new_refs_json TEXT NOT NULL CHECK (length(new_refs_json) >= 2),
  state TEXT NOT NULL CHECK (state = 'database_committed'),
  created_at TEXT NOT NULL,
  UNIQUE (state)
);
```

- `operation_id` 必须是本次内部 UUID；`new_refs_json` 必须由模块严格解析为按字节升序、无重复的逻辑 ref JSON 数组，且必须与本次 batch 消费的 provisional ref 集合完全相等。只有 `config_complete_import|migration_import` 在包内没有渠道凭证/列表密钥时允许规范空数组 `[]`，其它实际创建 batch 的 kind 必须非空。`channel_copy` 仅表示**有凭证源**创建新 ref 的分支；`disabled + 空 ref` 源复制只走 `ChannelBundleCommitter` 的无 batch 类型化短提交，既不插入 committed 行，也不以空数组冒充 `channel_copy`。未知列、未知 kind/state、非规范 JSON、重复 ref 或跨 batch ref 一律 fail-closed。`UNIQUE(state)` 明确同一实例最多只有一个未完成的 committed batch；该行存在时 coordinator 不得接受下一次 short/draining commit。
- `target_digest = sha256(canonicalRuntimeConfigProjectionV1)`。projection 从**事务内提交后视图**读取并用版本化规范 JSON 编码，只覆盖不会被代理热路径自行改变、且决定认证/路由/配置 snapshot 的字段：固定令牌 hash、运行设置、内容过滤设置、访问组/成员、两族全局模型映射、未 tombstone 列表密钥的 hash/ref/group/manual-disabled 策略与额度 limit、普通渠道的 `channel_identity + channel_config_version`、静态配置/admin state/secret ref/额度 limit 及其模型/映射/探针策略、价格目录。数组按稳定主键排序，整数用规范十进制，逻辑 ref 原样编码。它明确排除 `dollar_used_micros`、`quota_blocked`、quota 派生 status/reason、渠道健康/并发/探针运行计数、created/updated 时间，以及请求/Attempt/日志/聚合统计、WAL/SHM、authority generation、`SecretRefLifecycle` 和全部本机运行 journal。在 committed 行存在期间 coordinator 拒绝其它配置提交；在途 Attempt 仍可结算这些被排除的动态字段而不破坏摘要。运行快照 loader 与 journal 必须复用同一个 config projection 构造器，并让 snapshot 暴露同源 `configDigest`；渠道部分还必须复用同一个 `ChannelExecutionProjectionV1` 计算 config version 是否递增，禁止分别实现两套摘要或字段清单。
- 业务事务在移除本 batch provisional、登记旧 ref retired 和写完业务行后，以同一个 SQLite COMMIT 插入唯一的 `state=database_committed` 行；这是持久提交判定。`Complete()` 仅在当前数据库 config projection、待发布/已发布 snapshot 的 `configDigest` 和行内 `target_digest` 三者一致时，按 operationId 条件删除该行；删除失败或受影响行数不是 1 时保持 fail-closed，不释放 commit 串行权，也不接收下一次提交。完成态不另留历史行，操作审计由现有操作日志承担。
- COMMIT 返回不确定时必须用新连接按 operationId 重读：存在且全部字段与本次预期一致即按已提交只前滚；不存在且 `newRefs` 均未被当前三张引用表引用才允许按未提交 Abort；行存在但字段不匹配、行缺失但已有新 ref 被引用，或 projection 无法重算都阻止服务并保留所有 secret，禁止猜测清理。

---

## 3. 鉴权与选路行为

### 3.0 实例秘密命名空间与权威存储

业务调用方看到的 `secret.Store` 接口仍只有逻辑 `ref` 的 Put / Get / Delete，调用方、SQLite、配置/迁移包和同实例恢复点都只保存逻辑 ref。实例隔离和主/备存储选择全部封装在同一个深模块里，禁止 handler、storage helper 或恢复点代码自行拼物理 ref。只有 `RuntimeCoordinator` 的管理 secret-read 操作、`RecoveryPointStore` 和 `PrepareAttempt` 可在各自深模块内部通过 `SecretReadLease` 能力 pin/读取精确逻辑 ref + generation；该能力不向 handler 暴露，代理 handler 只能取得已经绑定凭证的 `AttemptLease`。

1. 每个全新数据目录在首次建库前生成随机 UUID `instanceSecretNamespace`。先解析数据目录的规范化真实绝对路径（消除 `.` / `..` 和符号链接），计算非秘密的 `dataDirectoryBinding = sha256(canonicalDataDirectory)`，再把 `{version: 1, namespace, dataDirectoryBinding}` 原子写入 `{dataDirectory}/instance-secret-namespace`（`0600`，文件及父目录 fsync）；schema 28 初始化事务同时把完全相同的记录写入本机运行键 `app_settings.instance.secret_binding`。两份记录与当前目录身份三者必须一致。重命名、移动或复制到**不同规范化真实路径**后禁止直接启动，必须使用迁移包进入新的空数据目录。该机制是实例 namespace 隔离与路径绑定，不是数据目录之外的机器绑定；把完整目录复制到另一台机器的相同绝对路径可能无法被检测，属于不受支持的操作，但 UI、验收和文档不得承诺一定识别或拒绝所有复制。
2. 只允许经过第 2 节 preflight 确认 `empty` 后初始化绑定。该分支**无论目录里是否预放了格式合法的 namespace 文件，都必须生成全新 UUID 并原子覆盖该文件**，不得复用外来 namespace；在 schema 事务提交前不得进行任何 `secret.Store` 读写。已有 schema 28 数据库则使用同次隔离副本 preflight 返回的 `instance.secret_binding`，并在构造 Store 前比较 SQLite 记录、文件记录和当前 `dataDirectoryBinding`；任一缺失、格式错误或不相等都阻止启动，不得生成新 namespace 猜测接管已有秘密。这样同一环境中规范化真实路径不同的数据目录不能通过复制文件或整库共享物理 ref；同一真实目录经不同符号链接仍由实例锁视为同一实例。跨机器同绝对路径的能力边界仍按上一条处理。
3. 命名空间适配器把逻辑 ref + generation 映射成**不可变物理账户**，例如 `instance:<namespace>:<sha256(logicalRef)>:<generation>`；同一个逻辑 ref 的 Put 永远写新 generation，禁止覆盖 active 物理账户。系统 service 可继续是 `oneai-proxy`。本地加密 fallback 已在数据目录内隔离，但仍使用同一映射。backup bundle、SQLite `secret_ref`、业务 journal 和 manifest 始终只保存逻辑 ref，不泄露或迁移物理账户。
4. 当前“主存储失败就写本地、读取仍永远优先主存储”的 `FallbackStore` 语义必须替换。每个逻辑 ref 在 `{dataDirectory}/secret-store/authority/` 保存一个原子、持久的非秘密 authority journal；文件名取逻辑 ref 的摘要，内容为严格状态机 `{version:2,phase,active?,candidate?,retired[]}`，每个 slot 只含 `{backend,generation}`，retired 可另带非秘密 reason=`superseded|abandoned_candidate`。`phase` 只允许 `stable|put_prepared|put_committed|delete_pending`；generation 是高熵随机值。记录 `0600`，每次同目录临时文件写入、fsync、原子替换并 fsync 父目录。**authority 不得保存 `sha256(value)`、明文、密文或任何可用于离线验证秘密猜测的值。**同一 logical ref 的状态机由 store 内部锁串行推进。
5. 每个物理账户保存统一内部封装 `{version:1,generation,value,sha256(value)}`：系统密钥环保存整个封装，本地 fallback 在现有加密层内保存整个封装，摘要不得作为旁路明文元数据落盘。Put 顺序固定为：生成新 generation → 原子写 authority `put_prepared`（保留当前 active、全部既有 retired，登记 candidate）→ 写 candidate 的新物理账户并精确读回校验 → 原子写 `put_committed`，把 candidate 提升为 active、旧 active 追加到 retired → 返回成功。retired 清理异步进行；没有对应 `SecretReadLease` 的 generation 才能删除，清理成功逐项从 authority 移除。`stable` 允许仍带尚被 pin 或待重试的 retired，不能把“收敛 stable”等同于 retired 必须为空。
6. 系统密钥环 candidate 的 Put 返回错误或读回失败时，结果一律视为**不确定**，不能证明物理写没有发生。切到 local 前必须在同一次 authority 原子替换中把原 `{keyring,generation}` candidate 追加为 `retired(reason=abandoned_candidate)`，并登记全新的 `{local,newGeneration}` candidate；该 authority 发布失败则保留原 keyring candidate 并终止 Put，禁止留下未登记账户。随后才写/读回 local。这样 keyring 实际写成功但返回错误时仍有精确清理证据，不需要也不允许前缀扫描。切回 keyring 同样走新 generation 状态机。
7. 同一 logical ref 的 Put 由内部锁串行，但**不得等待 retired lease 释放**。当上一轮仍是 `put_committed` 或 `stable + retired[]` 时，新 Put 以当前 active 为基线，原子进入新的 `put_prepared`，完整继承全部 retired，再按上述顺序产生 G2/G3；后续提交只追加 retired，禁止覆盖历史清理集合。任何时刻最多一个 candidate；每个 logical ref 最多 64 个 retired slot，开始新 Put 前必须为“当前 active 被替换 + keyring 结果不确定”预留最多 2 个 slot，空间不足立即返回 409、code=`secret_generation_backlog`，不等待旧 pin、不改 authority。清理器在 lease 释放后逐 generation 幂等删除；成功清理后新 Put 可重试。
8. 启动时 `SecretStore.RecoverPendingAuthority` 在任何业务 secret 读取前扫描**明确 authority 文件**而非密钥环前缀：`put_prepared` 一律保留旧 active，把 candidate 追加/保留为 `abandoned_candidate` 后幂等删除并回到 `stable + retired[]`；`put_committed` 以新 active 为权威并转为可继续 Put 的 `stable + retired[]`，同时续清 retired；`delete_pending` 继续删除明确列出的 active/candidate/retired，全部成功后删除 authority。authority 把 candidate 提升为 active 的原子发布是 Put 线性化点；发布前崩溃表现为旧值，发布后崩溃表现为新值。新 ref 没有旧 active 时，prepared 恢复为不存在，但仍须清理明确登记的 candidate/retired。
9. Get 在 stable/put_prepared 读取旧 active，在 put_committed 读取新 active；要求物理封装 generation 与 slot 完全相等，并在封装内部校验 `sha256(value)`。`delete_pending`、后端不可用、封装缺失或校验不符均 fail-closed，禁止尝试未被 active 指向的 candidate/retired。Delete 先原子写 `delete_pending` 并列全已知 generation，再逐个幂等删除物理账户，最后删除 authority；中途失败返回错误并由启动续作。`SecretReadLease` pin 的是 `{logicalRef,generation}`，Put/Delete 的 retired 清理必须等待精确 generation lease 释放。
10. journal 中的“Store.Put 成功”“重新读取 live 验证”都以上述线性化语义为准。namespace 文件、`instance.secret_binding`、authority journal 都是目标实例运行状态，不得通过配置/迁移包或恢复点覆盖。同实例恢复把 bundle 中每个逻辑 ref 通过 Put 写入当前实例的新 generation；旧 generation 只能经 authority retired 清理。`Store.Delete` 对 logical ref/明确 generation 已不存在必须幂等成功。

**本地 fallback 物理持久化契约（A2）**

上述 authority 发布依赖物理后端已经持久保存数据；即时读回只验证内容，不替代文件和目录同步。`encryptedFileStore` 必须补齐以下顺序，不能直接沿用当前 `writePrivateFile` 的目标文件 truncate 写法：

- `master.key` 的读取与初始化分开，`Get` 只读取和校验 32 字节正式主密钥，不创建密钥。只有首次 local Put 在 store 内部初始化串行点确认主密钥不存在、没有既有本地密文文件，也没有指向 local 的 active/retired authority slot 时才可生成；当前尚未写物理文件的 prepared candidate 不算既有依赖。已有本地账户依赖时，缺失或损坏的主密钥必须 fail-closed，不自动替换。
- 初始化先创建权限受限目录并同步其父目录，再在同目录写私有临时主密钥、fsync 文件、原子发布为 `master.key`、同步父目录，全部完成才可加密第一个 candidate。已有合法正式主密钥保持不变；中断后只清理模块专用命名且归属明确的初始化临时文件，不把临时文件猜作正式主密钥。若已发布正式文件而同步结果不确定，重试必须读取同一文件并完成同步，不能重新生成覆盖。
- local candidate 使用 authority 已登记的精确 generation，临时路径由该物理账户确定。写私有临时密文并 fsync，原子发布到该 generation 的不可变正式路径，再同步父目录；随后精确读回校验，全部成功才允许 authority 发布 `put_committed`。任一写入/同步失败都保留 prepared 或明确的清理证据，禁止报告 Put 成功；启动按 authority 精确清理该 candidate 的临时与正式文件，不扫描猜测密钥环、不覆盖 active generation。
- 删除 local 物理账户时，精确删除其临时/正式文件并同步父目录，完成后才可从 authority 移除对应 slot；文件 not-found 仍须完成父目录同步这一持久化步骤。同步失败保留清理证据供幂等重试，所有已知物理账户清理持久化后才删除 authority 并同步其父目录。Unix 与 Windows 使用平台等效的持久化/原子发布原语；不支持或同步报错时返回失败，不把最佳努力当作持久成功。

`app_settings.migration.pending-secret-cleanup` 这个历史键在 schema 28 由 `SecretRefLifecycle` 深模块接管，语义收窄为**数据库业务 ref 生命周期账本**，不是所有 secret 的通用垃圾箱。值为严格 JSON `{version:1,entries:[{ref,state,reason,operationId}]}`：`state` 只允许 `provisional` / `retired`；`operationId` 在 provisional 时必填且在 retired 时为空；同一逻辑 ref 最多一项。reason 只允许 provisional 的 `channel_credential_provisional` / `proxy_key_provisional` / `import_provisional`，以及 retired 的 `channel_credential_replaced` / `channel_deleted` / `proxy_key_rotated` / `proxy_key_deleted` / `import_superseded` / `restore_superseded`。非固定 ref 在 `channels.secret_ref`、`proxy_keys.secret_ref`、`content_blobs.key_ref` 三处必须全局单一所有权，创建、导入和恢复都生成新 ref，领域校验遇到跨行复用直接拒绝。handler 不得读改这段 JSON；模块内所有账本 read-modify-write 使用同一短 SQLite 事务并串行化，避免并发 provisional/cleanup 覆盖彼此，但不得跨 `Store.Put/Delete`、网络或文件 I/O 持有数据库事务。

`BeginProvision(operationId)` 返回不可伪造的 `ProvisioningBatch` capability：模块先取得该 operationId 的进程 lease，再在每次 `Store.Put` **之前**把 provisional 项加入同一 batch 并提交；账本可见时清理器因此必然能观察到 active owner。batch 状态严格为 `caller_owned -> coordinator_owned -> database_committed -> completed`，仅 `caller_owned|coordinator_owned` 可转 `aborted`；前两态是进程 capability + provisional 账本状态，`database_committed` 只能由 2.7 的 `provisioning_batches` 行表示，`completed` 只能由校验后删除该行并释放 lease 表示。普通 short commit 在业务事务中消费 batch：同一 COMMIT 原子移除新 ref 的 provisional 项、把旧 ref 转成 retired、写业务行，并插入包含 operationId/kind/目标 projection 摘要/精确 newRefs 的唯一 committed 行。COMMIT 成功即进入不可逆 `database_committed`。新 snapshot 发布成功且 digest 一致后 `Complete()` 才删除该行并释放 lease。

`Abort()` 只能在持久提交判定明确为未提交时逐 ref 删除：每次删除前仍要确认三张当前引用表都未引用该 ref；删除成功后移除 provisional，失败保留账本，最后释放 lease 供后台重试。SQLite COMMIT 成功、COMMIT 返回不确定但 2.7 的匹配行存在，或任一新 ref 已被当前库引用时，**禁止 destructive Abort**；必须保持 dedicated batch journal，fail-closed 并只前滚固定 live 和内存 snapshot，完成后再 `Complete()`。panic/defer 也必须先按 operationId 重读该表和当前引用，不能因为进程内状态尚未来得及改变就删除新 ref。启动后旧进程 lease 自然失效：未被当前数据库引用且没有 committed 行的 provisional ref 可续删；匹配 committed 行的 newRefs 只能按 5.4 启动恢复前滚，不能由 lifecycle 清理器删除。

complete/migration 整包导入使用同一个 operationId 和一个 `ProvisioningBatch`，并在 coordinator 外完成全部新渠道/密钥 `Store.Put`。`configreplace` 只能把 prepared replacement 与该 capability 一并交给自身持有的 `ConfigReplacementCommitter` 窄 façade；coordinator 在当前令牌复核和内部提交串行点**原子接管** batch ownership，只有接管成功才进入 draining。drain 等待全部其它 `RequestLease`、`OperationLease` 和 SecretRefLifecycle operation lease，明确排除已被当前 draining operation 接管的这一份 batch lease；新 provision 仍被拒绝。接管前 re-auth/校验失败由调用方 `Abort()`；接管后、SQLite COMMIT 前的 drain 超时、最终校验失败、事务明确回滚或 panic 只由 coordinator `Abort()`。COMMIT 成功或提交结果需要按持久证据判定时立即进入上述 `database_committed` 前滚分支，此后发布失败/panic 只能 fail-closed 并续作，不能 Abort、交还 ownership 或删除 ref；新 snapshot 发布成功后只调用 `Complete()`。恢复 staging ref 只由 restore sidecar 管理，不进入该 batch。

retired 清理按 **ref 级活跃引用** 判断，禁止再用“最后一个快照 generation”代表全部读者。`RuntimeCoordinator` 必须知道每个仍有 lease 的不可变 snapshot 引用了哪些逻辑 ref；只有所有仍引用该 ref 的 `RequestLease` / system/admin `OperationLease` 都释放、当前数据库三张引用表都不再引用、且没有精确 `SecretReadLease` 或资源 pin 时，才可调用 `Store.Delete`。成功或 not-found 后原子删除账本项，失败保留重试。SQLite COMMIT 到新 snapshot 发布之间不运行该清理；发布失败则 fail-closed，仍按旧活跃 snapshot 集合保护。进程重启先收口 processing request/Attempt，旧进程 lease 已不存在，再按当前数据库引用和恢复 journal/pin 继续账本。

固定认证令牌的 staging/previous、restore opId staging/previous、恢复点 `backup-bundle-key:<backupId>` 分别由 auth journal、restore sidecar、恢复点 creating/deletion journal 管理；正文保留期的文件、行与 `key_ref` 由正文资源级清理 journal/pin 管理。它们不得塞进本账本，也不得按前缀扫描。`secret.Store.Delete` 对目标 generation 已不存在必须幂等成功；Store 内部另一后端残留副本继续由 authority 自己的 pending cleanup 记录处理。

### 3.0.1 正文资源创建与清理 journal

`ContentResourceStore` 是正文文件、`content_blobs` 行和正文加密 key ref 的深模块；正文资源不并入 `SecretRefLifecycle`，但创建和删除都必须有自己的持久责任人。现有创建的“Put secret → rename 文件 → INSERT 行”和删除的“删资源 → 删业务行”顺序都不能依赖进程内错误分支收口：进程可能在任一步成功后直接退出。

- journal 位于 `{dataDirectory}/resource-journals/content/<blobId>.json`，版本化且只保存 `{version,kind,operationId,requestId,blobId,keyRef,stagedPath,finalPath}`；`operationId` 是本次资源操作的高熵唯一 owner，`requestId` 必须绑定所属 Request，`kind` 为 `creating` 或 `deleting`。创建前先生成 `operationId/blobId/keyRef`，在 coordinator 父行删除的同一序列点确认 Request 仍存在并登记本次 content-write 引用，随后把路径规范化并原子写入 `creating` journal（临时文件、`0600`、fsync、原子替换、父目录 fsync），再允许 `Store.Put` 或正文文件写入；写入及 journal 收口前不得释放该引用。恢复时若业务行仍存在，连同 `requestId` 一起校验精确身份；启动先从 journal 重建按 Request 的未完成资源登记，不得只查 `content_blobs` 推断没有在途资源。
- 每个 `blobId` 同时只能有一个由 `ContentResourceStore` 签发的进程内 resource-operation capability；Create、保留期/容量清理、手动清理、父 Request 清理、启动恢复和后台重试全部经过同一 seam，外部 helper 不得直接覆盖或删除 journal。读取、原子替换和删除 journal 都在模块内部的 per-blob 串行点重新校验完整 `{kind,operationId,requestId,blobId,keyRef,finalPath}`；只有当前 capability 与磁盘 owner 完全相等才可继续或清除，owner 不符一律返回冲突/fail-closed，禁止仅按文件路径无条件删除。实例锁保证没有两个进程同时拥有该串行点；启动恢复只接管旧进程遗留且尚无本进程 owner 的精确 journal。
- 创建顺序固定为：写 creating journal → `Store.Put` → staged 文件写入并 fsync → rename 到 final 并 fsync 父目录 → 在同一业务事务 INSERT `content_blobs` → `CompleteCreate(operationId)` 条件删除仍属于本次 `creating` owner 的 journal 并 fsync 父目录 → 释放 content-write 引用。精确 `content_blobs` 行是唯一创建提交证据；成功 COMMIT 后、journal 收口前仍由 creating owner 持有，任何删除入口只能返回 busy/跳过重试，不能把同一路径改写为 `deleting`。即使 journal 收口后、content-write 引用释放前删除者取得 ownership，也只能发布自己的新 opId 并等待该引用；旧创建者已完成条件删除，绝不能误删新 deleting journal。
- `RecoverPending` 先校验 journal version/kind/operationId、`requestId/blobId/keyRef` 和规范化路径均精确且不越界，再按 `kind` 分支，并为该精确 owner 重建不可伪造的恢复 capability；禁止用同一条“业务行存在就要求资源完整”的规则混合创建与删除。
  - `creating`：若 `content_blobs` 存在且 `requestId/blobId/keyRef/finalPath` 完全一致，则创建已经提交，要求正式文件和 secret 完整，保留二者，只清 staged 残留，并以当前恢复 capability 的 `kind=creating + operationId` 条件收口 journal；若行不存在，则创建未提交，幂等删除 staged/final 文件及精确 key ref，全部成功后同样只条件删除该 creating owner 的 journal。行存在但字段不一致、路径越界、owner 不符或已提交资源缺失一律 fail-closed。
  - `deleting`：deleting journal 的持久发布是删除意图的提交证据，重启不得清掉 journal 后恢复保留意图。若业务行仍存在，只校验其 `requestId/blobId/keyRef/finalPath` 与 journal 完全一致；文件或 key 已不存在是合法中间态。重新确认正文读取 pin、恢复点 pin 和关联引用均已释放后，严格按“幂等删 staged/final 文件 → 条件删除精确业务行 → 精确删除 key ref → `CompleteDelete(operationId)` 条件删除仍属于本次 deleting owner 的 journal 并 fsync 父目录”继续前滚；业务行、文件或 key 已不存在分别视为对应步骤完成。字段不符、路径越界、owner 不符、条件删除命中其它身份或仍有 pin 时不得猜测删除。
- 保留期/容量/手动删除必须先通过 `BeginDelete(blobId)` 取得同一 per-blob ownership：已有 `creating` 时只能等待有界收口或跳过并稍后重试，不得覆盖；已有 `deleting` 时只有同一 operation 的恢复/重试 capability 可继续，竞争清理者必须退出。仅在确认没有 creating/deleting owner 后，才生成新 operationId、原子写并 fsync `deleting` journal；随后等待正文读取 pin、恢复点 capture pin 和关联业务引用释放，再按上述 deleting 分支前滚。删除失败保留原 owner journal，启动和后台按精确身份接管重试。该 journal 是本机运行状态，不进入配置导出、迁移包或恢复点 bundle。
- 正文读取、删除和恢复点 capture 共享 `ContentResourceStore` 的资源准入协议。`BeginRecoveryPointCapture` 先在同一序列点关闭新的 delete ownership，再固定已经取得 deleting owner 的 predecessor 集合；这些先行删除不受新 fence/pin 阻塞，capture 在自己的 deadline 内等待它们完整前滚并条件收口 journal。只有 predecessor 归零后 capture 才进入 `ready`、允许开始 SQLite online backup；超时、持久删除失败或身份冲突时必须释放 fence/operation lease 并返回可重试的 409 `recovery_point_capture_busy`，不得启动快照、建立精确 pin 或发布恢复点。fence 先成立时，后续删除只能跳过/重试，直到快照 refs Seal 后仅由精确 pin 阻止相关资源。`BeginAdminContentRead` 若先取得 read pin，后续删除等待；若 deleting owner 已先成立，新的正文查看固定返回 404 `content_unavailable`，不得读取可能已删除的文件、建立新 pin 或等待删除“恢复”。以上只有短序列点和 lease/condition 等待，不跨文件 I/O 持全局 mutex。
- 自动与手动日志清理的入口在读取当前清理设置/计算截止时间及任何正文、统计、Request、审计、运行日志或日志文件清理前，统一取得一个内部 maintenance `OperationLease`，整个清理调用（含最后的数据库/文件操作）退出才幂等释放；手动入口先在锁外限时读入并校验 payload，取得 lease 时再次验证当前管理令牌，再从同代配置取得设置。draining 等待整个操作或 30 秒超时零提交退出，不跨文件 I/O 持有全局 mutex。入口把“正文清理 → 父 Request 删除”交给 `ContentResourceStore` 编排；其它日志类别保留原模块责任，但必须在同一 lease 下执行，禁止 `CleanupLogsWithRetention` 等外部入口再独立批量删除父请求。
- 正文写入内部的容量整理 `trimContentQuota` 不是独立定时/手动清理：它接受由 coordinator 验证且仍有效的父 `RequestLease` 能力，作为同一正文写入的同步子操作执行，不调用 `BeginLogRetentionCleanup` 申请新的顶层 lease。父 lease 的生命周期必须覆盖 trim、正文文件/行提交与 journal 收口，draining 期间仍允许已经取得父 lease 的子操作完成；维护等待父 lease 就覆盖了这些子操作。不得仅凭 requestId 或布尔标记绕过维护准入，不允许 trim 异步逃逸到父 lease 释放之后。trim 仍经相同 `BeginDelete`、owner、pin/fence 和删除 journal，仅整理正文容量，不顺便清理父 Request、审计或运行日志；真实 I/O、容量不足或 pin 冲突沿用正文错误/重试规则，但不得仅因进入 draining 把合法在途写入改判为维护拒绝。独立后台/手动清理仍必须新建 maintenance lease，draining 时拒绝。
- 每条父 Request 的删除必须与 content-write/read/capture 引用及 pin/fence 的登记在 coordinator 同一短提交序列点互斥；在该序列点内用同一 SQLite 短事务复核其仍满足保留期、`final_status IN ('success','error','timeout','cancelled','partial')`、无活跃 Request/正文写入/正文读取/capture 引用且 `content_blobs` 没有该 `request_id` 的行，同时按上述 `requestId` 登记确认该请求没有未完成 creating/deleting journal，再按精确 `request_id` 条件删除。任一条件不满足只跳过并在下次清理重试，绝不能让 `ON DELETE CASCADE` 代替资源清理；未完成资源删除的 journal 必须保留到该资源的文件/行/key 全部清理成功，收口 journal 后才可删父行。恢复只能等旧集合的整个日志清理 maintenance lease 释放后切换 live，旧操作不得在新集合继续执行父行删除。
- 恢复替换、draining 和正文写入必须通过 content operation/request lease 协调；drain 等待所有正文创建/删除执行者后，恢复提交必须在旧 live 尚未移动时调用同一 `ContentResourceStore.RecoverPending` 并确认 creating/deleting journal 集合为空，才允许切换 live 目录。restore 还在 staging 且进程崩溃时，冷启动先安全终止未提交 restore，再按唯一启动清单收口正文 journal；进入 switching/committed 的持久状态必须已经满足“正文 journal 为空”不变量。正文读取继续只认固定 live 主密钥，恢复专用主密钥由 restore sidecar 管理。

### 3.1 `RuntimeCoordinator`：快照、请求 lease 与维护提交

`RuntimeCoordinator` 是运行时代次、活跃请求和破坏性替换的唯一并发边界。外部接口不得暴露 `Lock/RLock/Unlock`、锁顺序、“调用方已持锁”标记、`...Locked` helper 或接收任意 callback 的通用事务 runner。下面是 coordinator module 的**能力全集**，不是一个由所有调用方共同依赖的大型 Go interface：构造时按消费者交付窄 façade，`server`、`configreplace`、`RecoveryPointStore`、定时维护和启动装配各自只能看到完成其职责所需的方法。只有确有生产/测试两种 adapter 的 seam 才定义 interface；否则使用具体 façade，测试经同一外部 seam 验证，不为 mock 暴露内部提交机制。

为避免 Go 包反向依赖，具体实现在独立 `internal/runtimecoordinator` 包，由 `cmd/oneai-proxy/main.go` 装配数据库、SecretStore 和快照加载/提交回调；`internal/server` 只依赖 coordinator 的窄接口，`internal/storage` 只提供不感知 coordinator 的事务/快照原语。禁止把 coordinator 放进 storage 后再导入 server 类型，也禁止 server/storage 互相回调持有对方内部锁。

- `PreflightProxyAuth(bearer) -> ok | unauthorized | draining`
- `BeginProxyRequest(bearer, parsedRequestMeta) -> (RuntimeSnapshot, RequestLease, error)`
- `BeginSystemChannelOperation(selection) -> (ChannelOperationSnapshot, OperationLease, error)`
- `BeginAdminChannelOperation(currentAdminBearer, target AdminChannelTarget, purpose) -> (ChannelOperationSnapshot, OperationLease, error)`
- `BeginAdminContentRead(currentAdminBearer, blobId) -> (SensitiveContentSnapshot, OperationLease, error)`
- `BeginRecoveryPointCapture(currentAdminBearer) -> (RecoveryPointCapture, error)`
- `BeginLogRetentionCleanup(source) -> MaintenanceOperation`（内部定时器或已重新鉴权的手动清理；自持 `OperationLease`，整个自动/手动清理调用退出后幂等关闭）
- `CaptureAdminRead(currentAdminBearer, selection) -> immutable AdminSnapshot`
- `BeginAdminRuntimeStream(currentAdminBearer) -> cancellable AdminRuntimeSubscription`（绑定管理认证代次，不持持续 `OperationLease`）
- `CaptureAdminSecretRead(currentAdminBearer, selection) -> SensitiveAdminSnapshot`
- 类型化 short-commit façade：固定令牌、设置、渠道 bundle、访问组、列表密钥、恢复点逻辑隐藏等调用方只提交各自已验证的领域 command/capability；不存在 `mutation func(tx)` 之类公共入口
- 类型化 draining façade：`ConfigReplacementCommitter` 只接收 `configreplace` 拥有的 prepared replacement + 可选 `ProvisioningBatch`，`RecoveryCommitter` 只接收 `RecoveryPointStore` 拥有的 prepared restore；二者不向 handler 暴露通用 operation callback
- `RequestLease.Release()` / `OperationLease.Release()`

coordinator implementation 内部可以复用 short/draining executor，但那只是内部 seam；本文后续出现的 “short commit” / “draining commit” 指这套内部协议，不代表存在可被 handler、storage helper 或任意模块调用的 `RunShortCommit` / `RunDrainingCommit` 公共方法。每个类型化 façade 必须负责当前管理令牌复核、command/capability 类型校验和结果发布，调用方不能夹带自定义 SQL、任意闭包或绕过所属深模块不变量。

`AdminChannelTarget` 只允许两种显式形态，不能用“有无 channel id”隐式猜测：

- `saved_channel`：带 `channel_id`，由 coordinator 在当前管理令牌复核点读取当前行的 `channel_identity + channel_config_version`、静态配置和精确代次凭证；手动探针、已保存渠道模型发现以及对已保存值的测试走此分支，后续由 `PrepareAttempt` 使用该 snapshot。
- `draft_channel`：表单 payload 形成不可变目标快照，允许新建渠道未保存，也允许编辑已有渠道但测试尚未提交的新值。coordinator 仍须在当前管理令牌复核点登记 `OperationLease`；它不要求数据库 identity/ref，不写 SecretStore、渠道健康、额度、Attempt、敏感词账本或持久 reservation，使用请求拥有的临时凭证，网络结束后销毁并释放 lease。

草稿操作在已进入 draining 时立即返回 503；若已取得 lease 后开始 draining，则由 draining 等待该 lease，超时零提交并恢复接流。初次鉴权后管理令牌变化且尚未取得 lease 的草稿请求必须 401，不能继续发上游。草稿分支不调用业务 `PrepareAttempt`，也不写请求/Attempt 账本；它只承担当前管理表单的测试和模型发现。

`RuntimeSnapshot` 包含同一代次的认证主体、`RouteScope`、访问组/渠道目录、每个渠道的 `channelId + channelIdentity + channelConfigVersion`、渠道逻辑 secret ref、用于请求改写的全局执行设置、完整 `RequestPolicy`、不可变 `PricingSnapshot` 和已编译过滤器快照；`RouteTarget` 与 `ChannelOperationSnapshot` 也必须携带相同三元组，取得后不可变。`BeginProxyRequest` 成功后，整个 Request 的全局 reasoning/service-tier 设置、完整 `RequestPolicy` 与渠道目录一起冻结，后续 Attempt 的连接/首字节/流空闲超时及请求总预算/尝试数均从该 snapshot 取值，不得重新读取 live `ChannelSettings` 或 `RequestPolicy`。system/admin 已保存渠道 operation 同样冻结同代执行策略，保证 projection 与实际传输一致。`channel_identity` 是数据库内部 incarnation，`channel_config_version` 是同一 incarnation 内的上游执行/健康归因代次；两者都不进入普通管理 wire。删除重建必须换 identity；合成后的 `ChannelExecutionProjectionV1` 改变必须递增 config version。`RequestLease` 与 `OperationLease` 都只是 coordinator 对不可变 snapshot/资源的活跃引用，不持 mutex，也不允许调用方解锁；lease 必须幂等释放。短提交在数据库事务中持久登记 retired ref 后才发布新快照；清理器按逻辑 ref 检查**全部**活跃 snapshot，而不是只检查紧邻的上一代。上游 HTTP、SSE、正文文件读取和 Attempt 结算期间可以持有 lease，但不得跨网络或文件 I/O 持有全局锁。

`CaptureAdminRead` / `CaptureAdminSecretRead` 的返回值是**当前管理读取视图**，不是直接序列化 `RuntimeSnapshot`：在 coordinator 当前管理令牌复核及配置提交序列点，使用一次短 SQLite 只读事务读取所选业务配置与热变字段，同一视图内取得渠道/密钥的 `dollar_used_micros`、`dollar_limit_micros`、`status`、`disabled_reason`、`quota_blocked` 等，必要的派生状态也只以该事务内的值计算。与当前已发布配置快照核对代次，不允许把 G1 的配置/ref 拼到 G2 的动态状态；不一致则重试短捕获或 fail-closed。完整配置/迁移导出、管理列表与摘要都只序列化该捕获结果，不能读取运行快照中已过期的 used/status，也不能分别补读 used 和 status。类型化 secret selection 在同一序列点解析 live ref 并 pin 精确 generation，随后结束数据库读事务、释放序列点，再复制明文、加密或写响应；读事务不得跨 secret I/O、文件、KDF 或网络等待。结算可并发写动态字段，以**读事务的一致视图**为结果线性化点；它们继续不参与 `configDigest`。

不可变认证索引只负责常量时间校验 Bearer 并解析 `principalType + keyId`，**不作为列表密钥当前状态权威**。`BeginProxyRequest` 在 coordinator 的短提交序列点内，以 keyId 对 `proxy_keys` 做一次索引查询并复核 `tombstoned_at=''`、真实 `token_hash` 仍匹配、当前 `status/disabled_reason`、`group_id` 及 `dollar_used_micros/dollar_limit_micros`；读取错误 fail-closed。`status=disabled` 或 `used>=limit>0` 都返回带当前原因的 disabled 主体结果，后者即使库中状态尚未收敛也按 `quota_exhausted` 拒绝；不得依赖旧 snapshot 的 used/status。该读取与 `SettleAttemptHealth` 的写事务由 SQLite 提交顺序线性化：结算先提交，则紧邻新请求必见超限；Begin 先完成，则该请求已经取得 `RequestLease`，属于 0.6 允许继续结算的在途请求。管理端额度/状态修改、tombstone 和整包替换同样经 coordinator 提交序列，禁止在 Begin 复核与 lease 登记之间插入主体删除。`RuntimeConfigProjection` 继续排除这些动态字段，无需为每次结算重建全量 snapshot。

`BeginSystemChannelOperation` 只供内部调度器启动自动探针：从当前 snapshot 解析渠道，取得 system `OperationLease`，随后由 `PrepareAttempt` 使用同一 snapshot；draining 后拒绝新建。`BeginAdminChannelOperation` 在当前提交序列点重新校验原始管理 Bearer，按 `AdminChannelTarget` 分派：`saved_channel` 供手动探针、已保存渠道模型发现和已保存值测试，凭证仍只能由后续 `PrepareAttempt` 在该 snapshot 上读取并随 `AttemptLease` 返回；`draft_channel` 直接使用本次表单的 owned 临时凭证和目标快照，不读取 live ref，也不进入 Attempt/健康/额度账本。两条分支都不允许 handler 直接接触 SecretStore。

`BeginAdminContentRead` 同样重新鉴权，在同一步骤 pin 当前 `content_blobs` 行/正文路径，并用短 `SecretReadLease` 复制精确 blob `key_ref` 与当前 `content-master` generation 的明文到 owned `SensitiveContentSnapshot`，随后立即释放 secret lease。文件读取/解密在 coordinator 外执行，敏感快照必须 `Destroy()`；`OperationLease` 释放前正文清理和恢复目录切换不得移走该资源。

`BeginRecoveryPointCapture` 在当前令牌复核点取得内部 `OperationLease`，并通过 `ContentResourceStore` 的资源准入 seam 登记一个不持 mutex 的 capture fence：同一序列点先禁止新的正文 delete ownership，再固定已经取得 deleting owner 的 predecessor；fence 不得阻塞 predecessor 删除，capture 有界等待其条件收口，只有 predecessor 归零才返回 `ready` capability。超时或删除无法安全收口时关闭 fence/lease 并返回可重试 `recovery_point_capture_busy`，调用方不得开始 SQLite backup。ready 后的宽 fence 阻止正文/业务 secret 的物理删除和三个固定 ref 的 generation 切换，不阻止代理、Attempt 结算或普通非破坏性写入。返回的 `RecoveryPointCapture` 不可拆出 lease，只提供 `Seal(snapshotRefs)` 与幂等 `Close()`。`RecoveryPointStore.Create` 对成功返回的 capture 立即 `defer Close()`，随后用 `Snapshotter` 在 coordinator 外生成 SQLite online backup；从**该快照文件**读取正文路径与逻辑 refs 后调用 `Seal`，coordinator 在宽 fence 尚有效时原子建立精确资源 pin/`SecretReadLease` 并释放宽 fence。之后固定 ref 可以换代，但旧 generation 仍被精确 lease 保护。正常路径在 committed artifact 发布后返回；失败/取消路径在 abort 清理证据持久化后返回。restore/draining 必须等待 capture 内部 operation lease，或按 30 秒超时零提交。handler 不得查 live DB 拼 refs 或自行 `Store.Get`。

`BeginLogRetentionCleanup` 在开始时登记整个清理调用的内部 operation lease，外层自动/手动清理入口持有直到正文、父行、审计、运行日志及文件清理的所有调用返回；正文资源模块负责其内部“正文→父行”顺序，coordinator 用同一短序列点仲裁正文 pin/fence 的登记和父行删除，不能只保护单个 `content_blobs` 删除。恢复进入 draining 后不能启动新的独立定时/手动清理；已有 RequestLease 内的同步正文容量 trim 按 3.0.1 继续，不重新准入。已开始的清理及这些父请求必须全部完成或让恢复超时零提交；进入新集合后不得继续执行旧集合的清理回调。

每小时 `runStorageMaintenanceScheduler → maintainHotStorage`（包括启动后立即执行的一轮）也必须通过同一个 maintenance seam，以内部定时器 source 调用 `BeginLogRetentionCleanup`；在读取当前设置、计算截止时间或枚举小时桶前取得 lease，覆盖 `AggregateMissingMetrics` 的小时枚举、明细读取、内存计算、`persistMetricDimensions` 提交，以及随后 `TrimProbeRuns` 的所有删除，整个调用退出才幂等释放。draining 时跳过新轮次且不执行数据库工作；已有轮次由 drain 等待，30 秒未结束则恢复零提交退出并恢复接流，不能提前释放 lease 让旧计算结果写入恢复后的集合。小时 dirty revision 只用于同一集合内的并发更新，不能识别恢复换库；复用现有 lease，不另加统计恢复协调器。

管理 `/logs/runtime/stream` 不属于一次性 `CaptureAdminRead`，也不持整个 SSE 生命周期的 `OperationLease`。`BeginAdminRuntimeStream` 在当前提交序列点重新校验原始 Bearer、登记带认证 generation 的可取消订阅；draining 时新订阅 503。管理令牌更改、整包导入/恢复发布新认证代次或进入 draining 时，在同一序列点失效并取消旧订阅，不能让它阻塞 drain。历史补发与每条实时事件都要在选取和发送前确认订阅仍有效；代次失效后不能再选取、发送新的日志事件或 reset 帧，必须退出并释放订阅资源。允许换代点前已获发送许可的一帧完成写出，但换代点后选取的日志绝不能投递到旧连接；许可检查只包住选帧/登记，不跨网络写持 mutex 或长 lease，写入受逐次期限和请求取消约束，慢客户端不会卡住令牌更改或恢复。

**代理入口固定顺序：**

1. 先通过不可变认证索引做轻量 Bearer 鉴权、解析 keyId 并读取 coordinator 维护状态，不读取完整业务目录、不取得 `RequestLease`、不以缓存 quota 状态作最终准入；无效直接 401，draining 立即 `503 + Retry-After`，二者都不读 body。
2. 在没有全局 lease/锁的状态下，按请求体大小上限和读取 deadline 读取并解析 body；读取失败不建账本。
3. 调用 `BeginProxyRequest`，由 coordinator 在当前提交序列点重新校验 Bearer，并按上文从数据库复核列表密钥当前动态状态：
   - 正常列表密钥（含 enabled/disabled）返回 `RouteScope` 与本次读取的密钥状态快照；tombstone 和 `pending:` 占位永不命中。
   - 固定通用代理令牌返回 `RouteScope{admin_proxy, "admin-proxy", ""}`。
   - 都不匹配返回 401；处于 draining 时立即返回 `503` 和 `Retry-After`，不排队等待维护结束。
4. `forwardAPI` 使用返回的不可变目录快照与本次主体状态只建一次账本；当前人工停用或额度停用的密钥在建账本后返回 `403 key_disabled`，不得选路或创建 Attempt。请求结束的所有路径都调用幂等 `RequestLease.Release`。
5. 固定令牌、普通 CRUD、渠道凭证和密钥 tombstone 使用短提交并发布新快照；渠道凭证更新必须新建逻辑 ref，禁止原地覆盖旧 ref。更新渠道行、移除新 ref 的 provisional 项和持久登记旧 retired ref 必须属于同一 SQLite 事务，COMMIT 后才发布新快照。旧请求/探针继续使用已取得的旧 snapshot，新操作只能取得新 snapshot；旧渠道 ref 必须等所有仍引用它的运行 lease 和 secret/resource lease 释放后再删。列表密钥物理清理由 coordinator 的引用状态与数据库 processing 状态共同决定。

**破坏性整包替换固定顺序：**

配置导入/迁移导入只经 `ConfigReplacementCommitter`，同实例恢复只经 `RecoveryCommitter`；两种类型化 façade 在 coordinator implementation 内复用同一 draining 协议。coordinator 在同一内部串行点先按当前管理令牌重新鉴权；complete/migration 还必须在这里原子接管参数中的 `ProvisioningBatch` capability，接管失败不得进入维护。随后原子切换为 `draining` 并取消已有管理日志订阅；旧令牌请求返回 401，不得先扰动维护状态。进入 draining 后，新代理、新 system/admin channel operation、新正文读取、新恢复点 capture、新独立日志清理、新管理日志订阅、新 provisional secret 操作，以及除 `/healthz` 和已经运行的当前操作响应外的新管理请求都立即返回 `503 + Retry-After`，不排队；`/healthz` 继续 200 并带 `state=draining`。已有 RequestLease 内的同步正文写入及容量 trim 可继续，不能再申请顶层清理 lease；随后等待已有 `RequestLease`、全部 `OperationLease`（包括整段正文及父记录清理）与**除当前已接管 batch 外**的 SecretRefLifecycle operation lease；已取消的日志订阅不计入等待集合，最多等待 `maintenanceDrainTimeout`。超时必须退出维护、零业务提交并由 coordinator abort 已接管 batch；成功 drain 后仅在 module 内部短提交窗口提交数据库并消费 batch。SQLite COMMIT 是 batch 的不可逆分界：此前失败才允许 Abort；此后运行快照或固定 live 发布失败必须保留 journal、fail-closed 并只前滚，禁止删除数据库已引用的新 ref。任何 prepare、解密、文件复制或网络 I/O 都不得放进该短提交窗口。

维护响应固定为 HTTP 503、`Retry-After: 30` 和 `{"error":"服务维护中，请稍后重试","code":"maintenance_draining"}`。恢复点捕获期间仅覆盖被 pin 固定 ref 的令牌修改返回 HTTP 409、code=`recovery_point_capture_in_progress`；其它代理和管理读取不受影响。

同实例恢复可以在管理端显式进入停服维护状态；普通 CRUD、固定令牌修改、列表密钥逻辑删除、恢复点创建和删除不得全站 drain。恢复点创建只在短临界区登记 capture fence；SQLite 快照由独立只读连接在临界区外固定源读事务后执行 online backup，随后把宽 fence 原子收窄为快照内精确 pin，正文复制与 bundle 加密继续在锁外完成。

HTTP Server 默认 `ReadHeaderTimeout=10s`、`IdleTimeout=120s`，代理和管理请求体读取 deadline 固定 30s；继续沿用各入口现有限长（代理/配置为 8 MiB，迁移包用自身常量）。请求体 deadline 使用 `http.NewResponseController(writer).SetReadDeadline` 或等价的实际连接读取期限，读取结束后、任何数据库/secret 写入或上游流开始前以 `SetReadDeadline(time.Time{})` 清除，不能只包 `context.WithTimeout`。现有 `runtimeResponseWriter`、`auditResponseWriter` 必须实现 `Unwrap() http.ResponseWriter` 返回直接下层，今后新增 wrapper 也必须保持该链；不得忽略 `SetReadDeadline` 的错误。设置失败时不读取 body、不建账本、不写数据库/secret，设置 `Connection: close` 并返回 HTTP 500、code=`request_read_deadline_unavailable`；读取后清除失败也在任何副作用前同样 fail-closed 并关闭连接，不能带着未知连接 deadline 开始 SSE 或提交管理操作。body 本身超时使用既有脱敏 408/读取失败语义。`maintenanceDrainTimeout` 固定 30s，本阶段不新增 UI 配置。不要用短的全局 `WriteTimeout` 截断 SSE；流式继续由请求总时限、stream idle timeout 和逐次写 deadline 约束。所有超时均需穿过真实 middleware 链在临时反馈环中验证。

### 3.1.1 管理请求的 prepare / re-auth / commit

管理 API 固定分为锁外 prepare 与 coordinator commit 两阶段：

1. 读取 body 前校验 Host/Origin，并用当前管理 Bearer 做初次鉴权；失败不读取业务或秘密。
2. 在 coordinator 之外限长、限时读取请求体，完成 JSON/信封解码、口令解密、严格字段校验、领域校验和脱敏预览。该阶段不得写 SQLite、secret Store 或运行快照。
3. 提交时把**原始 Bearer**与类型化 command/capability 交给对应的窄 commit façade；coordinator 必须在取得内部提交序列点后对当前代次的管理令牌重新鉴权。令牌已变化则返回 401 且零提交，不得信任初次鉴权 context；draining façade 只有复核通过且已原子接管本次可选 `ProvisioningBatch` 后才可切换维护状态。handler 不得取得内部 runner、SQLite callback 或其它领域的 commit façade。
4. 普通元数据读请求必须调用 `CaptureAdminRead`，在 coordinator 当前提交序列点重新校验原始 Bearer，并从一次短 SQLite 读事务复制配置与动态字段一致的管理读取视图；初次 middleware 鉴权不能代替这次复核。单个 secret envelope、渠道复制源、`complete_encrypted` 配置导出和迁移导出统一调用 `CaptureAdminSecretRead`：selection 只能是类型化的领域目标/导出模式，coordinator 在同一次令牌复核和短读事务中确定全部业务字段和 live ref，先 pin 精确 secret generation，结束读事务后才复制明文并立即释放 `SecretReadLease`，返回带显式 `Destroy()` 的 owned `SensitiveAdminSnapshot`；任何失败都释放 pin 且不返回部分导出。`channel_copy_source` 的结果必须是一个完整不可变 `ChannelCopySourceSnapshot`，同时包含同一捕获视图的可复制渠道 bundle、models、mappings、probe policy、`groupIds`、额度/过滤设置，以及与之同代的凭证元数据和**可选** owned 明文：仅 `admin_state=disabled && secret_ref=''` 可返回「无凭证」；ref 非空却不可读取时拒绝，不得降级为空；禁止 handler 先读任一元数据、再单独取得凭证。配置/迁移/复制模块在 coordinator 外完成必要的 KDF、压缩、加密或新 ref provisional 写入，并在所有成功/失败路径立即销毁敏感快照；渠道复制最终仍把同一原始 Bearer与类型化 `ChannelBundleCommand` 交给 `ChannelBundleCommitter` 二次复核，令牌已变化则清理已创建的新 ref（如有）且零提交。
5. 手动探针、已保存渠道模型发现和已保存值测试必须以 `saved_channel` 调用 `BeginAdminChannelOperation`；新建未保存渠道的测试/模型发现，以及编辑表单尚未保存值的测试必须以 `draft_channel` 调用同一入口，直接使用表单 owned 凭证，不得强行查数据库行。自动探针必须调用 `BeginSystemChannelOperation`；请求正文/响应正文查看必须调用 `BeginAdminContentRead`。这些入口不得直接查询 live `secret_ref`、调用 `Store.Get` 或缓存 `content-master`，也不得用 middleware 的旧鉴权 context。旧管理令牌若在初次鉴权后被轮换，任何普通 GET、完整导出、迁移导出、渠道/列表密钥查看、渠道复制、手动测试、模型发现或正文读取都必须在取得数据/秘密前 401。
6. 管理运行日志 SSE 必须单独归类为 cancellable subscription，不能按单次 metadata read 或长时间 operation lease 处理；连接取消时释放资源，前端持当前令牌与现有游标重连，401 暂停重连直至令牌变动或重新输入，503 遵循 `Retry-After`，保留原有游标失效的 reset 语义。其它新增管理入口必须显式归类为 metadata read、secret snapshot read、leased channel/content operation、short commit 或 draining commit；测试遍历全部 `/api/admin/v1/*` 注册项，漏分类直接失败。内部自动探针注册点另做静态枚举，确保只能从 system operation seam 进入。

配置/迁移导入在锁外完成限长读取、解密和解析；`ConfigReplacementCommitter` 先重新校验当前管理令牌再进入 draining，drain 完成后重跑所有依赖 live 状态的冲突/引用/领域校验，再做短提交。同实例恢复也必须经 `RecoveryCommitter` 在停服维护状态内复核 manifest、实例绑定和 live 恢复条件。恢复点创建、列表、校验和删除由 `RecoveryPointStore` 管理 pin/journal，不归类为 draining commit，但必须把原始 Bearer 交给 coordinator：List/Verify 使用当前管理读复核，Create 使用 `BeginRecoveryPointCapture`，Delete 的逻辑隐藏使用其类型化 short-commit façade 复核；Restore 只由 store 内部调用 `RecoveryCommitter`。handler 不得先鉴权后绕过这条 seam 直接操作 artifact。

### 3.1.2 管理/代理令牌双存储一致性

当前 `LoadOrCreateAuthTokens` **不**校验密钥环明文的 hash 是否等于库内 hash；`SetAuthToken` / `RotateAuthToken` / 配置导入 / 迁移导入都是先写密钥环再写库。本计划把「两个固定认证令牌」做成**一个模块**，不能沿用「对不上就补齐」，也不能只用单槽 journal。

`app_settings` 键 `auth.token_journal`（一次操作覆盖 admin+proxy 两个槽）。每个槽必须同时记下**新值**和**可恢复的旧值**，否则 live 被覆盖后无法回到完整旧集合：

```json
{
  "opId": "uuid",
  "phase": "staging"|"writing_live"|"committed",
  "admin": {
    "expectedHash": "<new>",
    "previousHash": "<old or empty>",
    "custom": true,
    "stagingRef": "auth-admin-token-staging",
    "previousRef": "auth-admin-token-previous"
  },
  "proxy": {
    "expectedHash": "<new>",
    "previousHash": "<old or empty>",
    "custom": true,
    "stagingRef": "auth-proxy-token-staging",
    "previousRef": "auth-proxy-token-previous"
  }
}
```

固定 live **逻辑 ref** 仍是 `auth-admin-token` / `auth-proxy-token`。staging / previous 也是逻辑 ref，只作暂存，鉴权不读它们；所有 ref 进入实际后端前统一经过 3.0 的实例命名空间映射。

`auth.token_journal` 是本机运行元数据，不属于可导出配置。任何会整表替换 `app_settings` 的配置/迁移导入，都必须把当前 journal 和 `instance.secret_binding` 保存在事务输入中：删除/插入普通设置后、COMMIT 前，在**同一事务**原样写回 binding 和 `phase=writing_live` 的 journal，并与新 `auth.tokens` 哈希一起提交。禁止先在旧库写运行键、随后被 `DELETE FROM app_settings` 删除。safe 导入不改固定令牌，也不得从包中导入或覆盖这两个运行键。

固定令牌模块在同一 coordinator 提交序列点只允许一个 journal 操作。开始 Set/Rotate、complete 导入、迁移导入或同实例恢复前，必须先按启动规则收口已有 `auth.token_journal`；`committed` 清理失败时新集合可以继续提供服务，但下一次会覆盖固定 live 的操作必须先完成该清理，否则返回 409/503，禁止覆盖固定 staging/previous ref 或让 restore 替换掉未收口 journal。

单槽 Set/Rotate（只改 admin 或只改 proxy）也走同一模块：未改的槽 `expectedHash = previousHash = 当前 live hash`，staging 与 previous 都拷贝当前明文。

**运行中实例 · 仅 Set / Rotate**（不换目录）写入顺序：

1. 写 journal `phase=staging`。禁止直接 `PersistAuthTokenSecrets`。
2. 把当前 live 明文写入两个 `previousRef`（缺旧明文则中止）。
3. 把新明文写入两个 `stagingRef`。
4. journal `phase=writing_live`。
5. 把 staging 拷到两个 live ref。
6. 写 `auth.tokens` 两个新哈希（同一 JSON，一次 SQL）。
7. 重新读取库内哈希并验证两个 live 都等于 expectedHash；更新内存 `s.auth`，再写 journal `phase=committed`。到此操作语义成功。
8. 尽力删 staging / previous，再删 journal。清理失败保留 committed journal 供启动收口，记录日志但不得回滚新 live 或新哈希。

**运行中实例 · complete 配置导入 / 迁移导入**（目录与令牌一起换）。**撤回 11.6「目录事务提交之后才写 hash」**：那会留下「新目录 + 旧 hash → 启动按旧 hash 拉回旧令牌」。正确顺序：

1. journal `phase=staging`：写 expectedHash / previousHash / stagingRef / previousRef。禁止 `PersistAuthTokenSecrets`。
2. 当前 live 明文写入两个 `previousRef`。
3. 新明文写入两个 `stagingRef`。**此阶段不要改 live。**
4. journal `phase=writing_live`。
5. **同一个 SQLite 事务**提交：渠道目录、访问组、成员、列表密钥、`content_filter`、`auth.tokens` 两个新哈希、其它可迁移配置；`app_settings` 替换完成后在 COMMIT 前原样写回目标 `instance.secret_binding`，并重新写入当前 `phase=writing_live` 的 `auth.token_journal`。日志占位渠道也在这个事务里处理（见 5.4）。
6. 事务成功后，把 staging 拷到两个 live ref。
7. 重新读取库内哈希并验证两个 live 都等于 expectedHash；原子发布新的 `s.auth`、认证索引、运行设置、目录和 `ContentFilterSnapshot`，再写 journal `phase=committed`。到此新目录语义成功，管理令牌与通用代理令牌**立即生效**，旧令牌立即 401；只有监听地址和端口继续等待重启。内存发布失败必须保留 journal 并 fail-closed，不能继续使用旧认证快照。
8. 尽力删 staging / previous，再删 journal。清理失败保留 committed journal 供启动收口，不能把已经提交的新目录和新哈希拉回旧令牌。

崩溃语义（导入/迁移）：

- 事务提交前崩溃（含 staging 已写、writing_live 但库内 hash 仍是 previousHash）：启动修复把 previousRef 保持为 live（live 本就没改），清 journal。**完整旧集合**（旧目录 + 旧令牌）。
- 事务已提交、live 尚未齐或 journal 仍是 writing_live：库内两个 hash 都等于 expectedHash → 用 staging 补齐两个 live，得到**完整新集合**（新目录 + 新令牌）。
- live 一槽新一槽旧时，仍按**数据库哈希的提交判定**用 previous 或 staging 整套对齐，禁止带着半套继续跑；不得仅凭 live 当前内容决定回旧。

**禁止**先覆盖 live 再提交目录；**禁止**目录事务提交后再单独写 `auth.tokens`。

**全新数据库首次初始化**：`previousHash` 为空，不写 previousRef。不得信任系统密钥环中可能残留的同名固定 live ref；先把本文规定的新默认管理/代理令牌写入 staging，再按 journal 覆盖两个 live，使 SQLite 哈希与固定 ref 成为同一套新集合。staging 崩溃则清 journal后重新初始化；`writing_live` 且 live 未齐则从 staging 写成完整新集合。

启动修复（Set/Rotate 与导入共用判定，目标仍是完整旧或完整新）：

1. 无 journal：两个 live 明文 hash 必须都等于库内对应 hash，否则**阻止启动**。
2. `staging`：视为未切 live。删 staging / previous，清 journal，保持旧 live。
3. `writing_live`：
   - 库内两个 hash **都等于** expectedHash：把 staging 拷到两个 live（若已是新值则幂等），然后 committed。完整新集合。
   - 库内两个 hash **都等于** previousHash（或仍是导入前的旧 hash）：live 用 previousRef（若 live 已被改过则拷回），清 journal。完整旧集合。
   - 库内哈希既不是完整 expected 也不是完整 previous：**阻止启动并保留 journal**，不得用 previous 掩盖未知提交状态。live 一槽新一槽旧不改变上述数据库判定。
4. `committed` 但 journal 未删：库内必须完整等于 expectedHash；两个 live 不齐时由 staging 补齐，校验后清 staging / previous 和 journal。staging 缺失且 live 对不上则阻止启动。
5. `AllowLocalDefaultTokens` 只接受当前 `LocalDefaultProxyToken`（`sk-oneai-admin-proxy`），且仅当 `ProxyCustom=false` 且库内 hash 就是该常量的 hash。

进程内任何一步报错都必须调用与启动恢复共用的判定函数，在 coordinator 的同一提交序列点读取库内两个哈希：

- 库内仍完整等于 previousHash：操作未提交，才允许 previousRef → 两个 live（若 live 未动则保持），验证旧集合后清 staging / previous / journal。
- 库内已完整等于 expectedHash：操作已经提交，**只允许前滚**。从 staging 补齐两个 live，验证后立即刷新相应内存；无法补齐或刷新时保留 journal，并立即停止代理与管理写接口。不得返回错误后继续用旧 `s.auth`，不得恢复 previous。
- 其它组合：提交状态不明，保留 journal并 fail-closed，等待启动恢复或人工处理。
- `phase=committed` 后只有清理失败：新集合继续生效，保留 committed journal供下次清理；不得把语义成功改判为旧集合。

**不要**再调用无 journal 的 `PersistAuthTokenSecrets(previousAuth)`，也不得在数据库 COMMIT 后用同实例恢复点或令牌 previous 单独回滚目录。

全新数据库直接写入 `sk-oneai-admin-proxy`。启动前已按「数据兼容边界」拒绝 pre-28 数据库，因此不增加 `legacy/current` 默认令牌判定，不保留 `oneai-local-proxy` fallback，也不需要 `/auth` 暴露旧默认类型。

### 3.2 `ResolveRoute(snapshot, input, scope)`

`BeginProxyRequest` 成功后，本请求的初始选路、指定目标刷新、fallback、Responses 亲和目标解析和请求体改写都只读同一个不可变 `RuntimeSnapshot`，不得再次从 live 目录拼出另一代渠道、组、映射、secret ref 或全局 reasoning/service-tier 设置。数据库只用于读取/写入按主体隔离的 affinity 记录和账本；读到 affinity channel id 后仍必须回到本请求 snapshot 解析目标。人工状态、健康、额度、渠道是否已删除和并发属于准入动态状态，每次 Attempt 由 `PrepareAttempt` 使用 snapshot 的 `(channel_id, channel_identity, channel_config_version)` 读取当前值。查询无行或 identity 不同都视为 `Skipped(channel_removed)`，不能把同 id 的新行当成旧渠道；identity 相同但 config version 不同不代表删除，按 3.3 的旧配置分支处理：允许的旧业务 Attempt 继续使用 snapshot 的 Base URL、凭证、模型、全局执行设置和价格，但不得把其结果归因到当前配置健康。

Resolver **必须返回可区分的失败**，不能再用一个 `ErrNoAvailableRoute` 包办组问题和模型问题：

- 候选查询对所有主体先硬排除 `channels.is_history_placeholder=1`。`proxy_key`、`admin_proxy`、`none` 都不得命中占位渠道，不能依赖其当前无凭证、人工禁用或无访问组的形态间接排除。
- `ErrGroupMismatch`：列表密钥 `GroupID` 空 / 组不存在 / `enabled=0` / 该组 `channel_group_members` 为空。调用方写 `group_mismatch`。
- `ErrNoAvailableRoute`：组有效且有成员（或 `admin_proxy` / `none` 不按组过滤），但协议 / 模型 / 凭证 / 能力没有候选。调用方写现有 `routing_exhausted`。
- `scope.PrincipalType == none | admin_proxy`：不按访问组过滤，只可能走到 `ErrNoAvailableRoute`。
- `proxy_key` 且组有效：`EXISTS (channel_group_members)` 过滤后再走协议/模型/凭证/能力/排序。
- `ResolveRouteTarget` / `ResolveRouteAffinity` **必须接收同一 snapshot 与 scope**，内部调用带 scope 的 `ResolveRoute`。禁止再调用 live 目录或无 scope 重载。组失败不得被改写成亲和不可用。
- 渠道人工状态、健康、额度、凭证和并发不是 Resolver 的最终准入职责；业务候选由 `PrepareAttempt` 在每次尝试前按 3.3 重新判定。

### 3.3 额度累加与停用

只在 `SettleAttemptHealth` 同一事务里结算。`updateAttempt` 返回 `transitioned`；`false` 则整次 no-op。`true` 时任何终态都置 `billing_applied=1`；仅 0→1 且 `billed_micros > 0` 时累加 used。规则见 2.4、0.4.1 与 0.6。

开始时密钥已禁用：建账本后 403，不转发。
业务准入（`purpose=business`）时 `quota_blocked=1` 或 `used >= limit > 0`：`PrepareAttempt` 跳过该渠道，`candidate_skipped` reason=`quota_exhausted`。所有非业务 purpose 都不计费、不累加 used、不清除 `quota_blocked`，但健康行为必须按 purpose 明确区分：

- `manual_probe`：保留既有版本化探针健康结算；half-open 成功转 healthy、失败转 cooldown，healthy/degraded 下失败按既有探针失败计数和阈值推进。它不得自动恢复 `auto_disabled`，不得启用 `admin_state=disabled` 渠道，也不得改变额度状态。
- `automatic_probe`：按既有版本化探针健康结算；只有满足 probe enabled、autoRecover 和成功阈值时才允许恢复 `auto_disabled`，不改变人工禁用或额度状态。
- `admin_discovery`：忽略额度门闩且完全不结算健康。
- `draft_channel`：不进入已保存渠道的 `PrepareAttempt`、Attempt、持久健康或额度状态机，只使用 admin operation 持有的表单 owned 凭证。

全局敏感词命中：不选路、不 Attempt。
渠道敏感词、渠道凭证与动态准入统一封装进深模块 `PrepareAttempt(snapshot, candidate, normalizedBody, purpose)`；调用方不得自行拼接“读 secret → 检查词表 → tryAcquire”顺序，也不得在返回后再按 `secret_ref` 读取凭证。它只返回已携带精确代次凭证的 `AttemptLease`、`Skipped(reason)`、`ContentRejected` 或错误，内部顺序固定为：

1. 只从调用方运行 lease 固定的同一 snapshot 刷新候选：业务请求使用 `RequestLease + RuntimeSnapshot`，自动探针使用 system `OperationLease + ChannelOperationSnapshot`，手动探针和已保存渠道模型发现使用已重新鉴权的 admin `OperationLease + ChannelOperationSnapshot`。先按 `channel_id + channel_identity` 复核 incarnation，再比较 snapshot/current `channel_config_version`。identity 不同按 `channel_removed`；config version 相同才按 purpose 复用当前状态机：business 执行人工状态、业务健康和额度门闩；automatic_probe 按上表参与健康结算和受控自动恢复；manual_probe 按上表参与探针健康结算但不执行自动恢复；admin_discovery 只取得非健康、非计费 reservation。
2. identity 相同但 config version 不同时，禁止把旧调用当成当前配置的探针：`automatic_probe` / `manual_probe` 直接 `Skipped(channel_config_changed)`；`business` 仍复核当前人工禁用、额度、删除和普通并发上限，只有当前健康为 `healthy` / `degraded` 才允许使用旧 snapshot 继续，当前为 `cooldown` / `half_open` / `auto_disabled` 时 `Skipped(channel_config_changed)`，不得用旧配置承担恢复验证；`admin_discovery` 可按原有非健康语义使用其一致旧 snapshot。所有允许继续的 config mismatch 分支固定 `healthAttribution=stale_config_neutral`，只取得普通并发/删除保护 reservation，不领取当前配置半开 owner，后续不得修改当前健康状态、失败/探针计数、最后错误或 owner。业务 Attempt 的 usage、价格、密钥/渠道额度仍按 Attempt 规则正常结算。
3. 在模块内部为**快照中的**逻辑 ref 取得精确 generation 的 `SecretReadLease` 并复制凭证明文；配置已换新不等于旧 snapshot ref 无效，生命周期清理仍须等待该 lease。config version 相同时，在同一准入状态机原子领取可取消的半开/并发 reservation；半开领取生成高熵 owner，并以 `(channel_identity, channel_config_version, health_version, owner)` CAS 持久占用，该四元组随 reservation/lease 传递。config version 不同时只取得上条允许的普通 reservation。secret 不可用或竞争失败返回 `Skipped(reason)`/存储错误；不执行渠道词表。
4. 仅对已经取得 reservation 的业务候选执行渠道词表；命中则只用该 reservation 的 identity/config/health/owner 条件释放自己持有的半开 claim（stale neutral 分支没有 claim）、并释放并发 reservation 和 secret lease，返回 `ContentRejected`，整次请求 400，不再 fallback、本候选不建 Attempt，也不消费半开结果；先前已结算的 Attempt/费用/健康不回滚。取消、Record 失败和 not-sent 走同一 owner-aware 释放原语。
5. 词表通过后把 reservation 提升为 `AttemptLease`，将凭证明文、snapshot config version 与 `healthAttribution` 作为 lease 字段返回并释放 `SecretReadLease`。`RecordAttempt` 在任何上游字节发出前把 config version 写入 Attempt；Prepare 时版本一致先写 `pending`，已确认 mismatch 则直接写 `stale_config_neutral`。后续状态变化不撤销已领取 lease；调用方只消费 lease，不再触碰 SecretStore。

若 config version 在 `PrepareAttempt` 成功后、Attempt 结算前再次变化，已取得的 lease 可继续完成，但 `SettleAttemptHealth` 必须在同一事务先完成 Attempt 终态/计费，再以 `channel_identity + channel_config_version + health_version + owner` 复核健康归因；config version 不匹配时把 `health_attribution` 改为 `stale_config_neutral` 并让健康部分 no-op，绝不能清除新配置 owner。匹配时写 `current_config` 并继续既有健康状态机。`CloseInterruptedWork` 不做健康归因，写 `not_applied`。

对 business 不可承接的人工禁用、健康禁用、额度耗尽、凭证不可用或并发已满渠道都不能凭自己的词表拦截请求；并发竞争失败发生在 reservation 之前，因此也不能先产生内容拒绝。探针和模型发现不运行敏感词，但仍复用 purpose-aware 的同一准入状态机；自动探针不得从 live DB 临时拼 target，手动探针和模型发现不得在 middleware 鉴权后直接读 secret。

### 3.4 提前建账本与快照

`CreateRequest` 在选路前调用。当时还没有 `ResolveRoute` 的逻辑模型。

| 字段 | 建账本时 | 选路成功后 |
| --- | --- | --- |
| `client_model` | body 的 model | 不变 |
| `logical_model` | 先写客户端模型 | `UPDATE` 为 `targets[0].LogicalModel` |
| `proxy_key_id/name` | `RuntimeSnapshot` 的鉴权快照；通用令牌空；即使该 key 随后 tombstone 也不变 | 不变 |
| `access_group_id/name` | 鉴权当时的组 id/名称（组已删则 id 仍在、name 空） | **不**随路由刷新改写 |
| 正文快照 | 建账本后、全局过滤前 `persistClientRequestContent` | — |

快照失败：账本标 `error` / `ledger_error`，HTTP 500「客户端请求快照保存失败」，不再做敏感词和选路。管理员看不到正文，但看得到失败原因。

请求行和 Attempt 的主体引用是 tombstone 延迟清理的权威证据之一。物理清理前必须同时确认 coordinator 已无该 key 的 `RequestLease`，且库内没有该主体关联的 processing request/Attempt；不得因管理列表已经隐藏就把主体行提前删除。

---

## 4. 计价规则

金额见 0.4（微美元）。**先按 0.4.1 归一化再匹配单价**。匹配目录，**查询必须 ORDER BY**，禁止无序「取第一条」：

1. 协议与本次请求相同，`source_status = valid`。
2. 名称优先级（某档存在记录即停，不再看下一档）：上游模型 `model_id` → 逻辑模型 → 客户端模型。
3. 同一档查询仍用 `ORDER BY provider ASC, stable_key ASC` 保证诊断结果稳定，但**只有恰好一条** `source_status=valid` 记录时才能计价。命中 2 条及以上即为价格歧义：`billing_status=unpriced`、`billed_micros=0`、`price_stable_key=''`，写不含秘密的运行日志 `billing.price_ambiguous`（记录候选 stable key），不得按字母顺序任取。当前 `Channel` / `RouteTarget` 没有供应商或目录 stable key 绑定，确定性排序不能证明价格属于真实上游。
4. `currency` 空或 `USD`：按 USD。其它币种：**当无价**（0 + `unpriced`），快照仍记下原 currency，不换汇。
5. `billingUnit`：
   - 空 / `per_1m_tokens`：价是每百万 token
   - `per_1k_tokens`：先把价 ×1000 再按每百万算
   - 其它单位：当无价
6. 四个单价都空，或价格匹配歧义：`unpriced`，金额 0，放行。
7. 通用令牌：usage missing/invalid 时固定 `usage_unknown`；usage valid 但价格不可计时固定 `unpriced`；只有用量有效且价格可算才是 `admin`，金额计入**渠道** used，不计入密钥。见 2.4。
8. 请求合计与 usage 缺失见 2.4。单价 → 微美元必须走十进制定点，见 2.4。
9. 低价不得在乘 token 前归零：`price_per_million="0.00000049"`、`billable_tokens=1_000_000_000` 必须得到 `490` 微美元。

「渠道编辑加模型必须在目录中」本计划不做。

---

## 5. 管理 API

均在 `/api/admin/v1/`，管理令牌 Bearer。渠道行级 POST/PUT 继续禁止。

### 5.1 分组 `/api/admin/v1/access-groups`

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | `/access-groups` | 列表：id、name、note、enabled、isDefault、渠道数、密钥数 |
| GET | `/access-groups/{id}` | 详情 + `channelIds` |
| POST | `/access-groups` | 创建（name 必填、唯一；禁止创建第二个 `is_default`） |
| PUT | `/access-groups/{id}` | 非默认组：改 name/note/enabled/`channelIds`。**`channelIds` 里每个渠道必须已是 default 成员且 `is_history_placeholder=0`**，否则 400/409，不隐式补 default、不复活占位渠道。默认组：禁止改 name、禁止改 `channelIds`、**禁止 `enabled=false`**；只允许 note |
| DELETE | `/access-groups/{id}` | `is_default = 1` → 400「默认分组不能删除」。其它组：成员 CASCADE，密钥 `group_id` SET NULL，渠道行保留 |

名称唯一、大小写敏感。`default` 行名称不允许改，因此不会出现第二组叫 `default`。

### 5.2 密钥 `/api/admin/v1/proxy-keys`

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | `/proxy-keys` | 无明文；prefix、groupId/name（可空）、status、disabledReason、limitMicros、usedMicros；金额为规范十进制字符串 |
| POST | `/proxy-keys` | name、note、**groupId 可空**、dollarLimitMicros（规范十进制字符串）；请求不得带 id，服务端生成至少 128 bit 随机且永不复用的 id，并立即生成真实 token/hash/prefix/secret_ref，固定创建为 `disabled + manual`；写密钥环使用 `proxy_key_provisional` 生命周期项；响应返回元数据和一次性 `tokenEnvelope`，不返回明文 `token` |
| PUT | `/proxy-keys/{id}` | 只改 name/note/groupId/limit，不接受 status；按新 limit 重算额度状态，规则见 0.6 |
| PUT | `/proxy-keys/{id}/state` | body 只含 `status`，值为 `enabled` 或 `disabled`；执行人工启停，规则见 0.6 |
| POST | `/proxy-keys/{id}/reset-usage` | `used=0`；已物化且 `quota_exhausted` → enabled；safe 占位且 `quota_exhausted` → `disabled + manual` |
| DELETE | `/proxy-keys/{id}` | 短事务写 `tombstoned_at` 后立即返回逻辑删除成功；不等待流结束。最后一个 lease/processing Attempt 结束后再物理删行和 secret |
| POST | `/proxy-keys/{id}/rotate` | provisional 新 ref → 同事务提交 token/hash/prefix/secret_ref、移除 provisional 并登记旧 ref=`proxy_key_rotated` → 满足 ref 级清理条件后删旧明文；**不改变 status/reason**；响应返回新 `tokenEnvelope`，不返回明文 `token` |
| GET | `/proxy-keys/{id}/secret` | 返回 `tokenEnvelope`，前端解密后查看/复制 |

新建密钥必须由独立 `/state` 显式启用。safe 占位密钥轮换出真实 token 后仍保持原 `disabled` 与原 reason，不自动启用；`manual` 只由已物化密钥的 `/state` 解除；`quota_exhausted` 在额度解除时，已物化密钥恢复 enabled，占位密钥只收敛为 `disabled + manual`。手工启用时先拒绝 `pending:<id>` / 空 ref，若已物化但当前 secret 不可读取则 fail-closed，再检查 `limit > 0 && used >= limit` → 400。UI 对占位展示「请先轮换」，禁用启用按钮但服务端仍须拒绝绕过 UI 的请求；额度解除后展示人工停用，轮换仍不自动启用。哈希不得与管理令牌或通用代理令牌相同，列表密钥之间本就 UNIQUE。
通用代理令牌不出现在本列表。

`tokenEnvelope` 固定为 `{version:1, algorithm:"AES-256-GCM", ciphertext, nonce}`。密钥仍由当前管理 Bearer 经 HKDF-SHA256 派生，但使用独立 info `proxy-key-wrap-v1` 与渠道凭证做域隔离；三类响应均设置 `Cache-Control: no-store`。前端只有在创建成功、轮换成功或用户明确点击查看/复制后才解密，不把解密结果写入日志、URL 或持久状态。

### 5.3 渠道 bundle

渠道配置只有聚合 bundle 写入口，渠道行、凭证、模型、映射、探针、访问组成员和相关生命周期证据必须在同一个 module 操作中校验并原子提交：

| 方法 | 路径 | 身份规则 |
| --- | --- | --- |
| POST | `/channels/bundle` | 唯一普通新建入口；`bundle.channel.id` 缺省或空，由服务端生成可读 id 和全新 identity；非空 id 直接 400 |
| PUT | `/channels/{id}/bundle` | 唯一普通更新入口；路径 id 是唯一权威，保留原 identity；前端不得发送 `bundle.channel.id`，若兼容请求仍携带非空值则只能与路径完全相等，否则 400，且不能用它选择/新建其它行 |
| POST/PUT | `/channels`、`/channels/{id}` | 行级创建/更新固定 405，不得绕开 bundle 只写渠道行 |

- `channel_identity` 与 `channel_config_version` 都是服务端内部字段，普通管理请求、响应、bundle、配置 schema 9 和 migration v2 均禁止出现。复制无论源 id 是什么都生成新的 id + identity，并令 config version 从 1 开始。safe/complete/migration 可以携带逻辑 `id` 维持组/映射引用，但包内普通渠道的目标行 identity 全部重新生成、config version 从 1 开始，并按 2.1 在同一事务初始化健康运行态；因为整包替换先 drain 全部旧 lease，不会与旧 snapshot 并存。同实例恢复直接恢复 SQLite 中的 identity/version，并在开放请求前完成 drain/收口。任何普通创建入口都不得接受或复制调用方提供的 id/identity/version。
- `group` → `tag`。
- `groupIds: string[]`：**保存时必填且必须包含 default 组 id**。缺少或空数组 → 400。新建表单默认就是 `["default"]`。更新也强制含 default，避免编辑时把兜底组去掉。
- `historyPlaceholder` / `is_history_placeholder` 是服务端派生的只读字段，不进入 bundle 可写白名单，也不进入配置/迁移包；客户端提交该字段按未知字段拒绝，不能靠传 `false` 复活渠道。
- `dollarLimitMicros`：wire 为规范非负十进制字符串，缺省 `"0"`（不限制），解析后须在 `int64`。前端输入美元最多 6 位小数，用十进制字符串精确换算，不经过 JS `Number`。不要用保存 bundle 覆盖 `dollar_used_micros`（已用只通过请求累加和重置接口改）。
- `streamIdleTimeoutMode`：必填枚举 `inherit | override`。新建默认 `inherit`；`inherit` 使用全局 RequestPolicy，`override` 要求 `streamIdleTimeoutMs > 0`。前端以显式选择器展示“继承全局 / 渠道覆盖”，继承态可保留最近一次合法覆盖值但必须禁用该数值输入且不得把它当有效传输值；禁止用空值或 `0` 暗示继承。schema 9 与 migration v2 必须往返该枚举，旧/未知字段或 `override + 非正数` 在 staging 前 400。
- 渠道凭证有变化时必须通过 `SecretRefLifecycle` 写新 ref，并在同一 short commit 的 SQLite 事务中更新渠道行、移除新 ref 的 provisional 项、把旧 ref 记为 `channel_credential_replaced`；COMMIT 后才发布新 snapshot。旧 ref 等所有仍引用它的 Request/system/admin operation snapshot、精确 secret lease 和资源 pin 释放后再删。凭证未变化时复用原 ref，不做无意义换代。
- 普通 DELETE 只允许删除无历史引用、且该 `(channel_id, channel_identity)` 没有活跃 reservation/`AttemptLease` 的渠道，并在 coordinator 同一提交序列点和事务把旧 ref 记为 `channel_deleted`；有历史引用或活跃 attempt reservation 时返回 409，不等待、不创建占位。旧 RequestLease 仅持 snapshot 而尚未 Prepare 时可以删除；它随后因 identity 缺失/不匹配跳过，即使新建同 id 渠道也不能误记账。只有 complete/migration 整包替换可按 5.4 把历史渠道转成日志占位，其清空的 ref 统一记为 `import_superseded`。
- 复制渠道只调用一次 `CaptureAdminSecretRead(selection=channel_copy_source)`，得到完整不可变 `ChannelCopySourceSnapshot`：源名称/备注、`tag`、`groupIds`、协议、Base URL、能力、自定义请求头、人工状态、失败策略、并发/超时/优先级、fallback/reasoning/service tier、models、mappings、probe policy、`dollarLimitMicros`、敏感词开关/词表，以及同一 snapshot 的凭证元数据与可选 owned 明文。源有非空 ref 时必须同代读取有效明文，失败直接拒绝，不能误认无凭证；为目标创建独立 provisional ref，经 `channel_copy` committed batch 提交。只有合法 `admin_state=disabled && secret_ref=''` 的普通渠道可走无秘密分支：复制静态配置为 disabled + 空 ref，不创建 provisional ref/secret batch/committed 行，直接构造类型化 `ChannelBundleCommand`。两分支目标都从 used=0、`quota_blocked=0` 起步；目标名称在最终短事务按源名称和当前目录生成唯一副本名，目标 id/时间、健康运行态、in-flight、探针计数和历史占位标记不得复制。最终只有 `ChannelBundleCommitter` 可重新校验同一 Bearer、全部 `groupIds` 仍存在且包含 default 及 bundle 领域不变量；失败返回 409，有秘密分支保留生命周期清理证据并销毁 owned 明文，成功才原子写入整个目标 bundle。复制语义固定为捕获时点快照，源渠道随后编辑或删除不做 CAS，也不得把新代字段混入旧快照。列表密钥从不属于渠道，不存在随渠道复制；源快照不满足普通渠道不变量或属于日志占位时返回 409。
- `contentFilterEnabled`（默认 false）、`contentFilterWords`（string[]）。关闭时仍可保存词表，方便以后打开。非法词（过短/过长）保存时 400。
- 普通渠道的可承接不变量是 `admin_state=enabled` 时存在非空、可读取的凭证；手工启用与完整 bundle 保存不得让无凭证渠道保持 enabled。safe 导入的普通渠道仅改变人工状态为 disabled，UI 按上述派生「缺少凭证」提示重新配置；这不创建新的持久化禁用原因。`PrepareAttempt` 对无凭证普通渠道跳过，自动探针、saved 手动测试和已保存渠道模型发现均不得借 safe 导入状态发出上游调用；管理员在编辑表单补入临时凭证后，可按 `draft_channel` 规则测试或发现模型，但该操作不改变已保存渠道状态。

另：

- `POST /channels/{id}/reset-usage`：只做 `used=0`、`quota_blocked=0`；`admin_state` 与全部健康字段保持原值。
- `PUT /channels/{id}/state`、分组成员写接口、`POST /channels/{id}/copy` 和 `POST /channels/{id}/test` 遇到 `is_history_placeholder=1` 必须拒绝，不能依赖当前无凭证或无模型间接失败。删除仍按历史外键规则返回“有请求记录不能删除”；重置健康/额度即使执行也不得清标记或恢复路由。唯一转正式渠道的入口是完整 bundle 保存：服务端确认新凭证已成功写入、`groupIds` 含 default 后，在同一事务清零标记。

目录响应：`tag`、`groupIds`、`groupNames`、`dollarLimitMicros`、`dollarUsedMicros`、`quotaBlocked`、`historyPlaceholder`、`disableReason`；两个金额字段均为规范十进制字符串。

### 5.4 设置 / 导出 / 同实例恢复点 / 迁移包

- `GET/POST /auth`：保存自定义令牌保留（body 带 `token`）。UI 不调用轮换。保存时拒绝与另一固定令牌、以及任一 `proxy_keys.token_hash` 冲突（三类两两唯一）。走 3.1.2 journal。
- `LocalDefaultProxyToken = "sk-oneai-admin-proxy"`。本方案只启动全新 schema 28 数据库，不存在旧默认令牌分支。
- `GET/PUT /settings` 的 JSON **增加** `contentFilter`，读写 `app_settings.content_filter`，经 `ContentFilterSnapshot` 发布；不要改 `config.Settings` 结构体。前端 `#settings/filter`。

**只接受当前版本，不做旧包 adapter：**

- 配置导入：`schemaVersion` 必须 = **9**，否则 400。
- 迁移导入：`migrationVersion` 必须 = **2**，且 `configSchemaVersion` = **9**，且 `databaseSchemaVersion` = **28**，否则 400。三者缺一不可。
- 同实例恢复点：`manifest.schemaVersion` 必须 = **28**；**删除** manifest version 0 的兼容。

**本文后续简称的 complete 只表示解密后的完整配置内容；HTTP wire mode 必须继续叫 `complete_encrypted`。** 完整配置导出只允许 `POST` + 非空一次性口令，响应必须是现有口令加密 envelope；禁止新增 `mode=complete` 返回含 token 的明文 JSON。迁移包继续使用自身的 Argon2id + XChaCha20-Poly1305 envelope，不与配置导出的 envelope 混用。

**配置导出 schema 9**（`exportDocument` / `exportChannel` 的 `UnmarshalJSON` 与 `validateExportDocument` 必须改白名单，漏一项导入会把新字段当未知拒绝，或把旧 `group` 当必填）：

顶层 `requireFields` 在现有字段上**增加**：`accessGroups`、`proxyKeys`、`contentFilter`。`tokens` 仍仅 complete 出现。

渠道：删除必填 `group`，改为：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `tag` | 是 | 原 `group` |
| `groupIds` | 是 | 非空数组，必须包含 `default`；日志占位渠道不进入导出包 |
| `dollarLimitMicros` / `dollarUsedMicros` | 是 | 规范非负十进制字符串；解析后为 `int64` 微美元 |
| `contentFilterEnabled` | 是 | bool |
| `contentFilterWords` | 是 | string[] |
| `credential` | 按模式 | 普通渠道沿用既有凭证对象字段；safe 禁止出现（包括 `null`）；complete 解密内容可省略，但仅 `adminState=disabled` 时允许省略 |

- 导出不写 `group`、`legacyUngrouped`。手改包带任一旧字段均按未知字段拒绝。
- schema 9 普通渠道保留必填 `adminState`（仅 `enabled` / `disabled`），日志占位渠道不入包。strict parser 只负责区分 `credential` 字段不存在、显式 `null` 和对象，随后必须先按 mode 分派再做领域校验：safe 无论 `adminState` 为 enabled 或 disabled 都只接受字段不存在，出现 `null` 或对象即 400；`complete_encrypted` / migration v2 若出现则必须是合法对象、非空 secret，不接受 `null`、空字符串或只有空 secret 的对象，且只有这两种完整模式的 `adminState=enabled + credential 缺失` 才直接 400。禁止在 mode 分派前用公共的“enabled 缺凭证”检查误拒绝 safe 包。

`accessGroups[]` 必填：`id` `name` `note` `enabled` `isDefault`。
`proxyKeys[]` 只包含未 tombstone 的密钥，必填：`id` `name` `note` `prefix` `status` `disabledReason` `groupId` `dollarLimitMicros` `dollarUsedMicros`。complete 另必填 `token`；safe **禁止**出现 `token` / `token_hash` / `secret_ref` / `tombstonedAt`，手改包携带 tombstone 字段按未知字段拒绝。
所有普通渠道在配置 schema 9 / migration v2 中必填 `streamIdleTimeoutMode`；`streamIdleTimeoutMs` 仍保存正数覆盖值，但只有 mode=`override` 时决定运行超时。safe/complete/migration 不得按数值是否为 0 猜 mode。
`contentFilter` 必填：`enabled`、`words`。不要塞进 `settings`。

管理列表、摘要及 safe/complete 配置导出和 migration v2 导出的渠道/密钥额度、状态字段，必须由 3.1 的一次当前管理读取视图提供：同一短 SQLite 读事务内捕获 `used`、`limit`、`status`、`reason` 与渠道 `quota_blocked`，按同一视图生成 wire 字段；加密与读取已 pin 的秘密在事务外进行。结算刚把密钥从 `90/enabled` 改为 `110/quota_exhausted` 后开始的导出，必须同时输出 `110` 和 `disabled/quota_exhausted`；不得输出 `110 + enabled`、过期的 `90 + enabled`，也不得把动态字段重新纳入 `configDigest`。

- **safe**：含分组、成员、列表密钥元数据（上表，无明文）；所有普通渠道禁止 `credential` 字段。导入时无论包内 `adminState` 是 enabled 还是 disabled，事务写入一律 `admin_state=disabled`，保留其余合法静态配置及额度数值，不创建渠道 secret/ref；导入后的「缺少凭证」仅是目录/UI 派生展示，不依赖包内可写原因字段。
- **complete（wire mode=`complete_encrypted`）**：解密后的 `proxyKeys[].token` 是明文；导入时新 `secret_ref`、新哈希通过 `import_provisional` 写入目标密钥环。普通渠道 `credential` 可选仅指 disabled 渠道；enabled 渠道必带非空有效凭证，否则 400；disabled 且省略时写空 `secret_ref`、保持 disabled，不暗中修正其它状态。HTTP 响应和磁盘上不得出现解密后的完整 JSON。

完整配置和 migration v2 导出对普通渠道使用相同规则：enabled 渠道无 `secret_ref`，或 ref 无法读取、读得空/无效凭证时整个导出返回 409，不生成部分包或秘密降级包；disabled 渠道没有 ref 时省略 `credential`，有 ref 时必须读取并校验有效才携带，失败同样 409。safe 导出永不读取/携带渠道凭证，允许导出仅用于停用恢复的渠道静态配置。恢复点另按下文校验，不受 complete 列表密钥占位导出 409 的规则影响。

safe 导入后尚未轮换的密钥没有 `secret_ref`，因此无法生成 complete/迁移包要求的 `token`。只要存在这种占位密钥，`complete_encrypted` 配置导出或迁移导出必须整体返回 **409**，响应只列非秘密的密钥 id/name 并提示先轮换；禁止静默遗漏、输出空 token 或退化成明文。safe 导出不受影响。

safe 占位密钥**允许进入同实例恢复点并原样恢复**。它没有 secret，不进入 bundle ref 集合；恢复点不是 complete 配置导出，不因占位密钥返回 409。恢复后仍保持原 `disabled` 与原 reason；轮换只补真实 token/hash/prefix/secret_ref，仍须按独立 `/state` 或额度规则启用。

**safe 导入密钥（必须满足 DDL）：**

```
token_hash = "pending:" + 密钥 id          -- UNIQUE。hashAuthToken 是 SHA256 的 Base64（无冒号），正常 token 哈希不到 `pending:` 前缀
secret_ref = ""
status     = "disabled"
disabled_reason = 包里为 quota_exhausted 且 limit>0、used>=limit 时保留 quota_exhausted；其它合法状态统一为 "manual"
```

schema 9 的 `status` / `disabledReason` 都是必填严格字段：缺字段、未知值或不满足枚举直接 400，不用“缺省/非法值自动修正”绕过 schema 校验。safe 只把**合法但不再能原样启用**的状态按上式收敛为 disabled。导入/恢复共用的占位不变量还要求 `token_hash == "pending:" + id`、空 ref、disabled，`quota_exhausted` 仅在 `limit>0 && used>=limit` 时合法；额度解除后只能是 `manual`，不得生成 `enabled + pending`。

鉴权先查未 tombstone 的 `proxy_keys.token_hash`：正常 SHA256/Base64 哈希不以 `pending:` 开头，故占位不可鉴权。查看/复制 400「请先轮换」。轮换后写入真实 token/hash/prefix/secret_ref，**不改变 status/reason，也不自动启用**。

**导入是整包替换，不是合并。** 管理 handler 先在 coordinator 外完成限长/限时读取、解密、严格解析和包含普通渠道凭证模式规则的静态领域校验。校验必须先分派 mode：safe 只检查 `credential` 字段完全不存在，包内 enabled 渠道也允许进入后续的强制停用转换；只有 `complete_encrypted` / migration v2 的 enabled + 缺/空凭证必须在创建 secret staging 前拒绝 400，不得走旧实现的静默停用分支。再创建本次 operationId 的 `ProvisioningBatch`，并在进入维护前把 complete/migration 的新渠道/密钥 secret 全部 Put 到该 batch；safe 没有 secret batch。随后 `configreplace` 把原始管理 Bearer、prepared replacement 和 batch capability 一并交给自身持有的 `ConfigReplacementCommitter`；handler 不持有该 façade。coordinator 必须先按当前管理令牌重新鉴权并原子接管 batch，再进入 `draining`；随后新代理立即 `503 + Retry-After`，新的管理写操作返回维护中，并有时限地等待旧 `RequestLease`、全部运行 `OperationLease` 和**其他** provisioning lease，绝不能等待自己接管的 batch。drain 后重跑依赖 live 状态的校验再短提交；超时或校验失败必须退出维护、零业务提交并 abort 已接管 batch。

`app_settings` 中的 `instance.secret_binding`、`auth.token_journal`、`restore.commit_marker`、`migration.pending-secret-cleanup`，以及专用表 `provisioning_batches`，都是**本机运行状态**，不是配置/迁移包字段。配置/迁移严格解析遇到等价输入字段直接拒绝，导出白名单不得包含该表；同实例恢复点可保留普通业务设置，但必须在**快照副本**的净化事务中删除源实例的四个本机键和全部 `provisioning_batches` 行，绝不能改 live 数据库。配置/迁移导入必须在替换 `app_settings` 的同一事务中原样写回目标实例的 `instance.secret_binding`；若 restore sidecar 已处于 `cleanup_pending`，还必须在 coordinator 提交序列点和同一 SQLite 事务读取并原样保留**当时仍存在且与 sidecar 相等**的 `restore.commit_marker`，不得把 prepare 阶段缓存的 marker 写回，也不得删除、改写 sidecar 或触碰其 `oldContentDir`。cleanup worker 删除 marker 也走同一提交序列点，因此导入只会观察“marker 仍存在并保留”或“cleanup 已完成且 sidecar 即将/已经删除”，不能复活已删 marker。随后按本节和 3.1.2 写回当前操作自己的 journal / `SecretRefLifecycle` 账本，并按 2.7 插入当前 operationId 的 committed 行；恢复同样保留目标 binding，只写当前 sidecar 对应的 `restore.commit_marker`，恢复后的 `provisioning_batches` 必须为空。固定令牌保存/轮换可以在 `cleanup_pending` 期间执行，仍只服从自己的 token journal；后续启动按当前数据库 hash 与当前 live token 验证，不再要求等于恢复点 bundle。drain 后把切换前账本中的未引用孤儿和当前库全部非固定 ref 去重写入 `supersededSecretRefs`，不能让整表替换丢失待清理证据。

同一 SQLite 事务（含 `auth.tokens` 新哈希，见 3.1.2 导入顺序；事务前只写 staging/previous，不改 live）：

1. 校验：组 id 唯一；恰好一个 default 且 `id/name=default`；密钥引用的 `groupId` 必须存在或为空，否则拒绝。每个导入渠道的 `groupIds` 都必须存在、非空、引用有效组且包含 default；缺失、空数组、旧字段 `legacyUngrouped` 或不含 default 均返回 400。
   - 普通渠道走与配置/迁移 parser、恢复点共用的模式化领域校验：safe 必须没有 `credential`，并强制持久化 disabled + 空 `secret_ref`；complete/migration 的 enabled 必须带非空有效凭证，disabled 可省略凭证并保持 disabled；显式 `null`、空凭证、其它无效对象直接 400。缺失凭证不是可静默修复的 complete/migration 状态。
   - complete / 迁移：三类令牌两两 hash 唯一；`proxyKeys[].prefix` 由实际 token 重算。
   - complete / 迁移的密钥额度状态必须交叉一致：`enabled` 要求 reason 为空且 `limit=0 OR used<limit`；`disabled+quota_exhausted` 要求 `limit>0 AND used>=limit`；`disabled+manual` 不限制 used。矛盾包直接 400，禁止静默猜测。
   - safe 密钥仍强制 `disabled`；`status` / `disabledReason` 缺失或枚举非法先拒绝；仅当包内 reason=`quota_exhausted` 且 `limit>0 AND used>=limit` 时保留该 reason，其余合法状态统一为 `manual`。
   - 渠道包不携带 `quotaBlocked`。导入时在事务内由金额唯一派生：`limit>0 AND used>=limit` → 1，否则 0；不得从包外信任门闩，也不得因派生门闩改写健康字段。
2. 记下将被替换掉的旧 `proxy_keys.secret_ref` 与渠道凭证 ref，和当前 `SecretRefLifecycle` 账本合并；本次 complete/migration 新 ref 已在维护外通过同一个、现已由 coordinator 接管的 `ProvisioningBatch` 以 `import_provisional` 登记并 Put。本事务必须消费且只消费该 operationId 的 batch：同时移除已被新行引用的 provisional 项、把全部旧列表密钥/渠道 ref 记为 `import_superseded`，并在所有业务行/`auth.tokens` 已写入后计算 `canonicalRuntimeConfigProjectionV1`，插入 kind=`config_complete_import|migration_import`、精确 newRefs 和目标摘要的唯一 `provisioning_batches` 行；不能让 `DELETE FROM app_settings` 丢掉已有生命周期项。COMMIT 前失败由 coordinator `Abort()`；COMMIT 成功或该行证明已提交后进入 `database_committed`，即使固定 live/新 snapshot 发布失败也禁止删除新 ref，只能保留 journal、fail-closed 并前滚；发布完成且 snapshot `configDigest` 一致后 batch `Complete()` 条件删除该行。
3. **日志占位渠道**（仅处理新方案运行后目标数据库中的日志外键，不兼容旧版数据库）：导入包里没有、但仍被目标机 `requests` / `attempts` 引用的当前渠道不得删除。在本事务内：
   - 保留该行 `id`
   - `is_history_placeholder=1`
   - `admin_state=disabled`（人工禁用）
   - `secret_ref=''`（清空凭证）
   - 删除其 `channel_group_members`、`channel_models`、`channel_model_mappings`、`probe_policies`
   - **不加入任何访问组**，不参与路由，列表密钥看不到
   - 额度字段保留 used 数字并按其现有 limit 派生 `quota_blocked`；健康字段保持原值（无凭证 + 人工禁用仍不可路由）
   - 无任何日志引用的旧渠道才允许 `DELETE`
4. 删除子表→父表：`channel_group_members`、`proxy_keys`、`access_groups`（保留系统稍后插入的 default 以及占位渠道行）、渠道配置子表等。`ReplaceCatalogConfiguration` 必须扩到这些新表。
5. `DELETE FROM response_affinity`（避免相同 key id 串用旧会话）。占位渠道若仍有亲和行，随渠道外键 CASCADE 或本步清空均可，本计划统一本步清空。
6. 插入父表→子表：`access_groups`（含 default）、包内渠道（显式 `is_history_placeholder=0`）、`channel_group_members`、`proxy_keys`（显式 `tombstoned_at=''`）。safe/complete/migration 的包内普通渠道均生成新 identity、config version=1，并按 2.1 初始化 `channels` 健康字段和完整 `health_runtime` 行；即使目标已有同 id 行，也不得保留旧 auto_disabled、冷却、失败/探针计数、owner 或当前探针摘要。此初始化与渠道替换一起 COMMIT/回滚，不改写历史 Request/Attempt、health_events、probe_runs；步骤 3 保留而未被包替换的日志占位渠道仍保留原健康字段。占位渠道和 tombstone 密钥都不在包内，不要从包恢复成普通活动主体。
7. complete/migration：覆盖 used；列表密钥和有凭证渠道的明文走 `import_provisional`，无凭证的 disabled 渠道保持空 ref；**管理/代理令牌的 hash 在本事务写入**，明文仍在 staging，事务成功后再拷 live。safe：覆盖 used 数字，所有普通渠道写 disabled + 空 ref，密钥按上面占位 hash 写入；不改管理/代理令牌。
8. 替换事务在 COMMIT 前已经把旧渠道凭证与旧列表密钥 ref 统一以 `import_superseded` 合并进 retired 账本；发布新运行 snapshot 后，只有所有仍引用该 ref 的运行 lease 与精确 secret/resource lease 都释放、且当前库不再引用时才删除。live 认证令牌不删固定 ref。draining 导入通常已没有旧请求或 operation，但仍必须尊重并发恢复点捕获持有的 secret lease。

**日志占位渠道与后续导出：**

- 配置导出（safe / complete）**不包含**占位渠道。它们不是可迁移配置，只为保住历史请求外键。
- 迁移包同样不导出占位渠道。
- 导出、路由和 UI **只认持久字段 `is_history_placeholder`**，禁止用「无凭证 + 无访问组 + 被日志引用」等形态猜测；否则重启后无法区分占位与普通残缺渠道。
- 渠道列表 API **要展示** `historyPlaceholder=true`：名称旁标明「日志占位」；普通状态、分组、复制和测试接口必须拒绝占位渠道。编辑保存若要把占位恢复成正式渠道，必须在同一 bundle 事务中成功写入新凭证、确保 `groupIds` 含 default、由服务端把 `is_history_placeholder` 清为 0，再按提交的 `admin_state` 处理；请求不得直接写该标记。本计划不提供「一键复活」，操作员当作新渠道完整编辑。
- 后台自动探针枚举同样必须显式排除 `is_history_placeholder=1`；不能只依赖占位渠道没有 `probe_policies`，也不能为它补默认策略后发起探测。
- 日志占位渠道根本不进入配置或迁移包；schema 9 不定义任何“普通渠道无访问组”的兼容表达。

**迁移包 v2 不自动继承配置导出。** `migrationPayload.UnmarshalJSON` 用 `decodeStrictObject` + `requireFields`，多字段当未知拒绝，少字段当缺字段拒绝。必须逐项改下面清单，缺一项算没做完：

- 常量 `migrationVersion = 2`。`supportedExportSchemaVersion` 只接受 9，不要再接受 7/8。
- `migrationEnvelope` / `migrationEnvelopeAAD`：`migrationVersion=2`，`configSchemaVersion=9`，`databaseSchemaVersion=28`。三者缺一拒绝。
- `migrationPayload` 结构体与 `UnmarshalJSON` 的 `requireFields` **增加**：`accessGroups`、`proxyKeys`、`contentFilter`。渠道数组校验与 complete 解密内容同一套（`tag` / 非空且含 default 的 `groupIds` / 额度 / 敏感词 / 按 `adminState` 校验 `credential`；禁止 `group` 和 `legacyUngrouped`）。
- `proxyKeys` 按 complete 规则：每把密钥必有明文 `token`。
- `migrationPayloadFromExport`：从 `exportDocument` 拷贝上述新字段，不能只拷现有 Channels/Settings/Tokens。
- `migrationPreviewResponse` / `migrationCounts`：增加 `accessGroups`、`proxyKeys`（只计数、无明文）、`contentFilterEnabled`。
- `MigrationConfiguration` 增加：`AccessGroups`、`ChannelGroupMembers`、`ProxyKeys`、`ContentFilter`；`ReplaceMigrationConfiguration` 写入这些表/行。`IsSecretRefReferenced` 必须同时查 `channels.secret_ref` **和** `proxy_keys.secret_ref`。
- 认证令牌经 3.1.2 模块，禁止 `PersistAuthTokenSecrets`。

验收使用全新 schema 28 源实例，数据包含列表密钥、非默认组、额度、敏感词和至少两个均属于 default 的普通渠道；迁移后验证列表密钥只能打到其组内渠道。另在目标实例先制造一条被请求日志引用、但不在导入包中的新方案渠道，验证它只会转成日志占位渠道且不进入后续导出。

**同实例恢复点由 `RecoveryPointStore` 单一深模块负责。** 保留现有 `/config/backup*` 路径以减少兼容性改动，但 API 文案、UI、manifest 与日志统一使用“恢复点”；它不是可携带备份或通用灾备。业务外部只调用 `Create/List/Verify/Restore/Delete`；启动装配层按唯一清单分别调用 restore `RecoverPendingBeforeRuntime` 与 artifact `RecoverArtifactJournals`，不得自行复制文件、拼 bundle key ref 或扫描系统密钥环。

管理入口必须补齐：

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| POST | `/config/backup` | 创建恢复点；返回 committed 恢复点元数据 |
| GET | `/config/backups` | 只列 committed；不暴露临时、deleting 或 secret ref 明细 |
| POST | `/config/backup/verify` | 校验 manifest、SQLite、正文、bundle、实例绑定和领域不变量 |
| POST | `/config/backup/restore` | 显式维护状态下恢复；复核当前管理令牌与原实例绑定 |
| DELETE | `/config/backups/{backupId}` | 写 deletion journal 后逻辑隐藏并幂等删除整套资源；允许后台继续清理 |

**创建生命周期：**

1. 生成不可复用的 `backupId`，预先派生 artifact 路径与 `bundleKeyRef=backup-bundle-key:<backupId>`，把完整清理清单（包括暂存 SQLite 及其确定的 `-wal`、`-shm`、`-journal` 路径）写入持久 `.creating-<backupId>` journal/暂存目录并 fsync；任何文件或 `Store.Put` 都必须发生在 journal 发布之后。
2. 调用 `BeginRecoveryPointCapture` 重新鉴权、取得内部 operation lease，并按 3.0.1 在正文资源准入序列点先关闭新的 delete ownership、固定已先取得 deleting owner 的 predecessor 集合，立即 `defer Close()`；随后退出 coordinator 临界区。capture 不得 pin 或阻塞这些 predecessor，而是在 deadline 内等待它们把文件、行、key 和 owner journal 全部收口；只有返回 `ready` 才算 fence 建立完成。等待超时或任一删除无法安全收口时返回可重试 409 `recovery_point_capture_busy`，关闭 fence/lease 并让步骤 1 的 artifact journal 走 abort 清理，禁止继续 online backup。fence 是删除/固定 ref 换代的引用屏障，不是 mutex，不占用业务 SQLite 主连接，也不跨 predecessor 文件 I/O 持全局锁。
3. 只有 capture 已 `ready`，`Snapshotter` 才可为 live DB 路径打开独立的只读源 handle（单连接、WAL、`busy_timeout=5000`）。它必须在该源连接上开始只读事务并执行一次真实读取来固定 SQLite 源快照，随后始终用这条连接创建 modernc SQLite `NewBackup`，通过 `Backup.Step` 每次最多复制 256 页到暂存文件；分页之间检查 context 并让出调度，业务连接的后续提交不得让 backup 重启到新源版本。Step 的 BUSY/LOCKED 在总 deadline 内按既有 busy timeout 重试；完整成功顺序固定为“固定源读取快照 → 分页 Step → `Finish` → 结束只读事务 → fsync 文件和父目录”，取消、BUSY/LOCKED 持续到 deadline、超时和其它终止错误也必须先 `Finish` 已创建的 backup，再回滚/结束只读事务并关闭 handle，不得遗留长读事务。`snapshotBackupTimeout` 固定 120 秒，从打开只读事务前开始并覆盖真实读取、全部 Step、`Finish` 与事务释放；到期只中止本次 capture、进入步骤 1 artifact 的 abort 清理并返回可重试错误，以限制 WAL 保留，本阶段不增加 UI 配置。禁止在业务主 handle 上执行 `VACUUM INTO`，禁止把第二连接加入常规业务连接池。这样快照不会捕获“deleting 已删文件但尚未删行”的资源；fence 先成立后的新删除则在 Seal 前不能取得 owner。
4. 仅在暂存副本执行净化事务：删除 `instance.secret_binding`、`auth.token_journal`、`restore.commit_marker`、`migration.pending-secret-cleanup` 四个本机运行键及全部 `provisioning_batches` 行，提交后按下述“SQLite 单文件封存与读取”契约完成封存；不得修改 live 数据库。随后经统一受控只读入口读取该**已净化且封存的快照**中的渠道、全部非空 `proxy_keys.secret_ref`（含 tombstone）、`content_blobs` 和认证 hash；refs 禁止从 live DB 收集。调用 capture `Seal(snapshotRefs)`，在宽 fence 仍生效时为快照正文和每个逻辑 secret ref 的当前精确 generation 建立 pin/`SecretReadLease`，校验固定认证 secret 与快照 hash 匹配，再原子释放宽 fence；任一封存/读取/pin 失败或 hash 不符则 abort，不得发布。
5. 在 coordinator 外按快照复制被 pin 的正文，通过精确 `SecretReadLease` 把全部 secret 读入敏感内存，随后释放 secret lease，再加密 bundle。bundle ref 必须是 `backup-bundle-key:<backupId>`；SQLite、正文目录、bundle、manifest 和 key ref 全由同一 backupId 派生。
6. 只有步骤 4 单文件封存成功且文件/父目录 fsync 完成后，才计算 SQLite 最终大小/sha256；步骤 3 的中间产物 fsync 不代表封存成功，也不能用于最终 manifest。manifest 必须写入 `{instanceNamespace,dataDirectoryBinding}`、schema、backupId、相对 artifact 路径、大小、sha256 和 bundleKeyRef；bundle 的 AEAD AAD 必须绑定 version、schema、backupId 及这两个实例身份字段。逐文件 fsync 并复验大小/sha256/refs，按下述 `ValidateRestorableSnapshot(restore)` 校验启用普通渠道凭证 ref 与已构造 bundle 的可读凭证一致后，才原子发布 committed 并返回，由 `defer Close()` 解除 capture；失败保留步骤 1 已 fsync 的 creating journal 和剩余资源清单，由 `RecoverArtifactJournals` 清理，错误返回同样触发 `Close()`。
7. capture 进入 `ready` 前只允许 predecessor deleting owner 继续前滚；进入 ready 后、宽 fence Seal 前不再准入新的正文删除，Seal 为快照 refs 的精确 pin 后才允许清理未被 pin 的正文。代理网络/SSE和账本写不受文件复制时长影响。成功、失败、取消、panic/提前返回都必须经幂等 `Close()` 释放 fence/lease/pins；重启时进程内状态自然清空，只按 `.creating-*` journal 删除未发布 artifact，不把旧进程 lease 当恢复证据。

**SQLite 单文件封存与读取（由 `RecoveryPointStore` 内部统一负责）：**

- online backup 完成和净化事务提交后，暂存副本仍可能处于 WAL 模式。封存只操作 `.creating-<backupId>` 所属副本：使用独占的暂存连接，事务外执行所需 `wal_checkpoint(TRUNCATE)` 并检查完成结果，再设置并确认 `journal_mode=DELETE`、`synchronous=FULL`；关闭全部副本连接后核验 SQLite 文件头读/写版本均为 1，且没有 `-wal`、`-shm` 或 `-journal` 文件。不得直接删除未 checkpoint 的 WAL 来满足检查；模式转换、checkpoint、关闭或校验失败均不发布，由 creating journal 接管 abort。文件和父目录 fsync 完成后才标记内部封存状态并计算最终大小/hash；封存后不再以可写方式打开该副本。
- 所有封存 artifact 的 SQLite 读取（Create 收集 refs/最终领域校验、Verify、Restore 的源读取）统一走一个受控入口：先持有该资源的 create ownership 或 committed 读 lease，以文件操作核对封存状态、文件头、大小/hash 和无旁路文件，再用 `mode=ro&immutable=1` 打开并保持只读。Create 使用封存时记录的元数据，已发布恢复点使用 manifest；不得调用会设置 WAL/执行 schema migration 的普通 `storage.Open`。`immutable=1` 仅用于已证明独立且不再修改的 artifact，绝不能用于 live 数据库、preflight 原库、未封存 WAL 副本或正在改写的 restore 工作副本。
- `ValidateRestorableSnapshot` 的 integrity/FK/领域校验在该入口上执行；净化删除必须能在只读取主文件时观察到。Verify/Restore 前后原 artifact 的大小/hash **及文件集合**都不得变化，不能只以主文件 hash 未变判定只读成功。发现 WAL 文件头、旁路文件或元数据不符时直接拒绝，不就地 checkpoint/修复已发布恢复点，也不把旁路文件加入 manifest 来绕过单文件契约。restore 工作副本仍按其独立的写入/封存流程处理。
- creating journal 是所有中间 SQLite 旁路文件的唯一清理责任人；发生 abort/崩溃后先确保相关连接关闭，再精确清理该未发布副本及其登记旁路路径，不扫描或删除 live/其它 backupId 的文件。废弃整份未发布副本不要求先 checkpoint，但绝不能删 WAL 后继续发布主文件。Delete 的 deletion journal 同样登记原 artifact 的确定旁路路径，逻辑隐藏并确认无读者后一起清理；即使异常遗留旁路文件，也不得漏项。

bundle 是单份 XChaCha20-Poly1305 密文 map `{logicalRef -> bytes}`，数据密钥为随机 32 字节并由 Store 保存到精确逻辑 ref `backup-bundle-key:<backupId>`。manifest 只保存上述非秘密实例身份和 artifact 元数据，不保存 secret 明文、数据密钥或 authority 记录。ref 集合必须与恢复点 SQLite 快照中的非空渠道 ref、全部非空列表密钥 ref（含 tombstone）、全部正文 key ref、`content-master` 及两个认证 live ref完全一致；safe 占位密钥没有 ref，不进入 bundle。恢复后先收口快照中的 processing request/Attempt，并完成 committed 集合验证、运行快照发布与 finalization；持久进入 `cleanup_pending` 后才按 2.3 清理 tombstone。

**删除生命周期：**

同一 `backupId` 的 Verify/Restore/Delete 由 `RecoveryPointStore` 的资源级 operation lease 串行化，不使用全局锁。Verify/Restore 只允许从 committed 取得读 lease；Delete 无法取得独占 lease时立即 409 `recovery_point_in_use`，不等待。

1. 取得独占 lease 后先写并 fsync deletion journal，精确记录 `backupId`、manifest、SQLite 及其确定的 `-wal/-shm/-journal` 路径、正文目录、bundle 与 `bundleKeyRef`。
2. 原子把恢复点标成 deleting/不可见并释放独占 lease；此后 List 不再返回，Verify/Restore 拒绝新启动该恢复点，后台只按 journal 清理。
3. 幂等删除 SQLite 及已登记的旁路文件、正文目录、bundle、manifest，再精确删除 `backup-bundle-key:<backupId>`；不得按固定名、目录名或前缀扫描密钥环。
4. 删除失败保留 journal 和剩余资源清单，转为 pending cleanup；全部完成后再删 journal并同步父目录。
5. 启动时由唯一清单中的 `RecoverArtifactJournals` 只处理明确的 `.creating-*`、deletion journal 和自身元数据，不扫描猜测密钥环。它必须在未结束 restore 已最终化之后、监听启动之前执行：journal/version/身份、逻辑可见性或清理责任接管失败会阻止启动；已经由该持久 journal 精确接管的 artifact、目录或 `backup-bundle-key` 物理删除失败可以保持不可见并交给后台重试。列表密钥 tombstone 仍必须等 committed restore finalization 成功后才清理。

连续创建的恢复点必须拥有不同 bundleKeyRef；删除第一份不得影响第二份。恢复点只支持在 namespace、SQLite binding、规范化真实目录身份和 authority 均未变化的原路径绑定实例内回滚。原目录丢失、移动/复制到不同真实路径、authority 损坏或从空目录重建均不在本能力范围；完整目录跨机器复制到相同绝对路径同样不受支持，但当前方案没有目录外机器身份，**不保证检测或明确拒绝这种复制**。跨设备/换目录只使用带口令迁移包，且迁移包不含请求日志或正文。可携带完整灾备与机器绑定均属于未来独立范围。

`VerifyRecoveryPoint`：schema 必须 = 28；manifest 的 `instanceNamespace`、`dataDirectoryBinding` 必须分别等于当前已通过 preflight 的实例值，并作为 AEAD AAD 验证；`backupId` 与 `bundleKeyRef` 必须存在且严格相等于 `"backup-bundle-key:" + backupId`；解密 bundle 后的逻辑 ref 集合必须与恢复点 SQLite 快照引用集合完全一致。任一缺失、多余、实例不符、路径越界、大小/哈希不符、密钥不可用或解密失败都拒绝。
清单校验通过不等于业务数据可恢复。必须在创建 sidecar、写 staging secret 或 rename 目录**之前**，经上述封存 artifact 的统一受控只读入口执行共享的 `ValidateRestorableSnapshot(mode)`：`PRAGMA integrity_check` / `foreign_key_check` 通过；schema 最高且唯一当前版本为 28；本机运行键已净化；恰好一个启用的 default 组；每个渠道的 `channel_identity` 非空且全库唯一、`channel_config_version > 0`；每个 Attempt 的 config version 与 `health_attribution` 枚举合法，processing 只允许 pending/stale neutral，终态不得残留 pending；普通渠道至少含 default，日志占位渠道满足人工禁用、无凭证、无组；密钥 status/reason/limit/used、渠道 `quota_blocked` 与额度一致；规范十进制价格、usage/billing 枚举及非负金额合法；正文记录、manifest 文件和 key ref 一一对应。普通渠道 `admin_state=enabled` 必须有非空 `secret_ref`，且 bundle 中精确对应并可解出非空有效凭证；`admin_state=disabled` 可无 ref，有 ref 时同样必须匹配 bundle 且可解读。校验的是捕获时的 bundle，不依赖当前 live Store 仍保留历史 ref；创建恢复点时则从 pinned Store 读取并构建同一 bundle。日志占位只能 disabled、空 ref、无组，不能以普通渠道无凭证导入规则将其转为活动主体。失败必须在零 live 变更状态返回，不能等 `replaceFromBackup` 靠外键碰运气。

`ValidateRestorableSnapshot` 的引用校验必须按语义分类，不能把历史快照字段当当前外键：

- 当前结构引用必须存在：`channel_group_members` 的组与渠道、当前 `proxy_keys.group_id` 对应的组、`content_blobs.request_id` 对应的 Request，以及 schema 28 外键和当前业务行规定的其它关系。
- 历史快照允许悬空：终态 `requests.proxy_key_id` 不要求原密钥行仍存在；所有 `requests.access_group_id` 都是鉴权时快照，不要求原访问组仍存在。不得为了通过恢复校验补建密钥、访问组或占位对象，历史展示继续使用请求行保存的 id/name。
- processing Request 单独校验：非空 `proxy_key_id` 必须仍有对应密钥行，允许该行已 tombstone；缺行表示违反 processing 阻止物理清理的不变量，必须在切换 live 前拒绝。processing Request 的 `access_group_id/name` 仍是快照，可在分组删除后悬空；`CloseInterruptedWork` 收口后才允许后续 tombstone 物理清理。

共享领域校验必须显式携带模式，不能用一条“所有 token 都有明文”的规则混用：

- 所有模式都要求两个固定认证 hash 分别匹配 bundle/当前目标中的 admin、proxy 明文；两者和全部已物化列表密钥的正常 hash 全局两两唯一。`pending:<id>` 只允许出现在下述 safe 占位行，因格式不可能与正常 SHA256/Base64 hash 冲突，但仍受表内 UNIQUE 约束。
- `complete_config` / `migration`：每把列表密钥都必须物化，具有非空新 `secret_ref`、正常 token hash，且与解密 token 匹配；拒绝 `pending:`。
- `restore`：物化密钥按上条校验；safe 占位密钥只允许 `token_hash == "pending:" + 本行 id`、`secret_ref==""`、`status=disabled`，reason 为 manual，或为满足 `limit>0 && used>=limit` 的 quota_exhausted。额度调整/重置后若不再超限，合法形态是 `disabled + manual`；`enabled + pending` 一律拒绝。占位没有明文、不要求 secret/hash 匹配，且恢复后必须继续鉴权失败。
- `safe_import`：包内仍禁止 token/hash/ref，只能由导入事务生成上述占位形态。该模式不能被 complete/迁移直接复用。
- 渠道模式规则与列表密钥规则在同一共享领域校验 seam 内实现，配置导入、迁移导入、恢复点创建/校验复用：`safe_import` 禁止包内渠道凭证并产出普通渠道 disabled + 空 ref；`complete_config` / `migration` 的 enabled 必须带非空有效凭证、disabled 才可无凭证；`restore` 对已持久化普通渠道按 admin_state、快照 ref、bundle 和实际读取结果验证。不得另造一套只检查字段存在的校验或在 restore 时把残缺 enabled 渠道静默改成 disabled。

**恢复点 refs 只从 SQLite online backup 产物收集，禁止查 live DB。** 当前 `CreateCompleteBackup` 在业务主单连接执行 `VACUUM INTO` 后仍查询 live `content_blobs`，必须由 `Snapshotter + RecoveryPointStore.Create` 替换：先登记 capture fence，独立只读连接以真实读取固定源事务快照后分页 online backup，从产物读取 refs 并把 fence 原子收窄为精确 pin，再复制正文、读取已 pin 的 secret、加密 bundle 和计算 manifest。数据库最终大小/sha256、bundle ref 集合和 manifest 均以净化并完成单文件封存后的同一快照为准；原 artifact 的全部 SQLite 读取复用受控只读入口，不对 live/preflight 使用 immutable。

**并发规则只通过深模块接口表达：**

- handler、storage helper、正文模块、SecretStore 和恢复点模块之间不得传递裸 mutex、锁顺序或“已持锁”状态，也不得提供 `...Locked` 变体。
- 代理上游调用与 SSE 只持 `RequestLease` 活跃引用，不持 mutex；正文读写、Attempt 结算和流式等待不能阻塞整个站点取得新读锁。
- 普通 CRUD、固定令牌 Set/Rotate、密钥 tombstone、正文写入和清理均走短操作或资源级 pin，不进入全站 draining。
- 配置/迁移整包导入与同实例恢复才允许分别使用 `ConfigReplacementCommitter` / `RecoveryCommitter` 进入内部 draining 协议；prepare 和文件 I/O 在维护外完成，drain 超时零提交退出，实际目录/SQLite 切换只占用 module 内短提交窗口。
- 恢复点创建只在短临界区登记 capture fence；`Snapshotter` 在临界区外用独立只读连接和固定源读事务生成 online backup，再从快照产物收集 refs 并把宽 fence 原子收窄为精确正文 pin/`SecretReadLease`。固定 ref 在精确 lease 释放前保留旧 generation，清理遇到 pin 或活跃 tombstone 主体时跳过并幂等重试。恢复点删除只操作其已隐藏资源和精确 bundleKeyRef，不等待代理流。
- 管理请求提交时始终按当前令牌重新鉴权；新增路径必须归类为 read、short commit 或 draining commit，不能默认绕过 coordinator。

**同实例恢复必须有外层 restore journal，且不得放进会被 `replaceFromBackup` 整表替换的 SQLite。** 当前接缝：`replaceFromBackup` / `RestoreDatabaseSnapshot` 都是先 `DELETE FROM app_settings` 再从恢复点 `INSERT`（`internal/storage/backup.go`）。把 restore journal 放在 `app_settings`，SQLite 一切换、认证 live / `content-master` 尚未切完就崩溃，最关键的恢复记录会被恢复点里的旧 `app_settings` 抹掉。双令牌 journal 也保护不了正文主密钥和目录切换。

权威且唯一的 restore journal 是数据目录 sidecar `{dataDirectory}/restore.journal`（`0600`）。**禁止**把 journal 写入 `app_settings` 或其它会被 ATTACH 覆盖的运行库；SQLite 只保留下文与表替换同事务的 commit marker，它不是第二份 journal。不要再发明第三种位置。

sidecar 的创建和每次更新都必须原子落盘：在同一 `{dataDirectory}` 下写 `0600` 临时文件，完整写入后 fsync 文件，用跨平台原子替换（Unix rename；Windows 等平台使用等价 replace API）发布为 `restore.journal`，再 fsync/flush 父目录；禁止原地 truncate/覆盖。删除 sidecar 后同样同步父目录。正式 sidecar 存在时只按正式文件恢复，残留临时文件在恢复收口后清理；只有首次创建留下临时文件、正式 sidecar 与 SQLite marker 都不存在时，由于协议禁止在正式 sidecar 发布前改动任何资源，可删除临时文件后正常启动，除此之外“只有临时文件”一律阻止启动。

journal 至少记录（缺一项算没做完）：

```json
{
  "opId": "uuid",
  "generation": 1,
  "phase": "staging"|"switching"|"rolled_back"|"committed"|"cleanup_pending",
  "databaseSwitched": false,
  "directoryStep": "prepared"|"old_parked"|"new_live"|"old_live_restored",
  "manifestPath": "...",
  "bundlePath": "...",
  "backupId": "...",
  "bundleKeyRef": "backup-bundle-key:<backupId>",
  "liveContentDir": ".../content",
  "oldContentDir": ".../backups/restore-old-content-<id>",
  "stagedContentDir": ".../.restore-content-<id>",
  "oldContentCleanup": "not_started"|"pending"|"done",
  "stagedDatabase": {
    "path": ".../.restore-work-<opId>.sqlite",
    "state": "building"|"ready"|"deleted",
    "sha256": "..."
  },
  "secretBindings": [
    {
      "ownerKind": "channel"|"proxy_key"|"content_blob",
      "ownerId": "...",
      "ownerIdentity": "...",
      "sourceRef": "secret_from_recovery_point",
      "stagedRef": "secret_new_for_this_instance"
    }
  ],
  "supersededSecretRefs": ["secret_old_...", "..."],
  "previousCaptured": false,
  "contentMaster": {
    "liveRef": "content-master",
    "stagingRef": "restore:<opId>:content-master-staging",
    "previousRef": "restore:<opId>:content-master-previous",
    "copiedLive": false
  },
  "auth": {
    "admin": {
      "liveRef": "auth-admin-token",
      "stagingRef": "restore:<opId>:auth-admin-staging",
      "previousRef": "restore:<opId>:auth-admin-previous",
      "copiedLive": false
    },
    "proxy": {
      "liveRef": "auth-proxy-token",
      "stagingRef": "restore:<opId>:auth-proxy-staging",
      "previousRef": "restore:<opId>:auth-proxy-previous",
      "copiedLive": false
    }
  }
}
```

`generation` 与 `opId` 同时生成，取密码学随机正整数；它只参与 sidecar/SQLite marker 的全字段相等校验，不依赖一个容易被恢复覆盖的自增计数。禁止未完成恢复上再叠第二次，因此不存在两个活跃代次的排序问题。`databaseSwitched` 只是 sidecar 中的进度缓存，**不能单独证明 SQLite 是否已经 COMMIT**；权威证据是下面与表替换同一事务写入的 commit marker。`directoryStep` 是可恢复进度，不可只作内存提示；路径组合仍是校验依据。`oldContentCleanup` 在 `live → oldContentDir` 成功并 fsync 后随 `directoryStep=old_parked` 同次写为 `pending`；幂等删除且同步父目录成功后写 `done`，此前为 `not_started`。`phase=committed` 仍属于恢复最终化阶段：在线恢复必须保持 draining；冷启动则尚未绑定监听器，保持启动独占。两条路径都必须完成完整新集合验证、中断账本收口、非正文残留删除或持久转交，并成功发布**本进程当前运行快照**后，才允许原子写成 `phase=cleanup_pending`。冷启动不能因为 runtime 尚未装配而提前越过该门槛，也不能要求 pre-runtime 恢复函数完成一个当时不存在的快照发布。后者表示恢复语义已经完成，sidecar 只拥有精确 `oldContentDir` 与恢复工作副本的垃圾清理责任，live 业务集合从此允许正常演进，不再要求保持恢复点原貌。`rolled_back` 表示 marker 不存在，旧数据库、旧正文 live 和旧固定 secret 已重新验证为完整旧集合；只有进入该阶段后才能删除最后一个 staged 目录/secret。`previousCaptured=true` 表示三个 opId previousRef 都已成功写入并逐一读回验证；只有该值为 true 才能持久化 `phase=switching`。`copiedLive=true` 表示对应固定 ref 已从 staging 覆盖 live。

`secretBindings` 是非固定 secret 转换的唯一持久证据，禁止再并存一个只有 ref 集合的 `stagedSecretRefs` 作为第二权威。它按固定 kind 顺序 `channel → proxy_key → content_blob`，再按 `(ownerId, ownerIdentity)` 字节序排序且无重复：`channel` 的 owner 固定为恢复点行的 `channels.id + channel_identity`，`proxy_key` 与 `content_blob` 分别使用其稳定主键且 `ownerIdentity=""`。每个 `sourceRef` 必须恰好等于只读恢复点 SQLite 对应行的 ref、在解密 bundle 中恰有一项；每个 `stagedRef` 全局唯一并由本次 opId 新建。三张表仍遵守 ref 全局单一所有权，因此 source/staged ref 均不得跨 binding 复用。binding 的 `stagedRef` 集合就是本次新库非固定 ref 集合；`supersededSecretRefs` 是切库前从当前 SQLite 收集的旧 ref，两者必须去重且互不相交。缺 binding、owner/source 不符、重复或未知 ownerKind 一律 fail-closed，不能按数组位置或 ref 前缀猜测归属。

sidecar 首次以 `stagedDatabase.state=building, secretBindings=[]` 发布是唯一允许的空映射过渡态，此时协议保证尚未生成或写入任何 stagedRef。恢复模块随后一次性生成并原子发布**完整** binding 数组；发布成功后数组内容不可增删改，才允许第一个 `Store.Put`。`ready|switching|committed` 必须要求 binding 与源三表所有非空 ref 完全双射；空数组只在源三表确实没有非固定 ref 时合法。这样恢复不需要用密钥环扫描证明“是否已经 Put”，只依赖先发布 binding、后 Put 的持久顺序。

`stagedDatabase` 是 `RecoveryPointStore` 独占的数据库工作副本，路径必须位于当前数据目录专用 restore-work 根下并绑定 opId；原 committed 恢复点的 SQLite artifact、manifest 和 bundle 始终以只读方式打开，任何阶段都不得原地改写。sidecar 首次发布即固定工作路径并记 `building`；工作副本经同目录临时文件完整复制、fsync、原子发布后，只允许恢复模块用单连接、`journal_mode=DELETE`、`synchronous=FULL` 改写，完成时关闭连接并确认没有依赖未 checkpoint 的 WAL/SHM。全部 binding 精确改写、`integrity_check` / `foreign_key_check` 与领域校验通过后，fsync 文件并把最终 sha256 与 `state=ready` 原子写回 sidecar，只有 ready 副本可进入 switching。abort/rollback 精确删除该路径；committed 可仅凭 live DB + binding + 原只读 bundle 重入，不依赖进程内 map。进入 `cleanup_pending` 前幂等删除工作副本、fsync 父目录并写 `state=deleted`；若在文件删除后、sidecar 更新前崩溃，committed 重入把 not-found 视为该清理步骤已完成，但仍须用 binding 验证 live 新集合。路径越界、ready 哈希不符、残留非空 WAL/SHM 或 cleanup 命中其它文件均阻止继续。

**SQLite commit marker（消除 COMMIT 与 sidecar fsync 之间的窗口）：**

- 保留本机运行键 `app_settings.restore.commit_marker`，值为 `{opId,generation,backupId}`。该键不是备份业务数据：创建快照、配置/迁移导出时排除；`replaceFromBackup` 从备份插入 `app_settings` 时也排除源库同名键。`replaceFromBackup` 的同一事务还必须在删除旧设置前保存目标 `instance.secret_binding`，插入备份业务设置后原样写回，再写 commit marker；源快照不得提供或覆盖 binding。
- `replaceFromBackup` 必须在删除/插入业务表的**同一个 SQLite 事务内**，于 `COMMIT` 前最后写入当前 `{opId,generation,backupId}`。事务未提交则 marker 与新库都不存在；事务已提交则 marker 与新库同时存在，不依赖之后能否更新 sidecar。
- 在线恢复和冷启动 `RecoverPendingBeforeRuntime` 都用 sidecar 的三元组查询 marker：`staging|switching|committed` 仍在恢复流程，marker 的有无/相等按提交点判定；存在但不相等一律阻止启动。`cleanup_pending + oldContentCleanup=pending` 必须仍有完全相等的 marker；目录已经删除并写成 `done` 后，marker 相等或因“先删 marker、后删 sidecar”的合法崩溃窗口而缺失都可。若 `phase=staging|switching`、sidecar 已写 `databaseSwitched=true` 却找不到 marker，也按状态矛盾阻止启动，不得相信布尔值回滚。随后可把 sidecar 缓存修正并 fsync。
- 在线正常完成顺序：原子写 `committed` → 在 draining 中按恢复点清单验证完整新集合、执行 `CloseInterruptedWork`、发布当前运行快照并转交所有非正文残留 → 原子写 `cleanup_pending` → 运行 `CleanupTombstonedKeys`（物理 secret 删除须先交给 retired 账本）→ 尝试幂等删除 sidecar 精确记录且经路径校验的 `oldContentDir`、fsync 父目录并写 `oldContentCleanup=done` → 删除 SQLite marker → 最后删除 sidecar并同步父目录。冷启动遇到 `committed` 时按下文拆成 pre-runtime 恢复和 runtime 发布后 finalization，禁止直接套用在线路径假装快照已发布。旧目录删除失败时保留 cleanup_pending sidecar + marker 和 `pending`，不得只写日志或失去 owner；恢复已经逻辑完成，可开放服务并允许普通请求、配置导入和令牌修改，只有新的 Restore 固定返回 409 `restore_cleanup_pending`。配置整包替换必须原样保留 marker，任何操作不得覆盖 sidecar 或旧目录。后台和下次启动只继续该精确垃圾清理，不得重放恢复、回滚当前业务状态或再用旧 manifest 校验 live。这样任一点崩溃都有唯一解释，也不会连续恢复累积旧目录。无 sidecar但仍有 marker 表示 journal 丢失或异常中断，必须阻止启动，不得当作普通完成；`storage.Open` 后的恢复检查即使没有 sidecar也必须查询 marker。

**content-master 必须与认证令牌一样有 opId 隔离的 previous + staging。** 当前 `getContentMasterKey` 只认固定 `content-master`（`internal/storage/content.go`）。恢复不能复用普通 Set/Rotate 的固定 staging/previous ref，否则锁外 prepare 会与令牌修改 journal 互相覆盖。每次恢复的暂存与 previous ref 都由 sidecar 的 `opId` 派生；鉴权/读正文只认固定 live。

固定 live ref 仍是 `content-master`、`auth-admin-token`、`auth-proxy-token`；恢复专用 previous/staging ref 形如 `restore:<opId>:<slot>-previous|staging`，只由 `RecoveryPointStore` 使用并完整记录在 sidecar。

恢复分成锁外 prepare 与 `RecoveryPointStore` 持有的 `RecoveryCommitter` 类型化 draining 两阶段。调用方不持锁；解密、静态校验、opId staging secret 和正文复制在维护外完成。**当前 live previous、`supersededSecretRefs` 和依赖 live 的最终校验只能在 re-auth 成功、drain 完成后捕获**，否则 prepare 期间的新请求或令牌修改会让回滚集合过期。真正提交按以下顺序执行：

1. 先检查已有未完成 restore journal 或尚未收口的固定令牌 journal，存在时先走各自恢复规则，禁止叠两次恢复或覆盖另一操作的恢复材料。然后校验清单、原数据目录实例绑定、解密 bundle，并完成 `ValidateRestorableSnapshot(restore)` 全部领域校验。若当前 `liveContentDir` 不存在，必须**在发布 staging sidecar 前**确认当前 live 库 `content_blobs` 无正文引用，再创建并 fsync 空目录和其父目录；有引用却缺目录视为损坏并拒绝恢复，不得猜测补齐。若创建后、sidecar 发布前崩溃，留下的空目录不属于恢复操作，重启按正常旧库验证即可。以上完成后才写 sidecar journal `phase=staging`，`databaseSwitched=false`，`previousCaptured=false`，`copiedLive=false`。
2. `phase=staging`（coordinator 外，**禁止读取/保存当前 live previous，禁止改任何 live 固定 ref，禁止切库**）：
   - bundle 里的备份主密钥和两个认证令牌分别写入本次 `opId` 的 stagingRef；不得使用普通令牌 journal 的固定 staging ref。
   - 从只读恢复点 SQLite 按 `channels(id,channel_identity)`、`proxy_keys(id)`、`content_blobs(id)` 的固定顺序枚举每个非空 ref，与 bundle 做一一核对；一次性为全部 owner 调用 `secret.NewRef()` 预分配新 ref，形成规范 `secretBindings`，并在任何 `Store.Put` 前随 sidecar 原子 fsync。生成 ref 没有外部副作用；sidecar 发布失败则丢弃内存结果，绝不能先 Put 再补 binding。
   - 在 sidecar 已固定的 `stagedDatabase.path` 创建恢复点 SQLite artifact 的独立工作副本；原 artifact 只读。对每条 binding，以 `sourceRef` 从 bundle 取明文：若 `stagedRef` 不存在则 `Store.Put` 后立即 `Get` 并在敏感内存中按类型验证值相等/格式有效；已存在则只允许其值与 source bundle 项完全匹配，不得覆盖不一致值。随后在工作副本中按 ownerKind 和稳定主键执行 `WHERE 当前 ref=sourceRef` 的条件更新，渠道还必须匹配 `channel_identity`，每条严格命中一行。不得按映射数组位置、当前 live 数据库或 ref 前缀改写。
   - 全部更新后，从工作副本反向枚举三张表：每个 binding 的 owner 必须只引用自己的 `stagedRef`，所有应改写的 sourceRef 均消失，stagedRef 集合与 binding 完全相等；再执行完整领域校验、`integrity_check` 与 `foreign_key_check`。通过后 fsync 工作副本及父目录、计算 sha256，并把 `stagedDatabase.state=ready + sha256` 原子写入 sidecar。失败保持 staging，由 abort 依据 binding 精确删除 staged ref、工作副本和 staged 正文。
   - 正文密文复制到 `stagedContentDir`；记下 `liveContentDir` / `oldContentDir`。全部文件写完后 fsync 文件和目录，并按 manifest 重新校验文件集合、大小和 sha256，校验通过才允许进入 `switching`。`liveContentDir` 在 sidecar 发布前已经持久存在，staging 阶段不得才补建；若此时意外消失，按状态矛盾 fail-closed。
3. 调用 `RecoveryCommitter`：coordinator 先验证 capability 确由当前 `RecoveryPointStore` prepared restore 产生，再按当前管理令牌 re-auth、进入 draining，并在固定 30 秒内等待已有全部 `RequestLease`、全部 `OperationLease`（明确包括 system/admin/content 与 `RecoveryPointCapture` 内部 lease）及 `SecretRefLifecycle` provisional operation lease。任一 lease 未释放就超时退出 draining、保持零提交并恢复接流。drain 完成后，先在旧 live 尚未移动时调用 `ContentResourceStore.RecoverPending`，要求全部正文 creating/deleting journal 已按当前库精确前滚或清理且目录为空；失败保持 staging、退出维护且零恢复提交。随后重新校验恢复点/暂存资源；从**此刻当前库**收集渠道、列表密钥、正文 ref，并合并生命周期账本中未引用的孤儿，完整写入 `supersededSecretRefs` 并 fsync sidecar。随后把三个当前 live 固定 secret 分别写入本次 opId 的 previousRef 并逐一读回验证；只有 previous 全部有效且正文 journal 仍为空，才把 `previousCaptured=true` 与 `phase=switching` 在一次 sidecar 原子替换中发布。缺任一 current live、写入/读回 previous 失败或 sidecar 发布失败，都退出维护并走与启动共用的 staging abort；此时不得改任何 live、目录或 SQLite。
4. 已持久化 `phase=switching` 且 `previousCaptured=true` 后，严格按下列顺序执行；每完成一步立刻把对应标志写入 sidecar（fsync）再做下一步：
   1. 正文目录：`live → oldContentDir`，fsync 源、目标两个父目录（相同则一次），同一次 sidecar 原子替换写 `directoryStep=old_parked`、`oldContentCleanup=pending` 并 fsync；再 `stagedContentDir → live`，同样 fsync 源、目标父目录，sidecar `directoryStep=new_live` 并 fsync。失败按下面目录状态机回滚，不能直接用非空 old 覆盖非空 live。
   2. `replaceFromBackup` 只能读取 sidecar 指定且 `state=ready`、sha256 匹配的工作副本（表清单含 `access_groups` / `channel_group_members` / `proxy_keys`，`INSERT` 写显式列，引用已是 binding 的 stagedRef），绝不能直接读取或改写原恢复点 artifact。同一事务最后写入 `app_settings.restore.commit_marker` 后 COMMIT。COMMIT 后再把 sidecar `databaseSwitched=true`；即使这次 sidecar 写入前崩溃，启动也能由 marker 确认新库。
   3. **仅当 commit marker 与 sidecar 三元组一致**才允许把 opId staging 覆盖三个固定 live，并逐槽持久化 `copiedLive=true`。
   4. marker 不存在时禁止覆盖 `content-master` 或认证 live。
5. `phase=committed`（在线路径）：先原子写 sidecar committed；在删除任何回滚资源前，按 `secretBindings` 逐 owner 校验新 SQLite 的 channel identity/稳定主键、当前 stagedRef 与 bundle sourceRef 的对应明文，再校验正文 manifest、固定 live secret 和其余领域不变量，证明它们是一套完整新集合。只验证“所有 stagedRef 可读”不算通过；把两个 owner 的 stagedRef 交换，即使两者都可读，也必须因 owner/source/bundle 不匹配而失败。**仍在 draining 时对新库调用 2.4.1 的 `CloseInterruptedWork`**，同时收口备份里 processing Request/Attempt、按其请求开始小时标脏恢复点已有的统计聚合，并清失去执行者的半开 claim/owner；此步失败保留 restore journal 与 marker、fail-closed。然后删除本 opId 的 fixed previous/staging，或把删除失败精确转交 SecretStore authority 的持久清理状态；仅删除 `supersededSecretRefs` 中已确认不再被 `channels`、`proxy_keys`、`content_blobs` 引用、没有运行 lease/`SecretReadLease`/资源 pin 的 ref，删除失败的旧 ref 以 `restore_superseded` 写入新库 `SecretRefLifecycle` retired 账本，保留新库仍由 binding 引用的 stagedRef。引用检查必须覆盖这三张表。**此阶段不得调用 `CleanupTombstonedKeys`：恢复点中仍存在的 tombstone 密钥必须保留到 finalization 成功并持久进入 `cleanup_pending`，否则下次 committed 重入无法区分合法清理与恢复集合损坏。**随后同步发布 `s.auth`、运行设置、`ContentFilterSnapshot`、内存 content-master 缓存及完整 `RuntimeSnapshot`；刷新失败保留 `committed` journal 并**立即停止代理与管理写接口**，不得继续用旧内存接请求。上述全部成功后，先幂等删除 sidecar 精确记录的工作副本并 fsync 父目录、写 `stagedDatabase.state=deleted`，再仍在 draining 中原子写 `phase=cleanup_pending, oldContentCleanup=pending` 并 fsync；这是允许开放请求的持久线性化点。若在写 committed 后、发布快照前崩溃，冷启动必须走下面的 pre-runtime + post-publication 两段式最终化，不能由本步骤假定旧进程的 snapshot 已发布。
6. `phase=cleanup_pending`：恢复语义已经完成，`stagedDatabase.state` 必须已为 `deleted` 且工作路径/WAL/SHM 均不存在；其它状态或路径异常 fail-closed。“文件已删、sidecar 尚未写 deleted”的合法窗口仍属于 committed，只能先按 committed 重入补写，不能提前解释为 cleanup_pending。在线路径仍在 draining、冷启动仍未监听时，先按 2.3 运行 `CleanupTombstonedKeys`；其数据库/引用/账本接管失败保持 fail-closed，物理 secret 删除已持久转交 retired 账本后可后台重试。tombstone 已不在认证索引和 `RuntimeConfigProjection` 中，因此无需为物理删行重发刚发布的 snapshot。随后退出 draining/继续启动并允许当前业务状态正常前进，再按路径安全规则清理旧目录。只验证规范化 `oldContentDir` 位于当前数据目录专用 restore-old 根下、包含本 opId，且不等于/不包含数据根、当前 live 或 staged 路径；删除成功或 not-found 后 fsync 父目录并原子写 `oldContentCleanup=done`，再删 marker、删 sidecar并 fsync sidecar 父目录。删除失败只保留 `pending` 并记录脱敏诊断，不影响已验证的新集合继续服务，但所有新 Restore 返回 409；后台/重启续作。此阶段不得再按恢复点 manifest、原 fixed secret、原 ref 集合或旧 configDigest 校验当前 live，不得再次运行恢复替换或回滚；正常启动仍按**当前数据库**依次执行 fixed token、provisioning、secret lifecycle、content 与账本的一般校验/恢复。禁止在进入 cleanup_pending 前开放服务，也禁止在完整新集合校验前删除 previous、old 或 marker。

**启动顺序锁定**（改 `cmd/oneai-proxy/main.go`，漏一步算没做完）。当前是 `storage.Open` → `LoadRuntimeSettings` → `LoadOrCreateAuthTokens` → `server.New`。先在实例锁内按第 2 节执行隔离副本 preflight，判定空数据库或当前 schema 28：空数据库总是生成新的 namespace 文件，当前 schema 28 则使用同次副本返回的 SQLite binding，与文件 binding 和当前规范化目录身份比较；三方通过后才构造 secret Store。已有库的 binding 缺失或不匹配必须在接触密钥环前失败。未完成的恢复必须在加载设置和认证之前修完，否则会用备份里的旧 hash 去拉已经被覆盖或尚未覆盖的 live 令牌，或用错误的 content-master 读正文。

```
instance.Acquire
storage.PreflightDataDirectory    -- 按第 2 节隔离副本判定 empty/current 并返回 binding；原数据库资产零写入
secret.LoadOrCreateInstanceNamespace -- 仅 empty 可创建；current 必须已存在且合法
secret.NewResilientStore(namespace)  -- 逻辑 ref 命名空间 + 持久权威后端
secretStore.RecoverPendingAuthority -- 收口 prepared/committed/delete generation journal
storage.Open
restoreStartup := recoveryPoints.RecoverPendingBeforeRuntime -- 读 sidecar；补齐/回滚持久集合；committed 返回待最终化 capability
storage.CloseInterruptedWork      -- 单事务按 2.4.1 收口 processing Attempt/Request、标脏受影响小时及同时清失主半开 claim/owner
provisioningBatches.RecoverPending -- 校验 committed row/digest/newRefs 后完成前滚并清 journal
secretRefLifecycle.RecoverPending -- 当前 DB 引用 + 无进程 lease 下续清 provisional/retired
contentResources.RecoverPending   -- 按 kind 收口正文 journal：creating 以业务行判提交，deleting 只前滚删除
storage.EnsureContentMasterKey    -- 无正文引用时可初始化；有引用但缺失则拒绝
storage.LoadRuntimeSettings
storage.LoadOrCreateAuthTokens    -- 走 3.1.2 启动修复
runtimecoordinator.New
publication := runtimecoordinator.PublishInitialSnapshot -- 从当前 DB/settings/auth/content 构建并发布，返回 coordinator 私有 receipt，不绑定监听器
runtimecoordinator.FinalizePendingRestore(publication, restoreStartup.PendingFinalization(), recoveryPoints) -- nil/no-op 或 committed→cleanup_pending；可启动精确 old 清理
recoveryPoints.RecoverArtifactJournals -- 收口恢复点 .creating/deletion artifact journal；逻辑状态失败闭锁，已接管物理清理可后台重试
storage.CleanupTombstonedKeys     -- 仅在 FinalizePendingRestore 成功后运行；物理 secret 删除须先有 retired 账本接管
server.New(runtimecoordinator)
server.Run                         -- 只有以上全部成功后才绑定监听器/启动后台任务
```

上表是阶段 13 **唯一权威启动清单**；文件映射、工作包和实现不得维护第二套不同顺序。任何未被专用持久 journal 接管的失败都阻止监听器启动，后续步骤不得运行。`CleanupTombstonedKeys` 只能在 finalization 之后运行：物理 secret 删除失败仅在 retired 账本已持久接管后交给后台重试；数据库事务、引用校验或账本接管失败仍阻止启动。该清理只删除已从认证索引和 `RuntimeConfigProjection` 排除的 tombstone，不要求重发初始 snapshot，也不得把已进入 `cleanup_pending` 的恢复重新按旧 manifest 判坏。特别是 `CloseInterruptedWork` 并非现有能力，必须按 2.4.1 新建；`ProvisioningBatchJournal.RecoverPending` 必须先于 lifecycle 清理；`SecretRefLifecycle.RecoverPending` 必须先于 content/tombstone cleanup；恢复点 `.creating-*` / deletion artifact journal 必须在 restore finalization 后由 `RecoveryPointStore.RecoverArtifactJournals` 显式收口，不能隐藏在 `RecoverPendingBeforeRuntime` 或后台任务里。初始 snapshot 的“发布”只是在尚未监听的 coordinator 内建立当前代次，不代表请求已经可进入；`RuntimeCoordinator.FinalizePendingRestore` 成功后才能收口恢复点 artifact、开始 tombstone cleanup，再构造/运行监听器。

`RecoverPendingBeforeRuntime` 与 coordinator 门面触发的 `RecoveryPointStore.FinalizeCommittedStartup` 组成一条两段式启动协议，不是两套恢复状态机。类型归属必须固定：`PendingRestoreStartupFinalization` 是 `internal/storage` 拥有且无公开构造器的一次性 capability；`RuntimePublicationReceipt` 是 `internal/runtimecoordinator` 拥有、字段不导出且无公开构造器的进程内 receipt。`storage` **不得**导入 `runtimecoordinator`；`RuntimeCoordinator.FinalizePendingRestore` 先验证自己签发的 receipt 仍对应当前已发布初始 snapshot，再通过只含 capability、规范化 digest 和发布 generation 的窄 storage 接口调用 finalizer。普通调用方不得直接拿字符串或自造结构绕过该门面：

1. pre-runtime 阶段在实例锁已持有、监听器和后台任务均未启动时执行。`staging` / `switching` / `rolled_back` 按既有规则回旧或补齐持久新集合；`cleanup_pending` 只校验安全路径并可尝试精确旧目录清理。此时没有并发配置导入，删除相等 marker 可依赖启动独占直接短事务完成，不需要伪造 coordinator 提交序列点；删除失败返回显式 cleanup-pending 状态，待监听前初始化完成后交给后台重试。
2. 遇到 `committed` 时，pre-runtime 阶段必须验证 marker、manifest、原只读 bundle 以及 sidecar 的规范 `secretBindings`：逐 owner 证明当前库引用的 stagedRef 对应 bundle 中该 owner 的 sourceRef 明文，不能只比较两个 ref 集合；补齐固定 live，删除或把非正文残留转交持久 journal，并计算当前稳定 `expectedConfigDigest`，但**保持 sidecar 为 committed**。工作副本仍在则校验 path/hash 后可删除，不在则只有 marker 相等且 live binding 校验完整时才视为 committed 清理步骤已完成；原恢复点 artifact 始终只读。该阶段返回不可复制、只能成功消费一次的 `PendingRestoreStartupFinalization`，其内部绑定 opId、generation、backupId 与 expectedConfigDigest，但这些字段不作为普通调用 API 暴露。此阶段不得发布 snapshot、不得写 cleanup_pending，也不得删除 marker/sidecar。
3. 随后的唯一启动清单照常执行幂等 `CloseInterruptedWork`、各 journal 收口、当前 settings/auth/content 加载。`RuntimeCoordinator.PublishInitialSnapshot` 从这些当前值构建并发布初始代次，成功后返回仅 coordinator 能构造和验证的 `RuntimePublicationReceipt`，内部绑定 configDigest 与发布 generation；失败则保留 committed sidecar/marker并终止启动。
4. `RuntimeCoordinator.FinalizePendingRestore(receipt, capability, finalizer)` 先确认 receipt 属于当前 coordinator 且仍对应当前初始 snapshot，再把已验证的 digest/generation 交给 `RecoveryPointStore.FinalizeCommittedStartup`。storage finalizer 重新读取并要求 capability 未消费、sidecar 仍为同一 committed、marker 三元组相等、当前稳定 projection 与 capability/已验证发布元数据的 digest 一致、没有 processing Request/Attempt 或残留半开 claim/owner，并已按精确路径删除工作副本、fsync 父目录且持久写入 `stagedDatabase.state=deleted`；满足后才消费 capability，原子写 `cleanup_pending` 并 fsync。**只有这个持久线性化点成功后，启动装配层才可调用 `CleanupTombstonedKeys`；物理 secret 删除失败必须先持久转交 retired 账本，数据库/引用/账本失败仍 fail-closed，但都不得把已完成的 finalization 回滚或重新按旧恢复清单校验。**随后可尝试精确 old 目录清理；失败只保留 cleanup pending 并允许在监听启动后后台重试。receipt 不匹配、capability 重复消费或任一复核失败都 fail-closed，不能提前开放端口。
5. 若进程在初始 snapshot 发布后、写 cleanup_pending 前崩溃，发布只存在于旧进程内；下次冷启动仍看到 committed，从第 2 步重建 capability 和全新初始 snapshot。若写 cleanup_pending 后崩溃，则只走垃圾清理，不再重放恢复。在线恢复不使用这个冷启动 capability，继续按上文在 draining 中原子发布并转相位。

`ProvisioningBatchJournal.RecoverPending` 只处理 `state=database_committed` 的唯一行。它严格校验 version/kind/operationId/canonical newRefs，使用与运行快照 loader 相同的 `canonicalRuntimeConfigProjectionV1` 从当前数据库重算 digest，并确认每个 newRef 恰好被 `channels.secret_ref` 或 `proxy_keys.secret_ref` 的当前行引用、对应 `Store.Get` 可读且没有跨行复用；该 journal 不接受正文 ref。动态 usage/健康字段可保留崩溃前最后一次成功结算值，不参与摘要。全部一致时，当前数据库就是唯一可前滚状态：启动过程尚未加载任何旧运行 snapshot，因此模块可按 operationId 条件删除 committed 行，随后正常从当前数据库加载包括动态字段在内的设置、认证和初始 snapshot。删除前崩溃会在下次重做，删除后崩溃则 journal 已完成且数据库仍是同一合法目标，二者都幂等。摘要、引用、secret 或条件删除任一不一致时保留行和全部 secret 并阻止启动；不得转入 `Abort()`。固定令牌 live 前滚继续由正交的 `auth.token_journal` 在 `LoadOrCreateAuthTokens` 收口，任一 journal 失败都不得启动监听器。

`EnsureContentMasterKey` 必须读取固定 `content-master` 并校验恰好 32 字节。仅当密钥不存在且当前库 `content_blobs` 为 0 时，才生成并持久化新主密钥；若已有任一正文引用却缺主密钥，必须阻止启动。该步骤让从未产生正文的全新实例也能立即创建同实例恢复点或执行首次迁移导入，同时不会为损坏的正文库猜测补钥。

`RecoverPendingBeforeRuntime(database, secrets, dataDirectory) -> StartupRestoreRecovery` 以 sidecar 描述操作，以 SQLite commit marker 判定 DB 是否提交。它必须先于正文 creating/deleting、恢复点 artifact journal 和 tombstone 清理，因为恢复可能整体替换数据库及 ref 集合；正文 journal 在新旧数据库确定后收口，恢复点 artifact journal 在 committed restore finalization 后收口。它不依赖尚未装配的 coordinator/server，也不负责发布运行快照：

- 无 sidecar且无 marker：继续启动。无 sidecar但有 marker：阻止启动，报告 opId/generation；不得猜测固定 secret 是否已经切换。
- `staging` 且 marker 不存在：协议保证尚未改 live、正文目录或 SQLite；`directoryStep` 必须仍为 `prepared`，允许正常的 `live + staged`、无 `old`。首次无正文实例也已在 sidecar 发布前 fsync 空 live 及父目录；sidecar 刚落盘就中断时仍按本行安全终止 staging。精确删除 sidecar 中三个 opId stagingRef、存在的三个 opId previousRef、每条 `secretBindings.stagedRef`、`stagedDatabase.path` 和 `stagedContentDir`；ref/file not-found 幂等成功，但任何路径/owner 越界都 fail-closed，不得删除 `sourceRef` 或 `supersededSecretRefs`。任一 `copiedLive=true`、`databaseSwitched=true`、`directoryStep!=prepared`、出现 `oldContentDir` 或 live 缺失都属于矛盾，阻止启动，禁止假设 previous 已捕获后盲目覆盖 live。其余情况清 sidecar，得到完整旧集合。
- `switching` 且 marker 不存在：必须先有 `previousCaptured=true`，否则阻止启动。数据库仍旧；按下面目录状态机恢复旧 live；固定 ref 若出现 `copiedLive=true`（本不该发生）只能从 sidecar 指定的本 opId previousRef 拷回并校验。清三个 opId staging/previous、全部 `secretBindings.stagedRef`、工作副本和 sidecar，得到完整旧集合。
- `rolled_back` 且 marker 不存在：重新验证旧数据库、旧正文 live、旧固定 secret 与当前库引用完整；只清本次 staged 目录、全部 binding stagedRef、工作副本和 temporary previous，最后删 sidecar。该阶段可无限次重入。`rolled_back` 却存在 marker，或旧集合校验失败时阻止启动。
- `staging` / `switching` 写着 `databaseSwitched=true` 但 marker 不存在：状态与收口顺序矛盾，**阻止启动**；不得按旧库自动回滚，也不得按新库继续。
- `switching` 且 marker 三元组一致：无论 sidecar 的 `databaseSwitched` 是 false/true，都以新库为准；必要时完成新正文 live，再由 staging 补齐主密钥和两个认证 live，验证持久新集合并原子写 `committed`。随后进入下一条的 pre-runtime 最终化，不能直接写 cleanup_pending。
- `committed` 残留：必须有三元组完全相等的 marker，并仍按恢复点 manifest、只读 bundle 和 `secretBindings` 逐 owner 验证完整新集合；当前行必须引用该 owner 的 stagedRef，读取值必须匹配其 sourceRef 的 bundle 项。必要时补齐固定 live，把非正文残留删除或转交持久 journal，计算当前稳定 projection digest，并返回 storage-owned `PendingRestoreStartupFinalization`。工作副本存在时只按 sidecar 精确 path/hash 删除，不存在时在 live binding 已完整验证的前提下视为删除完成；本次 restore 状态机不得改写或删除原恢复点 artifact，恢复完成后只有独立的 RecoveryPoint Delete 操作可以删除它。本函数不得调用不存在的 runtime 发布接口，不得把“函数返回”当作 snapshot 已发布，也不得在这里转 `cleanup_pending`；**不得在这里物理清理 tombstone 密钥**。`CloseInterruptedWork` 由紧随其后的权威启动步骤执行，最终复核与转相位由 coordinator 在初始 snapshot 发布后持自己的 receipt 调用 storage finalizer 完成；只有转入 `cleanup_pending` 后，启动装配层才运行可重试的 tombstone cleanup。任一步不一致都保留证据并阻止启动，不得把 committed 当垃圾清理阶段。
- `cleanup_pending` 残留：恢复操作不得重放。先要求 `stagedDatabase.state=deleted` 且精确工作路径/WAL/SHM 均不存在；其它状态、工作文件或路径矛盾均 fail-closed。`oldContentCleanup=pending` 时要求 marker 三元组相等，只做 sidecar 自身完整性、opId/路径归属和“旧目录不等于/不包含当前数据根、live、staged”的安全校验，然后调用与在线及后台重试完全相同的内部清理流程：幂等删除精确旧目录 → fsync 父目录 → 原子持久化 `oldContentCleanup=done` 并 fsync sidecar → 条件删除仍相等的 marker → 删除 sidecar 并 fsync 其父目录；不得用恢复点 manifest、binding 校验当前 live DB/正文/固定 secret/ref，也不得要求它们仍等于恢复时集合。冷启动已持有实例锁且尚无监听器/后台任务，删除 marker 可使用启动独占短事务，不需要伪造 coordinator；若 marker 已改写则 fail-closed。删除失败不回滚当前集合、不阻止普通启动，返回显式 cleanup-pending 结果，由装配层在完成后续**当前状态**启动清单后启动后台重试，同时所有新 Restore 返回 409；运行期后台删除 marker 则必须进入 coordinator 提交序列点。`oldContentCleanup=done` 时 marker 相等或因合法清理顺序已缺失均可继续删 marker/sidecar；旧目录仍存在、值与 `directoryStep` 矛盾或路径碰到当前 live/staged/数据根则阻止启动。任何入口都不得从 `pending` 跳过 done 持久化直接删除 marker。
- sidecar 损坏无法解析 → **阻止启动**。

**恢复未完成时的正文目录幂等状态机（仅适用于 `staging|switching|committed`；每次 rename 后 fsync 源、目标父目录，相同则一次）：**

| 路径存在状态 | marker 不存在（回旧） | marker 相等（补新） |
| --- | --- | --- |
| `live + staged`，无 `old` | live 是旧集合；验证后先写 `directoryStep=old_live_restored`、`phase=rolled_back`，再删 staged | 与既定顺序矛盾，阻止启动 |
| 无 `live`，`old + staged` | `old → live`，验证后先写 `old_live_restored/rolled_back`，再删 staged | `staged → live`，保留 old 到 committed |
| `live + old`，无 `staged` | live 是已切入的新目录：先 `live → staged`（若目标存在则用本次 opId 隔离名），再 `old → live`；验证后先写 `old_live_restored/rolled_back`，最后删 staged | 目录已是恢复出的新集合，保持；old 在进入 cleanup_pending 后再删 |
| 仅 `live` | `phase=rolled_back` 时复验旧集合后收口；否则只有 `directoryStep=prepared` 时视为尚未切并先写 rolled_back，其它情况阻止启动 | 仅在恢复未完成阶段要求 live 内容按清单逐文件 sha256 验证为备份集合，否则阻止启动 |
| 两者都无，或出现其它组合 | 阻止启动 | 阻止启动 |

严禁直接把非空 `oldContentDir` rename 到已经存在的非空 `liveContentDir`。恢复动作本身每完成一个 rename 也必须可再次崩溃并从上表继续。回旧时必须在删除最后一个 staged 证据之前持久化 `rolled_back`；不得恢复出“仅 live + switching/new_live”后再删除 sidecar。

进程内失败：`cleanup_pending` 之前按 SQLite commit marker 的提交判定和 `copiedLive` 走与启动相同的 abort 或补齐，不要另写一套；`databaseSwitched` 只用于诊断，不能决定新旧集合。进入 cleanup_pending 后只允许幂等垃圾清理，任何失败都不得重放恢复。

**导入失败不得只调用 `RestoreDatabaseSnapshot`，也不得自动恢复较早的同实例恢复点。** 配置/迁移导入没有正文目录切换：SQLite 事务提交前失败走 3.1.2 abort 并清理 staged 渠道/密钥 secret；事务已提交则按数据库哈希和 journal **只前滚**到完整新集合。导入前创建的同实例恢复点作为显式管理员回滚入口保留在列表中，不能在普通失败路径自动恢复并抹掉捕获后新增的请求日志或结算。

**导入/恢复成功 HTTP 响应禁止任何 token 明文**（当前 `config_api.go` / `migration_api.go` 会回显 `adminToken`/`proxyToken`，必须删掉）。响应的非秘密字段只含 `updated`、`recoveryPointId`、`requestId`、`operationId`，不得暴露恢复点物理路径。complete 配置导入、migration v2 导入和同实例恢复会立即替换两个固定令牌，因此成功响应必须另含一次性 `authTokenHandoffEnvelope`：内部只含新 admin/proxy token；由提交前已在 coordinator 复核的管理 Bearer 经 HKDF-SHA256 派生，独立 info=`auth-token-handoff-v1`，AEAD AAD 绑定响应中的 `{operationId,requestId,operation}`；响应 `Cache-Control: no-store`，日志、审计和错误都不得记录 envelope 或明文。safe 导入不改固定令牌，不返回该字段。

前端只在收到成功响应后解密 handoff：通过 0.5 的共享严格编解码入口把 `PendingAuthTokenHandoffV1{version:1,opId,adminToken,proxyToken}` 写入既有 v2 pending key `oneai-proxy-v2-pending-admin-token`，再依次写 v2 proxy token 和 v2 admin token，最后删除 pending；localStorage 与页面内认证状态全部切换后再发任何管理请求，不把值写回可见输入框、Toast、URL 或日志。页面启动若发现 pending JSON，必须先用同一入口严格解析，再用其中 admin token 调用只读当前代次鉴权探测：成功则用 pending 中的两把令牌幂等完成本地切换并删 pending；401 时再探测当前 v2 admin token，当前值有效才丢弃 pending，否则退出并要求重新输入，禁止同时轮询两把令牌。若成功响应完全丢失，服务端仍保持新令牌且旧令牌继续 401；UI 明确退出并要求输入导入来源/恢复点中的管理令牌，禁止为恢复页面会话而回滚服务端认证集合。

**同实例恢复点（SQLite + 正文 + 加密 secret bundle）：**

删除顺序（子→父）示例：`channel_group_members`、`proxy_keys`、`response_affinity`、…、`access_groups`、`channels`、`app_settings`。
插入顺序（父→子）相反。`access_groups`、`channel_group_members`、`proxy_keys` 必须进清单。每张表 `INSERT` 写显式列，禁止 `SELECT *`。恢复走 restore journal，见上。

---

## 6. 前端

样式：只用 flex，禁止 `grid` 和 `gap`。分组/密钥独立页面和 css。

金额类型统一为字符串：`api.ts` 中所有 `*Micros` 字段都是 string；美元输入保留 DOM 原始字符串，使用共享十进制 helper 校验最多 6 位小数并换算，不调用 `Number` / `parseFloat`。列表和详情从 micros 字符串精确格式化美元；超过两位小数时可以仅在展示层 half-up 到两位，但不得把展示值写回或作为后续计算输入。

### 6.1 路由与顶栏

必须同时改，漏一项算没做完：

- `web/src/App.tsx`：`parseRoute` 增加 `#groups`、`#keys`、`#settings/filter`；`settingsSection` 联合类型加上 `filter`
- `PageHeader.tsx`：`PageKey` 增加 `groups` | `keys`；顶栏顺序见 0.7
- `internal/server/server.go`：注册 `/api/admin/v1/access-groups`、`/access-groups/`、`/proxy-keys`、`/proxy-keys/`；审计动作 `access_group.*` / `proxy_key.*`
- 前端 `api.ts` 类型与 `adminFetch` 封装

窄屏（≤700px，重点验收 390px）：**不要横向滚动、不要汉堡菜单**。顶栏改为纵向 flex：第一行品牌 + 工具区；第二行菜单（`flex-wrap: wrap`，允许两行）。具体：

- `.topbar`：`flex-wrap: wrap`；取消 `.topbar-nav` 的 `position: absolute` / `left: 50%` / `transform`（绝对定位无法形成有限宽换行容器）。
- `.topbar-nav`：`position: static`；`flex: 1 1 100%`；`min-width: 0`；`max-width: 100%`；`flex-wrap: wrap`；`white-space: normal`。
- `.nav-link`：保持 `flex: 0 0 auto`。
- `.topbar-tools`：第一行右侧，不被第二行菜单遮挡。
- 禁止 `grid` / `gap`。行距用 `margin`。

390px 验收：页面无横向溢出；8 个菜单都能点到（允许分两行）；工具区可见。

### 6.2 分组页 `#groups`

- 列表：名称、启用、渠道数、密钥数。默认组：无删除、名称只读、启停只读为开。
- 非默认组编辑：名称、备注、启用；可搜索多选渠道。多选下拉只列出已经属于 default 且不是日志占位的渠道；提交其它渠道 → 409。
- **默认组编辑：名称只读；备注可改；不能停用；渠道列表只读。** 所有普通渠道都应出现在 default；不满足时按数据一致性错误展示，不提供兼容补写入口。
- 删除确认（非默认）：「该组密钥会变成无分组、不能访问渠道；渠道不会被删」。

### 6.3 密钥页 `#keys`

- 列表：名称、分组（空显示「无分组」）、已用/总额度、状态（人工停用 / 额度用尽）、前缀、查看/复制、轮换、重置已用、停用/启用、删除。
- 新建：分组**可空**；美元额度，0 或不填 = 不限制；创建成功后解密 `tokenEnvelope`，展示一次 24 位明文。
- 遮罩 / 查看 / 复制。不展示通用令牌。safe 导入的密钥：状态为停用，展示按 5.4 交叉校验后的有效 reason（实际超限才是额度用尽，否则人工停用）；查看/复制提示先轮换；原明文 401。
- 编辑名称、备注、分组、额度只调用元数据 PUT；启停按钮只调用独立 `/state`，不得为了复用表单把 status 混入保存请求。

### 6.4 渠道列表 / 编辑器

- 列「分组」→「标签」；筛选按 `tag`。访问组列展示名称（至少有 default）。**去掉「未分组」筛选项。**
- 健康列按 1.3 展示禁用原因，不能只写「自动禁用」。
- 新列：已用额度 / 总额度（微美元在 UI 显示为美元，两位小数；如 `12.35 / 100` 或 `12.35 / 不限制`）。普通渠道必须至少展示 default；日志占位渠道单独标识且不显示为普通未分组渠道。
- `historyPlaceholder=true` 的行在名称旁标明「日志占位」，禁用行级启用、加入分组、复制和测试操作；保留完整编辑入口，不得根据无凭证、无访问组等形态自行推断该标记。
- 编辑器：原分组输入改为「标签」；「访问分组」可搜索多选，**default 必选且不可取消**；「美元额度」使用十进制输入（最多 6 位小数），0 = 不限制，提交前不转 JS Number。
- 编辑器新增卡片「敏感词拦截」：Switch 默认关；`Select` `multiple` + 允许创建（输入回车打成 Tag），点 Tag 删除。关闭时词表仍可编辑。
- 保存提交 `tag` + `groupIds`（含 default）+ `dollarLimitMicros` + 敏感词字段。
- 日志占位渠道只有通过完整 bundle 保存，同时提交有效凭证并含 default 成员，后端才在同一事务清除 `is_history_placeholder`；普通行级启用或分组操作不能把它转成正式渠道。
- 列表可提供「重置已用」。

### 6.5 设置

「代理令牌」→「管理员通用代理令牌」。placeholder：`本地默认：sk-oneai-admin-proxy`。说明：可访问全部渠道；不受分组和密钥额度限制；渠道自身额度仍生效。
**删除「轮换管理令牌」「轮换代理令牌」按钮。** 改令牌 = 输入框填写新值后点保存。
前端默认代理令牌常量同步改为 `sk-oneai-admin-proxy`，并只读写 0.5 定义的 v2 localStorage key；旧 key 中即使残留 `oneai-local-proxy` 或自定义值也不得自动带入新方案。

新增侧栏「敏感词」（`#settings/filter`）：全局开关默认关 + 与渠道相同的 Tag 词表。说明：开启后**所有**代理请求先过全局词表，再过目标渠道词表（若该渠道也开了）。

设置 → 数据管理把现有“完整备份”文案统一改为「同实例恢复点」，明确显示「只支持当前 namespace 与原路径绑定实例回滚，不能用于目录丢失、换路径或跨设备灾备；跨机器同路径复制不受支持且不保证识别」。页面调用 List 展示 committed 恢复点的创建时间、大小和校验状态，提供创建、校验、恢复、删除；删除需二次确认，deleting 项立即从普通列表消失，失败只提示后台将重试，不暴露 `bundleKeyRef` 或物理路径。

### 6.6 请求页

- 原「分组」筛选改为「标签」；另加「密钥」「访问分组」。
- 详情：密钥名、访问组、本次金额、未计价标记；Anthropic 正数 cache creation 的未计价详情通过 `billingReason=anthropic_cache_write_ttl_unexpressible` 明确说明当前价格模型无法表达 TTL。若 `content_filtered`，展示拦截层（全局/渠道）和命中词。CSV 只导当前页。

---

## 7. 文件拆分

| 新增 | 职责 |
| --- | --- |
| `internal/storage/access_groups.go` | 分组 CRUD、成员替换、默认组保护 |
| `internal/storage/proxy_keys.go` | 密钥 CRUD、哈希查找、额度累加/重置 |
| `internal/server/groups_api.go` | 分组 HTTP |
| `internal/server/keys_api.go` | 密钥 HTTP 与 secret wrap |
| `internal/billing/pricing_snapshot.go` | 从目录构建不可变 `PricingSnapshot`、按确定规则解析 Attempt 单价并输出可持久化价格快照；不查询 live 目录、不负责 HTTP |
| `internal/contentfilter/engine.go` | 词表规范化、容量校验、多 owner Aho-Corasick 编译、一次 JSON 字符串遍历、context 取消与 `FilterEvaluation`；不写正则引擎 |
| `internal/configreplace/orchestrator.go` | complete/migration 的锁外 prepare、`ProvisioningBatch` staging/所有权接管、draining 单事务替换和运行快照发布；提交时在 coordinator 序列点读取并保留当时仍有效的 restore cleanup marker，不使用 prepare 缓存复活旧 marker；HTTP handler 只作适配 |
| `internal/runtimecoordinator/coordinator.go` | 不可变运行快照、只解析 key id 的认证索引、`BeginProxyRequest` 当前主体 DB 复核/lease 线性化、ref 级活跃引用、管理/系统 operation lease、管理读复核、整段日志清理 maintenance lease、按管理认证代次可取消且不占 drain lease 的运行日志订阅、自持 operation lease 且可幂等 `Close` 的 `RecoveryPointCapture`、短提交与 draining；capture 通过窄 `ContentResourceStore` seam 关闭新 delete admission并有界等待先行 deleting owner，不在 coordinator 重写资源状态机；拥有无公开构造器的 `RuntimePublicationReceipt`，冷启动初始快照发布后由 `FinalizePendingRestore` 验证 receipt 并通过窄 storage finalizer 在监听前完成 committed 最终化；由启动装配层构造，不暴露裸锁 |
| `internal/runtimecoordinator/secret_lifecycle.go` | `SecretRefLifecycle` provisional/retired 持久账本、operation lease、三表引用检查与幂等清理；固定令牌、恢复点和正文资源 journal 不进入该账本 |
| `internal/storage/content_resources.go` | `ContentResourceStore` 的正文 creating/deleting journal（含 `operationId` 与所属 `requestId`）、per-blob resource-operation capability、文件/`content_blobs`/key ref 三方提交证据和按 kind 分支的启动恢复；creating 以业务行判定提交且删除不得覆盖其未收口 owner，deleting 以前滚删除意图为准并允许资源已缺失，journal 清除必须匹配 kind+operationId；统一正文 read/delete/capture 准入、自动/手动清理的正文及父 Request 条件删除，短事务复核终态、行、journal 与 pin/fence，禁止绕过 pin 的级联删除；不把正文资源塞入通用 ref 账本 |
| `internal/runtimecoordinator/provisioning_batch.go` | `ProvisioningBatch` capability/所有权接管、`provisioning_batches` committed journal、共享 RuntimeConfigProjection 摘要、COMMIT 不确定判定、`Complete` 与启动 `RecoverPending`；不得把 committed 状态塞回 lifecycle JSON |
| `internal/server/attempt_preparer.go` | 已保存渠道候选按 identity/config version 的可承接性、旧配置健康中性分支、精确代次凭证、可取消 reservation、渠道词表与半开/并发 `AttemptLease` 提升；`draft_channel` 不进入此模块 |
| `internal/secret/instance_store.go` | namespace 文件 / SQLite / 数据目录身份绑定、authority v2 generation 状态机、不可变物理账户、精确 generation lease 与 `RecoverPendingAuthority` |
| `internal/storage/snapshotter.go` | 独立只读单连接、真实读取固定的源事务快照、modernc SQLite online backup 分页复制、120 秒总 deadline、全出口 `Finish`/事务释放与快照产物 fsync |
| `internal/storage/interrupted_work.go` | `CloseInterruptedWork` 单事务收口 processing Request/Attempt、失主半开占用、按“原 processing Request ∪ 被关闭 Attempt 的父 Request”执行 checked request 聚合和按请求开始小时去重标脏；已终态父行只修正 Attempt 派生聚合并保留原响应终态；启动与在线恢复共用的幂等语义 |
| `internal/storage/recovery_points.go` | 同实例恢复点 Create/List/Verify/Restore/Delete、restore 的 `RecoverPendingBeforeRuntime`、finalization 后的 `RecoverArtifactJournals`、capture 的 `defer Close()`、净化后 SQLite 单文件封存/受控只读打开及旁路文件精确清理、恢复点正文/secret pin、只读源 artifact + opId 工作副本、按 owner 持久化的 `secretBindings`、restore journal 与 artifact 生命周期；拥有无公开构造器的单次 `PendingRestoreStartupFinalization`，冷启动由 `RecoverPendingBeforeRuntime` 返回，待 coordinator 验证自己的初始 snapshot receipt 后，通过只传 capability + 已验证 digest/generation 的窄接口调用 `FinalizeCommittedStartup` 才转 `cleanup_pending`；随后显式收口 `.creating-*` / deletion artifact，后者只做精确旧目录/工作副本清理且不再用旧 manifest 校验已演进的 live；只依赖 storage/secret，不导入 server 或 runtimecoordinator，禁止形成包循环 |
| `web/src/auth-token-storage.ts` | v2 admin/proxy token 与 `PendingAuthTokenHandoffV1` 的唯一 localStorage 类型、严格编解码和原子 handoff 收口；`api.ts`、设置页与启动恢复只调用此 module，不各自解析 JSON |
| `web/src/pages/models/components/ModelManagementDialogs.tsx` + `modelCatalog.ts` | 四项价格以规范十进制字符串作为表单、类型和提交权威；价格输入不得经过 `InputNumber` / `Number`，空字符串是缺价、显式 `"0"` 是免费；重新编辑保持原文规范值，范围排序/最小最大值使用十进制比较 helper，不使用 `Math.min/Math.max` |
| `web/src/pages/groups/groups.tsx` + `groups.css` | 分组页 |
| `web/src/pages/keys/keys.tsx` + `keys.css` | 密钥页 |

`server.go` / `proxy.go`：`Service` 只注入按消费者定义的 `ProxyAdmission`、`AdminRead`、`AdminChannelOperations` 等窄 façade，以及 `contentfilter.Engine`、`billing` 和 `configreplace` 的必要 seam；不得注入包含全部能力的 `runtimecoordinator.Coordinator` 大接口，也不得取得通用 short/draining runner，不再拥有 `migrationBarrierMu`。入口先通过不可变认证索引轻量鉴权 Bearer/解析 keyId，再在无全局 lease 状态下限长/限时读取 body；`BeginProxyRequest` 按 keyId 查询数据库当前 tombstone/hash/manual/quota/group，在同一 coordinator 序列点登记 lease，并返回冻结目录、价格和全局执行设置的不可变 `RuntimeSnapshot + RequestLease`。全局 `ChannelSettings` / `RequestPolicy` 保存必须调用类型化设置提交 façade，不得只写设置后单独替换内存；`proxy.go` 的请求体改写/请求预算及 `proxy_transport.go` 的超时取值只能读 Request snapshot 中已经计算的 effective timeout，不能调用 live `snapshotSettings()` 或再次按 `<=0` fallback。`runtimeResponseWriter` / `auditResponseWriter` 实现 `Unwrap()`，请求体读取 helper 对设置和清除真实连接 read deadline 的错误 fail-closed。`proxy.go` 只编排“建请求账本 → 正文快照 → 全局过滤 → 选路 → PrepareAttempt → `AttemptLease.RecordAttempt` → 传输/结算”，不得绕过 lease 直接插 Attempt，也不得新增价格匹配、过滤自动机、主体准入或导入状态机实现。
`protocol.go`：usage 三态解析；invalid 粘性合并；Anthropic 流式读取 `message_start.message.usage` 并与 `message_delta.usage` 合并。
`model_catalog.go` / `models_api.go`：`ModelPricing` 改为规范十进制字符串 wire，解析时拒绝指数、负数和非有限值；目录 CRUD/适配保留，`PricingSnapshot` 构建与 Attempt 价格解析移入 `internal/billing`。`ModelManagementDialogs.tsx` / `modelCatalog.ts` 的价格表单、类型、提交、重新编辑和范围比较全部保持规范十进制字符串：不再用 `InputNumber`、`Number(value) || 0` 或 `Math.min/Math.max` 作为价格权威；清空提交缺价 `""`，只有显式输入 `"0"` 才提交免费。所有管理 HTTP 和包内 `*Micros` 同样使用规范十进制字符串，前端不经过 `Number`。
`catalog.go`：构建包含服务端 `channel_identity + channel_config_version`、显式 `stream_idle_timeout_mode` 和预计算 effective timeout 的不可变目录快照；实现共享 `ChannelExecutionProjectionV1(channel, globalChannelSettings, requestPolicy, familyModelMappings)`，bundle 更新以当前全局设置、RequestPolicy 及所属协议族映射比较 projection，全局 reasoning/service-tier 和 RequestPolicy 保存则按 2.1 在同一事务比较全部普通渠道修改前后的有效 projection，只为实际受影响渠道递增 config version 并使旧健康/owner 失效；全局 idle 只影响 inherit 渠道，mode 切换即使当前有效值相同也换代。全局映射保存按 2.1 对所属协议族普通渠道保守换代。三种入口都在提交后一次发布同代设置、映射与目录。`ResolveRoute/Target/Affinity` 均接收同一 snapshot 与 scope，不在请求中重查 live 目录；所有主体硬排除 `is_history_placeholder=1`。普通创建、复制、配置/迁移导入分别按 5.3 生成新 identity/version，编辑按 projection 决定是否递增，同实例恢复保留。
`channel_admission.go`：底层 lease 原语只对 `purpose=business` 执行 `quota_blocked` / used≥limit 门闩，并提供按 `(channel_id, channel_identity, channel_config_version)` 复核的可取消 reservation；config mismatch 只允许 3.3 规定的普通 reservation，不得领取半开 owner。`attempt_preparer.go` 是业务、探针与已保存渠道模型发现唯一准入入口，先取得精确代次凭证和原子 reservation，再执行渠道词表，通过后提升为携带凭证且提供 `RecordAttempt` 的 `AttemptLease`。该 reservation 在 processing Attempt COMMIT 前阻止同 identity 渠道删除，COMMIT 后由历史引用接管；失败/过滤/取消释放。非业务 purpose 不跑词表、忽略额度门闩且不计费，也不得清门闩；manual probe 保留版本化健康结算但不自动恢复 `auto_disabled`，automatic probe 才可按策略自动恢复，`admin_discovery` 不结算健康。`probes.go` 自动任务只从 `BeginSystemChannelOperation` 取得 snapshot/lease，手动探针和已保存渠道模型发现只从 `BeginAdminChannelOperation(saved_channel)` 进入；未保存表单测试/模型发现走 `BeginAdminChannelOperation(draft_channel)`，直接使用 owned 临时凭证，不创建 Attempt、健康、额度或持久 reservation；枚举显式排除日志占位渠道。
`health.go`：`ResetChannelHealth` 不清除 `quota_blocked`；`HealthSettleInput` 带 purpose、HTTPStatus、channel config version 和半开 claim owner；健康领取/释放必须按 `channel_identity + channel_config_version + health_version + half_open_claim_owner` 条件更新。manual probe 的 half-open 成功/失败及 healthy/degraded 失败计数沿用现有规则，但 `auto_disabled` 恢复必须额外要求 `purpose=automatic_probe`；admin discovery 不调用健康结算。config mismatch 只完成 Attempt/计费并写 `stale_config_neutral`，健康状态机 no-op；只有启动/在线恢复的 `CloseInterruptedWork` 可在无旧执行者前提下批量清除 claim。
`channel_directory.go`：`available` 要求 `quota_blocked=0`；按 0.6 分别计算 `AutoDisabled` / `Disabled` 并在各摘要内去重；返回 `historyPlaceholder`。
`backup.go` 与 `sqlite.go`：作为 `RecoveryPointStore` 的表级恢复原语，使用显式列、子→父删除；不得自行管理全局锁、online backup 或 artifact 生命周期。online backup 只由 `Snapshotter` 执行，业务主 handle 禁止 `VACUUM INTO`。
`config_api.go` / `migration_package.go`：只保留 HTTP/迁移包适配、schema 9 / migration v2 wire（含 `configSchemaVersion=9`）和独立字段白名单；完整配置 wire mode 仅 `complete_encrypted`。导入 prepare、provisional staging、ownership transfer、事务替换、handoff 结果与失败清理由 `configreplace` 深模块负责，禁止继续把 orchestration 堆进 `config_api.go`。
`catalog_api.go`：渠道持久写只注册 `POST /channels/bundle` 与 `PUT /channels/{id}/bundle`，行级 `/channels`、`/channels/{id}` POST/PUT 固定 405；新建拒绝非空 `bundle.channel.id`，更新只以路径 id 选择行，body id 若存在且不同则 400，任何入口都拒绝 identity。保存值入口构造 `saved_channel` target，表单 payload 测试/模型发现始终构造 `draft_channel` target（即使正在编辑已有渠道，也使用本次未提交值）；两者统一调用 admin channel operation，handler 不直接读 live ref 或 SecretStore。渠道复制只从一次 `CaptureAdminSecretRead(channel_copy_source)` 取得完整不可变 `ChannelCopySourceSnapshot` 与同代可选 owned 凭证，不得分次查渠道、models、mappings、probe、groupIds 或 secret；有 ref 时写新 ref 走生命周期账本，无 ref 且 disabled 时仅 short commit、不创建 secret batch，最终均复核原始 Bearer 与外部引用不变量。
`models_api.go` / `catalog_configuration.go`：全局模型映射保存调用 2.1 的协议族版本化提交，规范映射、该族普通渠道 config/health version 与 owner 失效同事务提交；`RuntimeConfigProjection` 显式包含全局映射，不得只更新 resolver 缓存。
`web/src/pages/channels/channel-editor.tsx`：新建固定 POST `/channels/bundle`，更新固定 PUT `/channels/{id}/bundle`；两种 writable bundle 都不发送 `channel.id` / `channel_identity` / `channel_config_version`，更新目标仅来自当前路由 id。成功响应的服务端 id 只用于新建后导航，不回填成下一次提交的可写身份。流空闲设置使用明确的 inherit/override 选择器；inherit 显示当前全局有效值并禁用覆盖输入，override 只接受正数，不再用 0/空值表达继承。
`logs_api.go` / `server.go` / `metrics.go` 清理：正文 GET 只从 `BeginAdminContentRead` 取得 pin 后的路径/密钥快照，已存在 deleting owner 时返回 404 `content_unavailable`；独立自动 `cleanupExpiredLogs` 和手动 `logs.cleanup` 取得覆盖整个清理调用（包括审计、运行日志和文件）的 maintenance lease；正文容量 trim 则在所属有效 RequestLease 内同步完成，不另申请顶层 lease、不清理父 Request/审计/运行日志。三者的正文删除均遵守 3.0.1，并统一通过 `ContentResourceStore.BeginDelete` 取得 per-blob owner，禁止覆盖 creating 或由外部按路径删除 journal，随后委托该模块清理正文文件、行和 key ref。只有独立自动/手动日志清理继续在同一 pin/fence 序列点按请求终态、保留期、引用和未完成 journal 条件删除父行。`CleanupLogsWithRetention` 不得再直接批量 `DELETE FROM requests`，也不能依赖 `content_blobs.request_id ON DELETE CASCADE` 清理正文；遇到活跃引用即跳过并延迟重试，不与代理或文件复制共享跨模块裸锁。
`runtime_logs.go` / `logs_api.go` / `web/src/pages/logs/logs.tsx`：运行日志 stream 通过 `BeginAdminRuntimeStream` 重新鉴权并登记可取消的认证代次订阅；令牌换代或 draining 停止后续历史/live 事件及 reset 帧，取消旧连接且不把长 SSE 算成 drain lease。写入受逐次期限限制，前端从当前令牌和游标重连，401 等待令牌更新、503 按 `Retry-After` 延迟；现有游标失效 reset 路径保留。
`cmd/oneai-proxy/main.go`：不得维护摘要版或另一套启动顺序，必须逐项装配 5.4 的唯一权威清单：`instance.Acquire → storage.PreflightDataDirectory → secret.LoadOrCreateInstanceNamespace → secret.NewResilientStore → secretStore.RecoverPendingAuthority → storage.Open → recoveryPoints.RecoverPendingBeforeRuntime → storage.CloseInterruptedWork（含半开占用）→ provisioningBatches.RecoverPending → secretRefLifecycle.RecoverPending → contentResources.RecoverPending → storage.EnsureContentMasterKey → storage.LoadRuntimeSettings → storage.LoadOrCreateAuthTokens → runtimecoordinator.New/PublishInitialSnapshot → runtimecoordinator.FinalizePendingRestore → recoveryPoints.RecoverArtifactJournals → storage.CleanupTombstonedKeys（仅 finalization 后）→ server.New/Run`。恢复集合、账本收口、初始 snapshot、finalization、恢复点 artifact 逻辑状态以及 tombstone 数据库/账本接管失败不得监听；仅已由对应持久 journal/retired 账本接管的物理删除可后台重试。在线恢复不执行第二套启动清单，但恢复后新库开放前必须复用同一 `CloseInterruptedWork` 原语；冷启动 committed 不能在 coordinator 初始快照发布前转 cleanup_pending。
`sqlite.go`：双槽 `auth.token_journal`（含 previousRef）；提交前回滚、提交后只前滚；导入时 `auth.tokens` 与目录同一事务；`RestoreDatabaseSnapshot` 不得单独作为导入回滚。
`recovery_points.go`：加密 secret bundle；只经 `BeginRecoveryPointCapture` 在正文资源 seam 关闭新删除、固定并有界等待先行 deleting owner，ready 后立即 `defer Close()`，再调用 `Snapshotter` 锁外 online backup，净化暂存副本并完成 SQLite 单文件封存，从受控只读入口收集 refs 后 `Seal` 为精确 pin，再锁外复制/加密；capture busy 返回可重试 409，不得让快照捕获“文件已删、行未删”的正文。`backupId` 派生 `bundleKeyRef`；创建/删除 journal；只列 committed；sidecar `{dataDirectory}/restore.journal` + SQLite `restore.commit_marker`；回滚有 `rolled_back` 持久阶段；`content-master` previous+staging。restore 对原 artifact/manifest/bundle 只读，使用 opId 专属 `stagedDatabase` 工作副本，并在 sidecar 持久保存按 channel identity/稳定主键定位的 canonical `secretBindings(sourceRef→stagedRef)`；切库及 committed 重入都按 owner/bundle 校验，不能仅比较可读 ref 集合。在线恢复 sidecar 的 `committed` 按恢复点 manifest 完成最终化并保持 draining；冷启动 committed 则在 pre-runtime 校验持久集合并返回单次 capability，初始运行快照发布后才转 `cleanup_pending`。进入前精确清理工作副本，进入后恢复语义结束，只按 opId/安全根精确续删 `oldContentDir`，不得重放恢复或用旧 manifest/configDigest/ref 集合验证正常演进的 live。
`local_fallback.go`：按 3.0 分离主密钥读取/原子初始化；candidate 文件及父目录同步先于 authority committed，删除持久化先于清理证据移除；不直接复用 truncate 写法。
`server.go` / `metrics.go` / `probes.go`：每小时 `maintainHotStorage` 整轮按 3.1 复用 maintenance lease，覆盖聚合读取/计算/提交与 `TrimProbeRuns`，不得只保护最终 SQL 事务。
`content.go`：恢复暂存只使用 sidecar 指定的 `restore:<opId>:content-master-previous|staging`；读正文只认固定 live `content-master`；启动时仅对无正文引用的新库初始化 live 主密钥。

阶段 13 只拆出本阶段新增或直接触及的职责，不做无关重构。当前已超过约 1000 行且会被本阶段触及的 `internal/server/proxy.go`、`internal/server/config_api.go`、`internal/storage/model_catalog.go` 不得继续承载上述新状态机；完成 A–E 后，这些被触及文件应回到约 1000 行以内。若仍超限，必须在对应工作包内按本节 module seam 继续提取，而不是把同一职责切成无行为深度的 pass-through 文件。

---

## 8. 阶段 13 内部工作包

阶段 13 只有一个正式发布批次，A–E 是便于实现和验收的**内部工作流**，不是可独立部署版本。schema 28 只在 A 的空数据目录初始化中创建一次；配置 schema 9、迁移包 v2 的读写闭环在 B 同时切换。任何中间构建不得创建或导入正式数据，也不得打 release。

每个工作包开始前先写最小验收清单；涉及权限、计费、并发、持久化和事务的关键行为使用最少量临时测试或可控假上游建立失败反馈环，实现后复跑同一反馈环并删除临时文件。每包完成定向 Go/TypeScript 检查和文档回填；只有自身本地 gate 通过才进入下一包。用户只授权单包时，`READY` 不自动授权继续；明确委派整个阶段 13 时，可按第 8.0 节连续推进，无需逐包重复请求授权。跨平台发布验收统一在 E 本地功能收口后执行，不阻塞 A–E 本地开发。

实施基线当前不是全绿：`go test ./...`、`go test -race ./...`、`go vet ./...`、`npm run typecheck` 虽通过，但 Go 输出全部为 `[no test files]`，只能证明包可编译和静态路径未失败，不能当作行为覆盖；`npm run check` 仍有 5 个前端文件格式问题及 `web/src/pages/logs/logs.tsx:369` hooks warning；`git diff --check HEAD` 仍被 `internal/storage/catalog_configuration.go:546` 的 EOF 空行阻塞。这些是阶段 13 实施前的**只读基线**，不是 A–E 自动取得的修改授权；阶段 13 实现不得顺手修复无关 dirty 文件。若实施时仍存在，必须另立范围明确、经用户授权的 baseline maintenance batch，并在 E 发布 gate 前关闭；禁止把既有失败误归因于阶段 13，也禁止在最终发布 gate 中豁免。

### 8.0 Grok / 实施代理执行入口

本文已经给出技术方案与实现约束：第 0 节决定产品行为，第 1 节定位当前代码接缝，第 2 节规定数据模型与 DDL，第 3–5 节规定接口、事务、状态机及唯一启动清单，第 6 节规定 UI，第 7 节规定文件与模块职责，第 8 节逐包列出实现范围和验收，第 10 节规定编码约定。下面的路线表只组织阅读与编码顺序，不替代这些契约，也不另定义启动顺序。具体函数体由执行者结合当前代码编写，不能因表中只列主要文件而漏掉工作包验收项。

**首次开始与恢复进度**

1. 读取仓库约束、主计划阶段 13 和本文；首次只读记录 staged/unstaged/untracked 工作区、当前版本与验证基线。结构查询优先 CodeGraph，精确字符串用 `rg`。保护用户已有修改，不提交、不清理无关资产。
2. 确认委派范围：整阶段任务按 A1→A2→A3→A4→A5→B→C→D→E 连续实施；单包任务完成后停在指定边界。所有运行、导入和恢复反馈环使用隔离的临时数据目录、端口与测试凭证，不接触用户正式数据，也不以“初始化”删除旧 schema 27 数据目录。
3. 开始每个检查点前，把该点及所属工作包的相关 gate 转成可判定的最小验收清单，记录到第 11 节。先实现被依赖模块的真实行为和窄接口，再接 storage/service/HTTP/UI；尚未接入的路径明确记为未完成，不用空实现或固定成功响应宣称通过。
4. 依次完成一个关键行为的失败反馈环、最小实现、同一反馈环复跑；随后做受影响 Go/TypeScript 检查和增量 diff 审查。并发、权限、计费、持久化和事务故障必须在当前检查点验证；测试成功后删除本次临时测试文件，保留可重建夹具的操作说明、命令和结果摘要。
5. 验收通过后回填第 11 节和主计划中的真实进度，再进入下一检查点。上下文压缩、会话中断或换执行者后，先读取记录并核对当前代码和工作区，从最后未完成项继续；不从头重做、不凭历史 PASS 跳过后来修改所影响的验收。

**编码路线与完成证据索引**

| 顺序 | 功能与采用方案 | 主要代码落点 / 阅读入口 | 进入下一步的证据 |
| --- | --- | --- | --- |
| A1 | 全新 schema 28、default 组及约束；实例锁内复制到隔离目录做 SQLite preflight，原资产零写入 | `internal/storage/sqlite.go`、`cmd/oneai-proxy/main.go`；第 2 节、第 5.4 节 | 全部新增 DDL 实际执行、关键约束拒绝非法值、旧库拒绝与 WAL 最新提交读取反馈环 |
| A2 | namespace/path binding；带 generation 的 SecretStore authority；keyring/fallback 持久化与启动恢复 | `internal/secret/instance_store.go`、`keyring.go`、`local_fallback.go`；第 3.0 节 | prepared/committed、不确定 Put、连续换代与本机 fsync/原子发布故障反馈环；外平台原生证据登记待 E |
| A3 | provisional/retired 生命周期、ProvisioningBatch ownership、稳定 projection 与固定令牌 journal | `internal/runtimecoordinator/secret_lifecycle.go`、`provisioning_batch.go`、storage 固定令牌原语；第 2.7、3.1.2 节 | COMMIT 前 abort、COMMIT 后前滚、不确定提交和重复启动均有持久证据且无误删 |
| A4 | 不可变快照、窄 façade、短提交/lease/draining；maintenance、SSE 代次取消、中断账本与价格目录快照 | `internal/runtimecoordinator/coordinator.go`、`internal/storage/interrupted_work.go`、`internal/billing/pricing_snapshot.go`；第 2.4.1、3.1 节 | 本机慢 body、令牌换代、维护交错和中断幂等验证；价格快照不冒充 C 的真实计费 |
| A5 | 正文资源 journal/read-delete-capture pin；独立 online backup；恢复点与恢复状态机内核 | `internal/storage/content_resources.go`、`snapshotter.go`、`recovery_points.go`；第 3.0.1、5.4 节 | 正文及恢复各提交窗口、只读 artifact、单文件封存、唯一启动清单、真实 restore 与 A4 maintenance 交错通过；汇总关闭整个 A gate |
| B | 先组/密钥及额度管理，再固定 RouteScope/主体 affinity、bundle 换代与 PrepareAttempt，最后 UI 和 schema 9 / migration v2 完整往返 | 第 2、3、5、6 节；第 7 节的 groups/keys、catalog/admission、`configreplace` 与 token storage 落点 | B gate 全部本地通过；密钥鉴权与组隔离同时接通；导入复用 A 内核；used 受控夹具不冒充真实结算 |
| C | 先 usage 三态与精确价格，再 Attempt/额度/健康同事务幂等结算，最后金额详情与价格编辑 UI | `internal/server/protocol.go`、`internal/billing`、storage Attempt/health 原语、模型价格与请求详情组件；第 0.4.1、2.4、3.3、4 节 | C gate 的真实 Attempt 跨限、重复结算、流式 usage、溢出及十进制往返通过 |
| D | 多 owner Aho-Corasick，一次规范化/扫描；先全局过滤，再接 B 的 reservation 后渠道过滤，补设置与账本 UI | `internal/contentfilter/engine.go`、`internal/server/attempt_preparer.go`、`proxy.go`；第 0.8、3.4、6 节 | D gate 的候选可承接性、拒绝后不 fallback、保留先前费用、确定性命中与容量/取消反馈环通过 |
| E | 先接 A 的恢复点 API/UI，再本机完整恢复/崩溃/并发回归、响应式收尾，最后集中跨平台发布验收 | 第 5.4、6 节及 E gate；handler 复用 `RecoveryPointStore`，不重写状态机 | 分别记录 E 本地结果和逐平台发布结果；全部发布门槛关闭后才具备发布条件 |

**继续、阻塞与完成的判定**

- 整阶段委派允许在当前本地 gate 通过后继续下一包；发现本包实现缺陷就在本包修复。规格矛盾、需要改变产品契约或需要越过委派范围时，列出具体冲突和建议，等待决定后再做依赖该决定的工作。
- Windows/Linux/macOS 其他架构环境不足、签名/公证或跨 OS 互导尚未执行，只登记为 E 发布待办，继续可完成的本地任务。平台适配代码按既定契约实现；交叉编译仅算编译证据，不能替代原生 keyring、文件替换/flush、恢复与迁移验证。
- 既有无关格式/hooks/EOF blocker 按本节上方的 baseline maintenance batch 规则处理；不阻塞无依赖的功能开发，最终发布 gate 仍须关闭。整阶段开发委派不隐含清理无关 dirty 文件的授权。
- 本地 A–E 实现及本地验收通过，但跨平台待办未关闭时，交付状态为“本地实施完成，跨平台发布验收待完成”；不能标记整个阶段完成、正式支持未经验证的平台，或宣称可发布。实际发布、提交代码与处理正式数据均不属于本执行入口的授权。

### 工作包 A — 权威基线、实例秘密与运行协调器

**实现范围**

- 同步主计划与本文的阶段 13 状态、版本和术语；代码与 UI 不再把同实例恢复点描述为通用灾备。
- 空数据目录一次初始化完整 schema 28，包括本计划后续所需全部表、列、CHECK、索引与 `proxy_keys.tombstoned_at`；实现第 2 节隔离副本 preflight，非空数据库最高版本不等于 28 时在原库任何 SQLite 打开和 secret 读取前拒绝，原数据库资产零写入。
- 实现 namespace 文件、SQLite `instance.secret_binding`、规范化真实目录身份三方 preflight，以及 authority v2、不可变 generation 物理账户、连续 Put/不确定 candidate 的精确清理集合、精确 generation lease 与 `RecoverPendingAuthority` 的实例化 SecretStore。
- 实现 `SecretRefLifecycle` 的 provisional/retired 账本、`ProvisioningBatch` capability/operation lease、专用 `provisioning_batches` committed journal、排除热路径动态字段的共享 RuntimeConfigProjection 摘要、ref 级活跃引用检查、owned batch 接管协议和启动幂等前滚；恢复点/fixed token/正文资源继续使用各自 journal。
- 实现双槽固定令牌 journal、启动前滚/回滚和新默认代理令牌 `sk-oneai-admin-proxy`。
- 实现 `RuntimeCoordinator`、含 `PricingSnapshot` 的不可变 `RuntimeSnapshot`、`RequestLease`、system/admin/content `OperationLease`、整段日志清理 maintenance lease、可取消且绑定认证代次的管理运行日志订阅、自持 operation lease 的 `RecoveryPointCapture`、内部 short/draining executor、按消费者交付的类型化窄 façade、`maintenanceDrainTimeout` 与维护 503；SSE 订阅不计入 drain lease，capture 只暴露 `Seal`/幂等 `Close()`。handler 不得取得通用 callback runner；只有存在两种真实 adapter 时才定义 interface，不为测试把内部 seam 暴露出去。
- 实现 `Snapshotter` 独立只读单连接、固定源读取事务的分页 online backup、`storage.CloseInterruptedWork`、`ContentResourceStore` 的正文创建/删除 journal、父 Request 条件清理与继承在途 RequestLease 的同步容量 trim，以及负责净化后 SQLite 单文件封存与统一只读 artifact 入口的无 UI `RecoveryPointStore.Create/List/Verify/Restore/Delete`、restore `RecoverPendingBeforeRuntime` 与 artifact `RecoverArtifactJournals` 内核、正文/secret pin、创建/删除/restore journal 和 artifact 原子状态。正文 creating 必须在首次 `Store.Put`/文件写入前登记并以 `content_blobs` 行作为提交证据；每份 journal 带 operationId，创建、删除、恢复和所有清理入口通过同一 per-blob owner，只有匹配 kind+operationId 才能收口，deleting 不得覆盖尚未收口的 creating。deleting journal 一经发布就只前滚删除，启动按 kind 区分“已提交资源必须完整”与“删除中资源允许部分缺失”。capture 与删除使用同一资源准入序列点，有界等待已经取得删除 owner 的 predecessor 完成后才允许 online backup；`Create` 取得 ready capture 后立即 `defer Close()`，在 committed 或已持久化 abort 证据后才返回。先让后续整包导入能够创建可供管理员显式选择的导入前恢复点，普通导入失败仍按事务提交点 abort/前滚，禁止自动恢复较早恢复点。管理入口与最终三平台验收留到 E。
- HTTP Server 增加 `ReadHeaderTimeout`、请求体大小/读取期限和 `IdleTimeout`；保留适合 SSE 的总时限、流空闲时限和逐写期限。
- 按第 7 节拆出本阶段将直接触及的大文件职责，不得把新增状态机继续写进超限文件；对无关基线 blocker 只记录，不修改，除非用户另行授权 baseline maintenance batch。

**A 内部顺序检查点（仍是同一个不可独立发布工作包，不增 coordinator 层）**

1. A1：用全新临时 SQLite 执行 schema 28 **全部**新表/索引 DDL，插入 default 组，断言唯一默认、引用、safe 占位只能 disabled、渠道 config version 为正、`stream_idle_timeout_mode` 只能是 `inherit|override`、`override` 时 `stream_idle_timeout_ms > 0`，以及 Attempt health attribution 状态约束；完成实例身份与隔离副本数据库/目录 preflight 最小反馈环，覆盖 WAL 最新提交状态和原资产零写入。
2. A2：实现带 generation 的 `SecretStore` authority、系统 keyring/fallback 一致性、3.0 的本地后端持久化契约与启动恢复，完成 prepared/committed/不确定写入/连续 Put 的独立崩溃反馈环。
3. A3：实现 `SecretRefLifecycle`、`ProvisioningBatch`/committed 行、固定令牌 journal 与共享稳定 projection，分别验证暂存清理、COMMIT 前 abort 与 COMMIT 后前滚，再接入下一检查点。
4. A4：实现 `RuntimeCoordinator` 的短提交/lease/draining、覆盖日志清理及每小时 `maintainHotStorage` 的整段 maintenance operation 与可取消管理日志订阅、固定价格目录快照和 `CloseInterruptedWork`，验证无网络锁持有、慢 body、令牌换代/维护时 SSE 断流，以及 processing、已终态父 Request 下残留 processing Attempt、失主半开占用与受影响统计小时标脏的单事务收口；A 只用受控 SQLite 事务验证动态字段不进入 configDigest，不提前宣称真实 Attempt 计费或列表密钥准入完成。
5. A5：实现 `ContentResourceStore`、独立 `Snapshotter` 与无 UI `RecoveryPointStore` 内核；先分别闭环正文 creating/deleting 的 operation owner 交接、恢复分支与父 Request 条件清理，再验证先行 delete/capture 双向交错、online backup 以固定源读事务持续推进且不占业务主连接、capture pin、净化后的 SQLite 单文件封存及旁路文件精确清理、只读恢复点 artifact + opId 工作副本、owner-aware secretBindings、restore pre-runtime/finalization 与恢复点 artifact 创建/删除 journal 在唯一启动清单中的崩溃续作。

每点的交付物是对应 module 的可调用接口、最少量可复跑的临时反馈环及结果记录；临时测试成功后删除。A1–A5 不构成独立发布；进入 13B 必须关闭整个 A 本地 gate，并满足第 8.0 节的整阶段或单包授权边界。

**A gate**

- 将文档里的完整新表/索引 SQL 在临时 SQLite 真正执行到结束，default 插入成功；重复默认、非法成员、非正 `channel_config_version`、非法 `stream_idle_timeout_mode`、`override + 非正 stream_idle_timeout_ms`、非法 `health_attribution` 和 `enabled + pending:<id>/空 ref` 被约束拒绝，合法 `inherit`、正数 `override` 与 disabled 占位可插入，且占位额度解除后只能保持 disabled；不得只做 Markdown/SQL 文本匹配。
- 两个空数据目录得到不同 namespace 和物理 secret；替换 namespace 文件、binding 不一致，以及把目录移动/复制到**不同规范化真实路径**均在 secret 读取前失败。另明确记录：完整目录跨机器复制到相同绝对路径不受支持但当前不保证可检测，本 gate 不得伪造“所有复制均拒绝”的证据。
- 隔离副本 preflight 最小反馈环覆盖：已 checkpoint 且仅剩主文件的 WAL 模式旧库；未 checkpoint 的 schema 27/29 库；缺少/损坏版本表；主文件为 27、WAL 最新提交为 28 且含合法 binding，以及主文件为 28、WAL 最新提交为 29 或无效 binding。必须依据 WAL 最新已提交状态判定，合法 schema 28 可重启，其余无效状态在 namespace 写入和 secret 访问前拒绝。分别比较调用前后原 DB/WAL/SHM/journal、namespace/secret 文件集合与哈希（仅实例锁文件除外），不得因 mode=ro 新建原 WAL/SHM。复制失败或复制期间文件变化时也应拒绝，关闭并清理副本，不回退原路径；不存在/0 字节主文件但有非空未解释恢复 sidecar 时不得创建 namespace 或当作空库。
- authority v2 在 `put_prepared` 发布前后、candidate 物理写入后、`put_committed` 发布后与 retired 清理中逐点模拟崩溃：恢复后只能读完整旧值或完整新值，旧 generation 在提交前不得丢失。注入“keyring 物理写成功但 Put 返回错误”和“写成功但读回失败”，原 candidate 必须以 `abandoned_candidate` 留在精确清理集合；G1 被 lease pin 时连续 Put G2/G3 不等待、不覆盖 retired 证据，并覆盖每个 authority 发布点崩溃。retired 达到预留阈值时新 Put 必须 409 且 authority 零变化，释放 lease/清理后可重试。`RecoverPendingAuthority` 连续执行两次结果相同，delete pending 不扫描密钥环。
- 本地 fallback 故障注入覆盖首次 `master.key` 临时写入、正式发布及父目录同步前后，candidate 临时写入、正式发布、父目录同步及 authority committed 前后，以及物理删除后、目录同步前、slot 移除前的窗口。未完成初始化不发布依赖它的 committed 账户；已有合法主密钥不被替换，已有本地密文但主密钥缺失/损坏时 Get/Put 都拒绝生成新钥。文件或目录同步失败不得报告持久成功；重启只清理精确 candidate/临时文件，旧 active 保留，删除证据在同步完成前不得消失。测试至少记录底层持久化调用顺序及失败出口；进程 kill 仅证明进程崩溃恢复，不得标成已验证真实掉电持久性，平台 flush 能力另按三平台 gate 验证。
- 无效 Bearer 不读取 body；已通过 preflight 的慢 body 不持 `RequestLease`，且受大小/读取期限限制。一个慢代理请求与一个 draining 请求不会形成 writer-preference 全站锁死；drain 超时返回零提交并恢复接流。
- 请求体 deadline 反馈环必须使用真实 `adminHandler` middleware 链，依次穿过 `runtimeResponseWriter → auditResponseWriter → handler`；两个 wrapper 的 `Unwrap()` 让底层 deadline adapter 实际收到 set/clear。覆盖设置失败、30 秒慢 body、正常读取后清零、清零失败四条路径：前两类按规则终止，正常清零后可继续 SSE/keep-alive，清零失败在任何账本/secret/上游副作用前 500 并 `Connection: close`。只对裸 handler 或自制无 wrapper writer 调用不算通过。
- 管理运行日志 SSE 经真实 admin middleware 建立订阅后轮换管理令牌：旧连接在换代点后选取不到新日志或 reset，新代次用当前令牌与游标重连；建连与令牌换代并发必须只得到新代次订阅或 401。打开日志页时整包导入/restore 进入 draining，旧连接取消且不阻止 30 秒 drain，新连接维护期 503；慢客户端不得占用全局锁。前端 401 等新令牌、503 遵守 `Retry-After`，游标失效仍按原 reset 路径处理。
- 固定令牌在 staging、live 覆盖、SQLite 提交和 committed 清理各崩溃点恢复为完整旧或完整新；提交后只前滚。
- provisional ref 在登记前、Store.Put 后、业务事务前后各崩溃点都不会产生不可追踪孤儿；活跃 operation lease 阻止并发误删，重启后未引用 ref 可续删，已引用 ref 只清账本不删 secret。
- 正文 creating 在 secret 已写但 `content_blobs` 尚未建行、final 文件已发布但尚未建行、业务行已提交但 journal 未删三个中断点均由 `ContentResourceStore` 接管：有精确已提交行只前滚清 journal，无行则幂等删除文件和 key ref；已提交行缺资源、路径越界或字段不匹配失败闭锁。固定暂停在“业务行 COMMIT 后、creating journal 条件删除前”，并发触发容量、保留期和手动清理：竞争删除不得覆盖 creating；创建者收口后删除者才能发布新 operationId 的 deleting，创建者迟到收尾也不得删除该 deleting。正文 deleting 分别在“journal 刚 fsync”“文件已删”“业务行已删”“key 已删”“journal 删除前”中断，连续重启及重复 `RecoverPending` 都必须继续同一 owner 的删除意图；文件/行/key 的 not-found 是对应步骤完成，仍有 pin、owner 或精确身份不符则不得删除，也不能因业务行尚存在而把已删除文件误判为创建损坏。
- 自动与手动保留期清理分别覆盖：读取/capture pin 令正文跳过后继续到父 Request 阶段，父行和 `content_blobs` 元数据必须仍在，释放 pin 并完成正文 journal 后才可重试删除；只有 processing Request 或未完成 creating/deleting journal 时同样不得删父行。固定在正文清理阶段结束与父行删除前发起在线 restore：restore 必须等待旧清理的整段 maintenance lease，超时则零提交；旧操作结束后才允许切换集合，绝不能在新库继续删父行。重复清理后不残留无主 secret、文件或 journal。
- 每小时维护分别暂停在“旧集合统计计算完成、尚未调用 persistMetricDimensions”和“已读取旧保留设置、尚未执行 TrimProbeRuns”，随后发起 restore。恢复必须等待整轮 maintenance lease 完成，或 30 秒超时零提交；不得在切换后继续写入旧聚合或按旧设置清理新探针记录。夹具分别让恢复目标该小时没有 dirty 行、以及 revision 与旧值相等，证明隔离依赖 lease 而非 revision 碰巧不同。draining 中触发新轮次（含启动后首次调用的统一入口）不得读写数据库；释放后新轮次从当前设置和当前集合重新计算。A4 用受控维护提交交错验证 lease，A5 接入真实 Restore 复跑；不把尚未接入恢复内核的夹具标为完整恢复验收。
- 在途请求已取得 RequestLease、尚未写客户端正文时暂停 → 导入/恢复进入 draining → 继续写正文并触发容量 trim：trim 必须继承父 lease 且能够正常完成，不能新申请 maintenance lease 后因维护状态返回 ledger_error；同时新定时/手动清理仍被拒绝。维护等待父请求和同步正文子操作全部完成或 30 秒超时零提交；所有子操作返回前不得释放父 lease，释放后不能在恢复的新集合继续清理。此反馈环配置足够可回收空间，真实容量不足/I/O 失败不混作维护准入结果。
- `provisioning_batches` 只接受严格 v1 committed 行且最多一行；配置/迁移导出和净化后的恢复点 SQLite 均不含它。分别在“业务 COMMIT 成功后立即崩溃”“COMMIT 返回不确定”“snapshot 已发布但 Complete 删除前崩溃”注入故障：启动必须用共享 projection 摘要与精确 newRefs 判定并幂等前滚；摘要/引用/secret 不一致必须保留证据并阻止启动，不能 destructive Abort。
- 在 RuntimeSnapshot 构建前、构建后未发布和发布后分别暂停，用明确标注为 **A 阶段测试夹具** 的受控 SQLite 短事务修改 `dollar_used_micros`、quota 派生字段、健康和时间字段，证明共享 `configDigest` 不变且 snapshot 发布协议不被热字段写破坏；不得调用尚属 C 的真实 `SettleAttemptHealth`，也不得把该反馈环记录为真实计费/跨限验收。列表密钥当前状态复核和主体删除交错由 B 接续，真实 Attempt 结算由 C 接续。
- 初次管理鉴权后暂停，再轮换管理令牌：旧 Bearer 的元数据 GET、完整配置/迁移导出、渠道凭证与列表密钥 secret read 全部在当前代次复核处 401，不能观察或包装新代次数据。
- 内部创建两份空/基础 schema 28 恢复点后可独立校验；删除第一份不影响第二份。逐点中断 `.creating-*` 的 artifact/manifest/bundle/`backup-bundle-key` 写入，以及 deletion journal 的逻辑隐藏与每项物理删除；重启必须在 `FinalizePendingRestore` 之后显式调用 `RecoverArtifactJournals`，未发布 artifact 永不可见，删除中 artifact 不得重新可见。逻辑状态、身份或 journal 接管失败阻止监听；已持久接管的物理清理失败可启动后台重试且不误删第二份恢复点。
- 用至少跨越 6 个 256 页 Step 的大库持续写入并结算 Attempt，同时执行 `Snapshotter` online backup：独立源连接必须在第一次 Step 前以真实读取固定一个读事务快照，业务连接每个 Step 间提交也不能让 backup 重启，backup 在预期页数对应的有限 Step 内完成；代理与结算持续成功，快照通过 integrity/FK 校验，业务主 handle 不执行 `VACUUM INTO`。瞬时 BUSY/LOCKED 必须在总 deadline 内重试并完成；另分别在取消、持续 BUSY/LOCKED、其它错误和 120 秒总时限到期路径断言 backup `Finish`、只读事务及连接全部释放，只终止本次 capture，WAL 可继续回收且不发布恢复点。
- A5 单文件反馈环使用 WAL 源库，快照含四个本机运行键和 provisioning 行；净化提交后封存为 DELETE 模式且无旁路文件，仅读取主文件也能证明这些行已删除，integrity/FK 检查通过。分别在净化提交、checkpoint、模式切换、关闭连接、文件/父目录 fsync 和发布前注入失败/中断，未完成封存不得 committed，abort/重启精确清掉该副本及登记的旁路文件，不影响 live 或其它恢复点。连续两次 Verify/Restore 前后比较原 artifact 文件集合、大小和 hash，禁止新增 `-wal/-shm/-journal`；遇到 WAL 文件头或遗留 sidecar 时只拒绝而不修复。只读入口不得对 live/preflight 使用 immutable。
- 恢复点正在 online backup 或复制大正文时发起 restore：restore 必须等待 capture 内部 operation lease；30 秒内未释放则退出 draining、保持零提交并恢复接流。capture/delete 双向交错都要覆盖：先 ready fence 时新删除不能取得 owner；先 deleting owner 并已删文件、业务行尚在时，capture 不得 pin 该资源或开始 SQLite backup，必须让先行删除继续收口并有界等待，超时返回 `recovery_point_capture_busy` 且不发布恢复点。正文查看在同一 deleting 状态固定返回 `content_unavailable`，已有 read pin 则反向阻止删除。宽 fence 收窄为快照 refs 的精确 pin 前后都制造删除交错，快照引用不得丢失、无关资源不得被长期 pin。创建成功、复制/加密失败、请求取消和 panic/提前返回都通过幂等 `Close()` 释放进程内 lease/pin；崩溃后的 artifact/key 清理只由 journal 续作，不假设旧进程 lease 仍存在。
- restore staging 必须先一次性持久化按 owner 规范排序的全部 `secretBindings`，再写任一 staged secret；原恢复点 SQLite/manifest/bundle 的大小与 sha256 在 restore 前后完全不变，所有 ref 改写只发生在 sidecar 指定的 opId 工作副本。分别在 binding fsync 后、部分 `Store.Put` 后、部分工作副本行改写后和 `state=ready` 发布前中断：marker 不存在时连续重启均只清本次 binding stagedRef、工作副本和 staged 内容，不删除 sourceRef 或修改原恢复点。
- processing Attempt/Request 分别覆盖“仅 Attempt 中断、processing Request 含已结算 + 中断 Attempt、**已终态 Request 含已计费 + processing Attempt**、聚合 overflow、重复启动”组合；`CloseInterruptedWork` 单事务把 Attempt 收口为 `failed/process_interrupted + usage_unknown + $0 + health_attribution=not_applied`，对“原 processing Request ∪ 被关闭 Attempt 的父 Request”重算 attempt_count/金额/计费状态，仅把原 processing Request 收口为 `error/process_interrupted`，已终态父行保留原响应终态、完成时间和错误/HTTP 结果，同时清失主的 `half_open_claimed=0 + half_open_claim_owner=''`，不改健康状态/失败计数/额度/fallback；失败整笔回滚并阻止启动。已终态父行的已计费 Attempt 金额必须保留，中断 Attempt 只贡献 unknown+$0，聚合应从 priced 等状态变为 mixed，重复收口不得改金额或重复扣费。选一个请求 `started_at` 小时已存在 `hourly_metrics`、请求尚 processing 且跨到下一小时的场景：收口时仅为**开始小时**写一条 `hourly_metric_dirty`，`AggregateMissingMetrics` 后成功/失败率和 Attempt 指标与账本一致；同小时多 Request/Attempt 仅增加一次 revision，第二次收口无变化不加 revision。在线恢复复用此原语的独立交错在 E gate 验证。
- 记录 scoped `npm run check`、`git diff --check HEAD` 的阶段 13 增量结果并与既有 blocker 分开；不得修改无关基线资产。复跑 `go test ./...` 时明确把 `[no test files]` 记录为编译证据，权限/并发/持久化行为以本包最少量临时反馈环结果为准，成功后删除临时文件。全仓门禁只在另行授权的 baseline batch 完成后进入 E 发布 gate。
- 代码搜索确认跨模块接口没有 `migrationBarrierMu`、`adminBarrierAuth`、`...Locked`、“调用方已持锁”契约、任意 `func(tx)` mutation/operation callback 或可被 handler 调用的通用 `RunShortCommit` / `RunDrainingCommit`；`server` 只能看到消费者侧窄 façade，`configreplace` 与 `RecoveryPointStore` 各自只能看到专属 typed committer。

### 工作包 B — 访问组、列表密钥、RouteScope 与可迁移闭环

**实现范围**

- 实现渠道 `tag`、访问组、M:N 成员、default 不变量、列表密钥 CRUD/secret envelope 和独立 `/state`。
- 新建密钥由服务端生成至少 128 bit 随机、不可复用的 id，并立即生成真实 token/hash/prefix/secret_ref，固定 `disabled + manual`；轮换只改 token/hash/prefix/secret_ref，不改 id/status/reason。最终物理删除在同一事务清除该 key principal 的全部 affinity。
- 实现列表密钥与渠道的 limit 修改、reset-usage、quota 状态重算和占位密钥状态规则，以及对应管理列表/摘要/配置导出的一致读取；B 只提供管理写入与读取契约，不实现 Attempt usage 计价或自动累加 used。
- `proxyAuth` 改为先调用 `PreflightProxyAuth`，body prepare 后再调用 `BeginProxyRequest` 重鉴权；同一 `RouteScope` 贯穿 ResolveRoute、刷新、fallback 和 Responses affinity。
- 建立最终形态的 `PrepareAttempt` 基础 seam：业务、自动探针和 saved 手动探针/模型发现分别从 Request/system/admin operation lease 取得固定 snapshot；动态准入读取当前渠道状态，渠道被删除返回 `Skipped(channel_removed)`，同 identity 的 config mismatch 按 3.3 进入 skip 或 stale neutral；模块内部读取精确代次凭证、取得可取消 reservation并直接提升为携带凭证的 `AttemptLease`。半开 reservation 生成持久 owner，领取、健康结算和提前释放都校验 channel identity、config version、health version 与 owner；B 中过滤器为 no-op，D 只在 reservation 与提升之间接入渠道词表，不得重写调用顺序。draft 表单操作不进入此 seam。
- schema 28 渠道增加服务端 `channel_identity + channel_config_version` 与显式 `stream_idle_timeout_mode=inherit|override`，并贯穿 `RuntimeSnapshot/RouteTarget/ChannelOperationSnapshot/AttemptLease/HealthSettleInput`；Attempt 持久化所用 config version 与健康归因。普通创建/复制生成新 identity/version，编辑用共享有效 execution projection 决定 version 是否递增，配置/迁移导入重建，并在同一事务按 2.1 初始化完整健康运行态；同实例恢复保留身份与快照健康，仅收口失主 claim。全局 `ChannelSettings` / `RequestPolicy` 保存必须按 2.1 同事务比较每个普通渠道的修改前/后有效 projection，只递增实际受影响渠道的 config/health version，并清旧 owner；全局 idle 只使 inherit 渠道换代，override 渠道保持不变，mode 切换必换代。独立全局模型映射保存按 2.1 对规范映射发生变化的协议族普通渠道保守换代，另一族及相同映射不换代；`BeginProxyRequest` 固定同代全局执行设置、完整 RequestPolicy 和预计算 effective timeout，后续请求体改写、传输超时和请求预算不得读取 live 设置或自行 fallback。`PrepareAttempt` 按 id+identity+config version 复核，`AttemptLease.RecordAttempt` 在删除保护仍有效时插入 processing Attempt。
- 实现列表密钥 tombstone：删除立即对新鉴权/列表/导出不可见；在途 Request/Attempt 结算完成后再物理删行与 secret；启动先收口 processing，若有 committed restore 则在 finalization 成功进入 cleanup_pending 后才物理清理。
- 管理渠道操作显式区分 `saved_channel` 与 `draft_channel`：已保存渠道测试/模型发现使用当前行的 identity/ref 和 `PrepareAttempt`；未保存新渠道或尚未提交的编辑表单直接使用 payload owned 凭证取得 admin operation lease，不要求 identity/ref，不写 Attempt、健康、额度、敏感词账本或持久 reservation。草稿操作在当前令牌复核前失效即 401，draining 期间按 operation lease 等待/超时零提交。
- 分组页、密钥页、渠道标签/访问组 UI 同步完成；窄屏基础布局不使用 `grid`/`gap`。
- 同时启用配置 schema 9 和迁移包 v2 的严格导出、预览、导入与事务替换；complete/migration 由 `configreplace` 在 coordinator 外用同一 operationId/`ProvisioningBatch` staging 全部新 secret，再把 prepared replacement + capability 交给其专属 `ConfigReplacementCommitter` 原子接管。导入前自动恢复点必须调用 A 的 `RecoveryPointStore`，不得临时退回 SQLite-only 快照。导入本身按 journal/事务提交点在提交前 abort、提交后只前滚，不自动恢复较早恢复点而抹掉随后产生的日志；恢复点留给管理员显式回滚。safe 占位、complete_encrypted、实例运行键和日志占位渠道遵守第 5 节。
- 普通渠道凭证模式规则集中到配置/迁移/恢复点共用的领域校验 seam，不新增只转发的薄模块；safe 入口禁止渠道 `credential` 并强制停用，complete/migration 的 enabled 渠道缺失或空凭证在 staging 前 400，导出无可读凭证整体 409。UI 对 safe 导入渠道展示派生「缺少凭证」，持久人工状态不藏额外原因。

**B gate**

- default 不能删除/停用/移除普通渠道；非默认组删除后密钥无组且不能路由。
- 列表密钥从首次显式启用起只能命中所属组渠道；通用代理令牌不按组过滤；Responses affinity 不能跨主体。固定验证 K1 建立 affinity → tombstone 并完成物理清理 → 创建 K2：K2 的服务端随机 id 必须不同，猜中 K1 的 `previous_response_id` 也不能命中旧 affinity；物理删除 K1 的同一事务必须清空其 principal affinity，失败则密钥行与 affinity 一起回滚保留。
- 删除正在流式使用的密钥立即阻止新请求，但旧 Attempt 可完整结算；DELETE 不等待流结束，最终幂等删除 affinity、密钥行和 secret。普通 POST 携带 id 固定 400，连续创建/删除不得复用历史 id；complete/migration 重用包内逻辑 id 前必须按整包规则清空全部 affinity，同实例恢复才保留快照 id 与 affinity。
- 固定交错：请求取得含旧 `(channel-001, identity-A)` 的 snapshot 后暂停 → 删除无历史旧渠道 → 通过 `POST /channels/bundle` 自动编号、带非空 `bundle.channel.id=channel-001` 的非法新建、复制三条入口分别制造/尝试同 id 新渠道。非法显式 id 必须 400，自动/复制即使复用可读 id 也生成 identity-B；`POST /channels` 和 `PUT /channels/channel-001` 行级写固定 405，`PUT /channels/channel-001/bundle` 若 body id 不同必须 400。恢复旧请求后 `PrepareAttempt` 必须 `Skipped(channel_removed)`，不得读取 B 的动态状态、调用 A 的上游或向 B 记 Attempt/额度/健康。另在 Prepare 成功后、`AttemptLease.RecordAttempt` COMMIT 前并发 DELETE：必须 409；COMMIT 后由历史引用继续拒绝。Record 失败/过滤命中释放 reservation 后，无历史时删除才可成功。
- 同 id 导入的新身份反馈环：目标渠道分别为 auto_disabled、cooldown、half_open（含 owner），带非零业务/探针失败与成功计数、冷却、错误/当前探针摘要及历史 Request/Attempt、health_events、probe_runs。分别以 safe/complete/migration 导入相同 id 的新 URL 和按 mode 合法的凭证字段，验证新 identity、config version=1、health_state=healthy、health_version=0 和 2.1 全部运行态初值同事务生效，历史记录及其金额不变；新 enabled 且未超额的 complete/migration 渠道不因旧故障被准入跳过，safe 仍 disabled + 空 ref 且不发上游调用，used/limit 与 quota 门闩仍按导入值处理。提交前注入失败时身份/健康行一并回滚；不在包中的日志占位健康字段保留。对相同健康夹具做同实例恢复则必须保留状态、版本、计数和摘要，仅清失主 claim/owner，不套用导入初始化。
- 固定健康代次交错：旧 lease 持有 `(identity-A, healthVersion=G1, owner=O1)` 后暂停，管理状态变更使 G1 失效并让新半开验证领取 `(identity-A, G2, O2)`；旧 lease 随后的版本冲突结算、not-sent 或兜底释放都不得清除 O2，第二次 G2 领取必须继续返回 `half_open_claimed`，只有 O2 自己结算/释放后才可再次领取。所有 owner 条件更新失败均不得退化成 `WHERE channel_id=?` 无条件清位。
- 固定配置代次交错一：Request R 先取得 C1 snapshot `(identity-A, configVersion=1)`，管理员再把 Base URL/凭证改为 C2 并提交为 configVersion=2、当前健康进入 half_open，随后 R 才调用 `PrepareAttempt`。R 不得领取 C2 的 owner，也不得用 C1 成功恢复 C2 或用 C1 失败增加 C2 计数；当前健康为 half_open/cooldown/auto_disabled 时直接 `Skipped(channel_config_changed)`。若当前为 healthy/degraded，R 可用受 pin 的 C1 配置完成 Attempt，usage/价格/额度正常结算，但 `health_attribution=stale_config_neutral`。新 Request 使用 C2 并可正常领取 C2 半开验证。
- 固定配置代次交错二：C1 `PrepareAttempt` 已成功并取得 lease 后暂停，管理员提交 C2 并使新的半开验证取得 C2 owner，随后 C1 Attempt 结算。Attempt/计费照常完成，但配置版本 CAS 失败使健康 no-op、归因改为 stale neutral，且不能清 C2 owner。`ChannelExecutionProjectionV1` 还要验证名称/备注/标签/优先级/访问组/额度变化不误增 config version，而 Base URL、凭证、协议、Header、超时、模型/映射和健康策略变化必增。
- 固定全局设置代次交错：Request R 取得 G1 全局执行设置后，分别在 R 的 `PrepareAttempt` 前，以及 lease 已取得但结算前，把全局 reasoning 或 service-tier 设置提交为 G2。只对有效 projection 真正变化的渠道递增 config/health version，并清旧 owner；R 始终用 G1 改写请求，不能领取、恢复或释放 G2 的半开 owner，旧调用只按 stale neutral 完成。渠道固定 reasoning override 时修改全局 reasoning、渠道关闭 service-tier passthrough 时修改对应全局开关，均不得误增该渠道版本；新 Request 才使用 G2。
- 固定 RequestPolicy 代次交错：R 取得首字节超时 1 秒的 P1 snapshot，将策略改为 30 秒的 P2，分别在 R 的 Prepare 前及 lease 已取得但结算前提交。对连接超时和实际生效的流空闲超时复用同一交错；旧请求仍按 P1 执行，按 3.3 跳过或记 `stale_config_neutral`，不能用旧超时处罚 P2 健康、领取或释放 P2 半开 owner，新请求使用 P2。一次 `PUT /settings` 同时改 reasoning/service-tier 和超时，每个受影响渠道只能增版一次，设置与版本一同提交/发布，健康状态、失败/探针计数及额度保持不变。相同策略重复保存、`override` 渠道不受全局 idle 修改影响、仅改 `TotalTimeoutMs` / `MaxChannelAttempts` 均不增对应渠道版本、不清 owner；请求预算仍按各自 snapshot 执行，总预算耗尽保持健康中性。流空闲夹具必须覆盖：新渠道默认 inherit；全局 60s→30s 只使 inherit 渠道换代并让新请求使用 30s；override=300s 渠道不换代；inherit↔override 即使当前有效值相同也换代；override+0/负数在写入和导入时拒绝；schema 9 / migration v2 往返 mode 后语义不变。传输层只能使用 snapshot 的有效值，不得再按 `<=0` fallback。
- 固定全局映射代次交错：R 取得 M1 的 `alias→model-A`，修改同族映射为 M2 的 `alias→model-B`，分别在 R Prepare 前和取得 lease 后结算前暂停。保存 M2 必须与该族全部普通渠道 config/health 换代、旧 owner 清除同事务，R 始终使用 M1 并按旧配置规则 skip 或 stale neutral，不能领取/恢复/清除 M2 半开 owner；新请求使用 M2。OpenAI Responses 与 Chat 同属 openai，修改 openai 同时影响二者但不影响 anthropic；渠道本地映射遮蔽全局变化也按保守策略换代，规范化后相同映射重复保存不增版。`configDigest` 随真实全局映射变化而变化。
- tombstone 后仅有该密钥的 `requests.final_status='processing'`、尚无 Attempt 且进程 lease 已释放时，后台清理仍不得物理删行或 secret；已有 processing Attempt 同样阻止清理。启动先按 2.4.1 收口，再清理并验证幂等；清理查询不得使用不存在的 `requests.status`。
- 三代次交错 `G1 持旧 ref 的暂停请求 -> G2 无关 short commit -> G3 换凭证` 中，G1 仍能取得旧凭证并完成 Attempt；新请求只使用新 ref，清理器必须看到所有引用旧 ref 的 Request/system/admin operation lease，不能只等 G2。
- 自动探针、手动探针、已保存渠道模型发现、渠道复制和正文读取分别从规定的 system/admin/content seam 进入；逐条制造“初次鉴权后轮换管理令牌”和“读 ref 前换凭证/开始恢复”的交错，旧 Bearer 必须 401，合法在途操作要么持一致 snapshot 完成，要么使 drain 超时零提交，不能直接 `Store.Get` 新代次 secret。manual probe 在 half_open 成功必须转 healthy、失败必须转 cooldown，在 auto_disabled 成功或失败都不得自动恢复；automatic probe 仅按启用策略和阈值恢复，admin discovery/draft 不产生健康变化。所有非业务 purpose 均不计费、不累加 used、不清额度门闩或启用人工禁用渠道。
- 未保存新渠道可以测试和发现模型；编辑已有渠道但尚未保存时，测试必须使用表单新值而非数据库旧值。草稿请求在取得 operation lease 前遇到管理令牌轮换必须 401 且零上游请求；取得 lease 后再进入 draining 时由 drain 等待，超时后零提交恢复接流。草稿操作不得产生 Attempt、渠道健康/额度变化、敏感词账本、secret ref 或持久 reservation。
- 渠道复制捕获 G1 后并发把源渠道 Base URL、协议和凭证改成 G2：目标只能是完整 G1 bundle + G1 凭证，不能出现 G1/G2 混合；捕获后删除源渠道仍可按完整快照提交，并发占用候选副本名时在 short commit 重新生成唯一名称；并发删除被快照引用的分组则返回 409 并清理目标 provisional ref。
- 复制 `disabled + 空 secret_ref` 普通渠道时，在同一次当前管理令牌复核中取得完整静态快照，目标仍 disabled/空 ref、used=0 且无 provisional/newRefs/committed 行；源有 ref 但读取失败直接拒绝，不能走无凭证分支；有凭证分支仍保留同代 owned 明文与独立 ref。
- 渠道凭证或列表密钥 SQLite COMMIT 后、snapshot 发布前崩溃时，旧 ref 不提前删除且重启可继续 retired 清理；列表密钥轮换、渠道物理删除和整包替换都覆盖对应 reason。
- 创建与轮换均只通过一次性 `tokenEnvelope` 返回；safe 占位轮换后仍停用，显式 `/state` 才可解除 manual。
- 从各自独立的 safe 导入 `disabled + quota_exhausted + pending:<id> + 空 ref` 初始状态，分别执行重置 used、提高限额、改成不限额：金额照常更新，状态都只收敛到 `disabled + manual`，不能自动 enabled；未轮换时 `/state` 启用 400，轮换出真实 token/ref 后仍 disabled，由后续显式 `/state` 才能启用。UI 同步显示「请先轮换」且不提供可绕过此规则的启用入口。
- 创建、轮换的 `token_prefix` 都包含 6 位随机部分；complete/migration 从真实 token 重算且拒绝不一致 prefix，safe 只接受 `^sk-oneai-[A-Za-z0-9]{6}$`，四条路径不得退回恒定 `sk-oneai-`。
- schema 9 safe/complete_encrypted 与 migration v2 都能完整往返访问组、成员、密钥、RouteScope 相关配置；旧版本和未知字段零写入拒绝。
- 用明确标注为 **B 阶段测试夹具** 的受控 SQLite 短事务，把密钥从一致的 `used=90, enabled` 状态切成一致的 `used=110, disabled/quota_exhausted`；随后管理列表、摘要及 safe/complete/migration 导出必须从同一读视图输出完整前态或 `110 + disabled/quota_exhausted`，不能拼出 `110 + enabled`。secret generation 与配置属于同次捕获，SQLite 只读事务不跨 secret 读取、加密或响应写入；`configDigest` 不含 used/派生状态。本条只验一致读取与并发接口，不得记录为真实结算验收；真实 Attempt 跨限在 C gate 完成。
- 普通渠道三组导入/导出临时反馈环：从 live `enabled + 有效凭证` 渠道做 safe 导出时包内无 `credential`，把该包原样导入必须成功并落为 disabled + 空 ref，UI 显示「缺少凭证」，代理/探针/模型发现不能承接；这条路径不得在 mode 分派前被“enabled 缺凭证”公共检查拒绝。safe 包只有 `credential` 不存在才允许，含 `credential:null` 或对象均 400。complete_encrypted/migration v2 中 enabled + 缺/空/非法凭证均 400、零 staging/零业务提交且不改为 disabled；disabled + 省略凭证保持 disabled 可往返，有效凭证正常重建新 ref；完整导出遇 enabled + 缺 ref/不可读或空凭证整体 409，disabled 无 ref 则省略凭证。
- complete/migration 的 owned batch 在进入 draining 前被 coordinator 原子接管，drain 不等待自身 batch；并发启动另一 provisioning operation 时仍必须等待或按 30 秒超时。接管前失败只由调用方 `Abort()`；接管后且 SQLite COMMIT 前的超时、最终校验失败、明确回滚和 panic 只由 coordinator `Abort()`；COMMIT 后只能只前滚，运行快照发布完成且 digest 一致后只调用 `Complete()` 条件删除 committed 行。临时反馈环断言没有双重释放、交还 ownership、自等待、并存两行或提交后 destructive Abort。
- 在目录/生命周期事务 COMMIT 成功后、固定 live 或运行 snapshot 发布前注入失败与 panic：同一事务内的 `provisioning_batches` 行必须证明 `database_committed`，其 operationId/kind/targetDigest/newRefs 与数据库当前投影一致，新库引用的 ref 全部保留，进程 fail-closed 并可启动前滚；`Abort()` 必须拒绝 destructive cleanup。另覆盖 COMMIT 返回不确定、COMMIT 后立即崩溃重启、snapshot 发布后 Complete 前崩溃，以及 journal 摘要/引用被篡改的拒绝路径。
- migration v2 提交后无需重启：新管理/代理令牌和运行快照同时生效，旧令牌立即 401；监听地址/端口继续保持旧值直到重启。模拟内存发布失败时保留 journal 并 fail-closed。
- complete/migration 成功响应只用 handoff envelope 传递两把新令牌；前端只能通过共享 `PendingAuthTokenHandoffV1` 严格编解码入口写入/恢复 pending JSON，在该写入、两项 v2 token 更新的每个崩溃点都可幂等收口；缺失/未知 version、未知字段或错误类型必须拒绝。成功响应完全丢失时旧令牌保持失效，页面退出并要求输入导入来源管理令牌。

### 工作包 C — Attempt 精确计费与密钥/渠道额度

**实现范围**

- 完成 OpenAI/Anthropic usage 三态解析、缓存字段归一化、Anthropic SSE 合并，以及 Anthropic 5m/1h cache creation 明细识别；当前单一 cache-write 价无法表达正数 cache creation 时按 usage valid + unpriced 降级，不猜测或部分计费。
- 模型价格与 Attempt 快照使用规范十进制字符串；`PricingSnapshot` 与目录同代，价格只在 processing Attempt INSERT、发上游前解析并冻结一次；每分量最终 half-up 一次，所有乘法、求和和 used 累加显式检查 `int64`。前端模型价格编辑、类型、提交、重新打开和范围比较同样以字符串为权威，清空与零价严格区分。
- 扩展 `SettleAttemptHealth`，以 `transitioned` 保证 Attempt 终态、计费、密钥/渠道 used、额度门闩、健康和事件在同一事务幂等。
- 渠道 `quota_blocked` 与健康状态正交；通用令牌只累计渠道额度；探针/测试不计费。
- 接入 B 已完成的 limit/reset/status 管理契约，完成真实 Attempt usage 计价、密钥/渠道 used 自动累加和 quota 门闩更新；C 不重复定义额度 API、重置或管理一致读取。完成请求/Attempt 详情和前端精确金额展示。

**C gate**

- OpenAI 缓存总量不重复计费；Anthropic cache read 与 output 正确合并。出现 `usage.cache_creation` 时，5m+1h 必须等于聚合 creation，非法类型、负数或不相等保持 invalid。Anthropic `cache_creation_input_tokens=0` 可按其它分量正常计价；只要合法聚合值为正，无论只有 5m、只有 1h、5m+1h 混合或 TTL 明细对象缺失，当前单一 cache-write 价格模型都必须 `usage_validity=valid + billing_status=unpriced + $0`，详情原因固定为 `anthropic_cache_write_ttl_unexpressible`，不得给其它分量部分收费。missing/invalid/歧义价格仍分别保持既定状态。
- OpenAI Responses `response.created` 的 `response.usage:null → response.completed` 合法 usage、Chat `include_usage` 的普通 chunk `usage:null → 最后 chunk` 合法 usage：null 不污染三态，最终可正常计费；`usage` 非 null 非法容器或负数/错误类型字段 → 后续合法 usage 仍粘性 invalid、`usage_unknown + 0`；全程只有 null 而没有合法最终 usage 则 missing、`usage_unknown + 0`。
- 通用代理令牌 + 合法 usage + 目录无价（并分别覆盖价格歧义、非 USD/未知单位中的至少一项）必须是 `billing_status=unpriced`、`billed_micros=0`、渠道 used 不增加；不得写成 `usage_unknown`，也不得因主体类型覆盖为 `admin`。另以无/非法 usage 固定为 `usage_unknown`，以合法 usage + 可计价格固定为 `admin`，证明三层判定顺序唯一。
- 固定价格目录代次交错：Request R 取得 `PricingSnapshot=P1`，渠道 A 的 processing Attempt INSERT 后把 live 目录改为 P2 并发布；A 的长 SSE 仍按已保存 `price_*` 结算，随后 R fallback 到渠道 B 时也只能从 R 的 P1 解析并在 B 的 INSERT 持久化。改价发布后新建 Request R2 才使用 P2。结算路径和 `CloseInterruptedWork` 均不得查询 live 目录；“Attempt 在改价后创建”不等于允许读取实时价格。
- 同一 Attempt 重复结算不重复扣费；密钥和渠道只在同一事务双边更新，任一 overflow 时两边均 no-op 并写 `amount_overflow`。
- 用真实可计价 Attempt 把列表密钥从 `used=90, enabled` 结算到 `used=110, disabled/quota_exhausted`，并同时验证渠道 used/门闩、Attempt/Request 金额和管理一致读取；该端到端场景只在 C 记为真实跨限验收，B 的受控事务夹具不替代它。
- 列表密钥 used 跨限结算与 `BeginProxyRequest` 当前复核按 SQLite COMMIT 顺序线性化：Begin 先完成的请求作为在途可继续，结算先提交时紧邻请求必须 `403 key_disabled/quota_exhausted`；结算不要求刷新全量 RuntimeSnapshot，认证索引不得缓存为最终 quota 判定。
- 复用 B 的额度管理反馈环确认 `limit=0` 为不限额；提高额度、改 0 或 reset usage 只按 0.6 解除 quota reason，不解除 manual，也不修改渠道健康。C 只验证这些操作与已实现的真实结算结果衔接，不重复归属额度 API。
- wire 覆盖超过 `2^53` 与 `int64` 最大值的规范字符串往返，前端不使用 `Number` 作为金额权威。模型价格编辑器定向覆盖长小数 `0.12345678901234567890123456789` 的输入→提交→重新打开无精度变化；清空提交 `""` 并显示缺价，显式输入 `"0"` 才表示免费；四项价格的排序、范围最小/最大值使用十进制比较 helper，不经过 `InputNumber`、`Number` 或 `Math.min/Math.max`。

### 工作包 D — 全局/渠道敏感词与 PrepareAttempt

**实现范围**

- 实现一次 body 读取、逐字符串规范化、多 owner Aho-Corasick 编译与全局 `ContentFilterSnapshot`；客户端快照先于全局过滤。
- 在 B 已建立的 `PrepareAttempt` 深模块中接入渠道词表，把已有的精确代次凭证和可取消准入 reservation 与 `AttemptLease` 提升保持为同一个接口。
- 调用方只处理已携带凭证的 `AttemptLease`、`Skipped(reason)`、`ContentRejected` 和 error；渠道词表命中整次拒绝，不再 fallback、本候选不建 Attempt，reservation 释放且不消费半开结果。先前 fallback 尝试若已结算，不撤销其 Attempt/费用/健康。
- 补齐全局/渠道词表 UI、配置 schema 9 / migration v2 往返、请求结构化过滤字段和路由事件。

**D gate**

- 人工禁用、健康禁用、额度耗尽、凭证不可用或并发已满的渠道不会执行词表，也不能拦截请求；随后可承接渠道可正常接管。
- 在预检查后制造并发占满或半开竞争失败时，未取得 reservation 的候选不得先返回 `ContentRejected`；只有已原子取得且随后无副作用释放 reservation 的渠道词表可以拒绝整次请求。
- 可承接渠道词表命中返回固定 400，客户端看不到命中词，管理员账本能看到层级、词和渠道；本候选无 Attempt/费用/健康结算，不再 fallback。全局过滤在首个尝试前命中才是整次请求无 Attempt/费用。
- 渠道 A 已完成 Attempt、计费与健康结算并允许 fallback，渠道 B 获得 reservation 后命中词表：B 无 Attempt、本候选零费用且不继续尝试 C；请求终态 `content_filtered`，A 的 Attempt、健康/费用和既有路由事件保留，`attempt_count` 与请求金额按 A 的账本汇总。
- 半开过滤释放必须复用 B 的 owner-aware 原语：B 候选取得 `(identity-A,G1,O1)` 后命中或取消只能清 O1；在释放前先让管理状态变更产生 G2 并由另一请求领取 O2，G1 的过滤取消、not-sent 或迟到兜底都不得清除 O2，第二个 G2 领取仍返回 `half_open_claimed`，直到 O2 自己结算或释放。
- NFKC、小写、删除全部空白与逐字段不拼接规则通过最小临时反馈环。普通文本 `bad ` 重复 100 次、可成功 base64 解码的长文本及普通文本中的 `data:image/` 前缀都必须扫描并命中；三个协议合法图片块的指定载荷字段按 0.8 跳过，相邻文本仍扫描。将相同对象放到 tool 参数或未知路径不能取得图片豁免；错误类型、非图片 MIME、缺失分隔或无效编码不按图片跳过。
- 多字段命中时，仅改变 JSON 对象键书写/插入顺序，global 与指定渠道 owner 仍选中同一词；嵌套对象按键字节序、数组按原下标、字符串按规范化后 Unicode 起点比较，同起点长短词按词 Unicode 顺序决胜，不能直接取自动机第一个输出。该反馈环同时确认字段之间不拼接。
- 8 MiB 规范化正文、200 条全局词和多个 200 条渠道词表的最坏输入只遍历正文一次；候选渠道数量增长不得让正文扫描次数或规范化字节数增长。10,000 模式/1 MiB 模式字节、100,000 owner-pattern 关联/4 MiB owner 索引四项预算边界分别通过，超限保持旧配置；大量渠道共享同一个命中模式时 `FilterEvaluation` 只记录 pattern，不展开全部 owner，单个候选只做自身倒排集合求交。取消 context 后在最多下一个 64 KiB 检查点退出，并记录目标平台耗时基线防止超线性退化。
- 导入 `contentFilter.enabled=true` 后立即实际生效，不存在“能保存但热路径未接入”的中间发布状态。

### 工作包 E — 同实例恢复点、最终 UI 与正式发布 gate

**实现范围**

- 在 A 的 `RecoveryPointStore` 内核上接入正式管理 API，补齐 GET 列表、DELETE、显式恢复和对应 UI；不得在 handler 重写第二套恢复/删除状态机。
- 创建用 `.creating-<backupId>`、短临界区 capture fence、独立只读连接固定源事务后执行 online backup、从快照产物收集 refs 后原子收窄为精确 pin、锁外正文复制/bundle 加密和 committed 原子发布；列表只显示 committed。
- 删除用 deletion journal、逻辑隐藏、精确 artifact/bundleKeyRef 清理和启动幂等续作；不扫描猜测密钥环。
- 恢复在显式 draining/停服维护状态中执行，复用 sidecar + SQLite commit marker + 正文目录状态机；成功发布完整新快照，失败按提交点回旧或前滚。`committed` 仍保持 draining 并完成恢复最终化，随后原子进入 `cleanup_pending` 才可恢复服务；此时旧正文目录由同一 sidecar `oldContentCleanup` 持久接管，只阻止下一次 Restore，不能降级成仅日志告警，也不得再用旧恢复清单约束 live。
- 完成恢复点 UI、八项顶栏的桌面/390px 响应式收尾、明暗主题和 Windows/macOS/Linux 发布包回归。

**E gate / 阶段 13 发布门槛**

以下本机功能、并发、故障恢复与 UI 项随 E 实现验证；明确要求 Windows/macOS/Linux 原生环境、跨平台发布包及阶段 9/11 的剩余验收，在本地功能收口后集中执行。缺少平台环境时保留待办，不阻止其它本地项完成。此处只调整执行时点，不删除或降低下列发布门槛；迁移互导使用 schema 28 / 配置 schema 9 / migration v2 新数据，不增加旧版本兼容。

- Windows/macOS/Linux 均复跑 A5 单文件封存反馈环：WAL 来源的快照净化后只靠主文件即可读取；连续 Verify/Restore（含失败返回）不改变原 artifact 文件集合、大小/hash，不新增旁路文件；封存/校验/fsync 失败不发布，creating abort 与 Delete 均精确清理登记的 SQLite 旁路路径且不影响第二份恢复点或 live。
- 连续创建两份恢复点后列表均可见；删除第一份后其 artifact 和精确 bundleKeyRef 最终清理，第二份仍可校验和恢复。创建/删除中断由唯一启动清单在 restore finalization 后调用 `RecoverArtifactJournals` 续作；被 journal 持久接管的物理删除可后台重试，未接管或身份不一致则阻止监听。
- creating 各崩溃点不产生可见半成品且 pins 最终释放；deletion journal 在逻辑隐藏、各 artifact 删除和 bundle key 删除后的中断均不让恢复点重新可见，连续两次重启可幂等完成；恢复各切换点只能得到完整旧或完整新集合。
- 恢复的原 SQLite artifact/manifest/bundle 全程只读；工作副本路径、状态和最终 sha256 由 sidecar 持久负责。`secretBindings` 对 channel 使用 id+identity、对 proxy key/content blob 使用稳定主键，并与源 SQLite/bundle、新库逐 owner 双向全等。分别在业务 COMMIT 后立即崩溃、sidecar `databaseSwitched` 写入前后和 committed finalization 前崩溃，重启都可从 sidecar 重建校验，不依赖进程内 map；把任意两个 owner 的 stagedRef 交换，即使两者都存在可读，也必须拒绝启动/开放服务。工作副本在进入 cleanup_pending 前精确删除并 fsync；文件已删但 sidecar 未写 deleted 的中断可幂等收口，路径越界不得清理。
- 恢复点包含 tombstone 密钥及 processing Request 时，必须先完成 `CloseInterruptedWork` 和 committed 新集合校验；在 `CleanupTombstonedKeys` 前后、`FinalizePendingRestore` 前后分别注入中断，连续重启仍能完成恢复。finalization 前 tombstone 行和 secret 不得被物理清理；进入 `cleanup_pending` 后即使密钥已合法删除，重启也只按当前数据库和 cleanup sidecar 启动，不再按旧 manifest/ref 集合误判损坏。
- 冷启动 committed 最终化反馈环固定覆盖：在线恢复写 committed 后、发布新 runtime snapshot 前崩溃；重启 pre-runtime 校验完成并返回 capability 后崩溃；初始 snapshot 构建失败；发布成功后、capability 写 cleanup_pending 前崩溃；写 cleanup_pending 后立即崩溃。前四类重启都必须仍从 committed 重建 capability 和当前初始 snapshot，绝不能提前开放端口或跳过发布；最后一类只续作旧目录垃圾清理。receipt/opId/digest 不一致和 capability 重复消费均 fail-closed。成功路径必须证明 `server.Run`/后台任务只在 finalization 之后启动。
- 大正文 capture 与 restore 的确定性交错满足 A gate：capture 未关闭时 restore 只等待，超时零提交；capture committed/abort 后幂等释放 lease/pin，随后 restore 才可重新进入 draining，不能在复制中切换 live 目录。
- 大库 online backup 期间持续发起代理请求和 Attempt 结算，二者不得因占用业务主单连接而停顿；独立源连接以真实读取固定只读事务后，分页间持续业务提交也不能重启 backup，必须在有限 Step 内完成。取消、错误和 120 秒总时限出口必须完成 backup/事务/连接释放，避免 WAL 无界保留。capture fence 从宽删除保护收窄为精确 pin 的前后交错，不得漏复制快照正文/secret，也不得永久阻止无关清理。另固定“deleting 已取得 owner 且文件已删、行尚在 → 开始 capture”：先行删除必须继续完成，capture 在其收口前不得启动 SQLite backup 或把该行当作可 pin 资源；等待超时只产生可重试失败且不发布恢复点，正文查看同样不得读取该残缺资源。
- retired cleanup 遇到恢复点 `SecretReadLease`、正文 read pin 或其它资源 pin 时必须跳过并重试；释放后只删除精确 ref，不影响其它恢复点或当前库引用。
- namespace、SQLite binding、authority 与规范化真实路径均未变化的原路径绑定实例可恢复；移动/复制到不同真实路径、authority 损坏、空目录重建均明确拒绝。跨机器复制完整目录到相同绝对路径不受支持但当前不保证检测，UI 只能标注“原路径绑定实例回滚，不是迁移或可携带灾备”，不得承诺所有移动/复制都必然拒绝。
- 恢复点临时反馈环分别验证：enabled 普通渠道无 ref、bundle 缺 ref 或 bundle 凭证为空/无效时，创建不得发布 committed，Verify/Restore 在 sidecar 与 live 变更前拒绝；disabled + 无 ref 与 disabled/无凭证/无组的日志占位仍可恢复，safe 占位密钥仍按其独立规则恢复；历史 ref 已从当前 Store 清理但恢复点 bundle 完整时仍可恢复。另固定“请求完成 → tombstone 并物理清理列表密钥 → 删除非 default 访问组 → Create/Verify/Restore”：终态请求保留的 key/group id/name 快照不得导致校验失败或补建对象，恢复后历史仍可展示；processing Request + tombstone 密钥行合法，processing Request + 非空 key id 但密钥行缺失必须在 live 切换前拒绝。
- 对经额度重置/调高后仍为 `disabled + manual` 的 safe 占位密钥创建恢复点、校验和恢复均通过；篡改为 `enabled + pending:<id> + 空 ref` 的快照在 live 切换前拒绝，不把非法占位启用态悄悄规范化。
- 恢复 drain 超时零提交；普通 CRUD、令牌修改、密钥删除、恢复点创建/删除不会让站点进入 draining，也不持全局锁跨文件/网络 I/O。
- 在线恢复与自动/手动日志清理并发时，draining 必须等待“正文→父 Request”完整 maintenance operation 或超时零提交；恢复后即使重新取得相同可读 requestId，旧集合的清理也不得删除新库记录。恢复同时取消运行日志订阅而不等待长 SSE；恢复发布的管理令牌代次生效后旧连接不得收到新运行日志，当前令牌连接可重连。
- restore 在锁外 staging 期间崩溃不得读取或覆盖 current live；三个 previousRef 全部读回验证并持久化 `previousCaptured=true` 后才可进入 switching。启动收口只使用 sidecar 的 opId ref，不能回退到固定 staging/previous 名称。
- 无正文引用且首次没有 `liveContentDir` 时，恢复 prepare 必须在 staging sidecar 发布前 fsync 新建的空 live 及父目录；分别在「空目录持久化后但 sidecar 前」和「sidecar 落盘后立即中断」重启，均不得误判为损坏，旧库仍可用。有正文引用却缺目录必须在发布 sidecar 前拒绝，不得自动补空目录掩盖损坏。
- 半开验证占用期间创建恢复点，待旧请求结束后在线恢复该点：恢复的新库在仍处 draining 时按 2.4.1 原子关闭历史 processing，并清失主的 `half_open_claimed + half_open_claim_owner`；普通旧 lease 的迟到结算不能清掉恢复后新领取的 owner，随后同一半开渠道可重新领取**一次**验证。失败计数、冷却、健康状态和额度保持快照语义，不得用 `ResetChannelHealth` 清空。在线收口失败保持 fail-closed；启动从 commit marker 前滚后也复用同一收口，重复执行幂等。
- 正文资源 creating journal 必须覆盖 `Store.Put` 已成功但无业务行、final 文件已 rename 但无业务行、业务行已提交但 journal 未删；重启按精确行判定，未提交时删除文件和 key ref，已提交时只清 journal。暂停在业务行 COMMIT 后、creating owner 收口前并发触发所有清理入口，deleting 不得覆盖 creating；接管后两个 journal 代次的迟到收尾只能删除自己的 kind+operationId。deleting journal 必须覆盖刚发布、文件已删、行已删、key 已删和 journal 删除前各中断点；重启只前滚同一 owner 的删除，资源 not-found 幂等成功，仍有恢复点/read pin 时继续保留 journal。连续重启不得删除合法 creating 资源，也不得把 deleting 中间态判为损坏。
- 恢复点含已有 `hourly_metrics` 的完整小时及属于该小时的 processing Request（可含 processing Attempt），在线恢复后 `CloseInterruptedWork` 必须同事务标脏原请求 `started_at` 小时；另构造父 Request 已是 success/error/partial、一个 Attempt 已计费且另一个仍 processing 的恢复点，收口必须保留父响应终态和已计费金额，只关闭子 Attempt 并把父 attempt_count/计费聚合修正为 mixed。后台 `AggregateMissingMetrics` 重算出与最终 Attempt/Request 一致的统计，重复在线/启动收口无状态变化时不增加 dirty revision、金额不变且不重复扣额度。不能仅因恢复点带着已有聚合而跳过重算。
- 恢复成功后固定令牌立即切到恢复点集合，响应通过 `authTokenHandoffEnvelope` 切换当前页面；不得因前端未收到响应而恢复旧服务端令牌。
- 在新集合已 committed 后注入 `oldContentDir` 删除失败：恢复最终化完成后必须先原子进入 `phase=cleanup_pending`，sidecar/marker 保留且 `oldContentCleanup=pending`，服务可继续但下一次 Restore 返回 `restore_cleanup_pending`。随后正常请求产生新正文、保留期/容量清理删除恢复点原有正文，并至少执行一次 complete/migration 配置导入或固定令牌修改，再重启：启动必须只按当前数据库做一般校验和精确旧目录清理，不得按旧恢复点 manifest/fixed secret/ref/configDigest 拒绝启动、重放恢复或回滚后续业务状态；配置替换保留提交序列点仍有效的 marker，cleanup 删除 marker 后并发/后续导入不得用 prepare 缓存将其复活。在线、后台重试和冷启动三种入口必须调用同一内部顺序，只删除 sidecar 中经 opId/根路径校验的旧目录，绝不触碰新的 live/staged/数据根：删除成功或 not-found → 父目录 fsync → sidecar 原子写 `oldContentCleanup=done` 并 fsync → 删 marker → 删 sidecar 并 fsync。三种入口都分别在物理删除后写 done 前、写 done 后删 marker 前、删 marker 后删 sidecar 前中断，均可幂等收口且不积累第二个旧目录；任何入口都不能从 pending 直接删 marker。
- 配置 schema 9、迁移包 v2、同实例恢复点、访问组、密钥、额度和过滤器完成整套往返；A–E 的临时反馈环均复跑通过且临时文件已删除。
- 八个菜单在 390px 无横向溢出且可操作；Windows、macOS、Linux 定义的发布 smoke test 和阶段 9/11 回归完成。
- 运行 `go test ./...`、`go test -race ./...`、`go vet ./...`、`npm run typecheck`、`npm run check`、正式前端构建、Markdown/`git diff --check HEAD` 和三平台 smoke test；命令全部为当前工作树绿灯且 A–E 验收没有未解决项后，才可把阶段 13 标为实现完成并解除发布禁令。Go 永久测试文件仍不入库，行为 gate 必须保留临时反馈环执行记录，不能用 `[no test files]` 代替。

## 9. 明确不做

- 用户、团队、RBAC、充值、多租户。
- 密钥多分组、渠道↔密钥直接绑定表。
- 组级额度、组级并发、用组改渠道优先级。
- 请求协议加 group 字段。
- 渠道多把上游凭证。
- 复活 `route_groups`。
- TPM / RPM / 请求次数额度。
- 密钥超限去改渠道；渠道超限去改密钥。
- 导入或修复非 28 数据库、把旧 `group_name` 当访问组。
- 通用令牌进入密钥列表。
- 为额度加大 SQLite 连接数。
- 用探针恢复密钥或恢复手动禁用渠道。
- 设置页保留「轮换令牌」按钮（本计划删除这两个按钮）。
- 为自动禁用新增 `health_state` 枚举值。
- 用户填写正则 / 拼音谐音 / 第三方审核云。
- 敏感词命中后换渠道继续发。
- 把命中词回给客户端。
- 渠道编辑「模型必须已在目录中」（后续）。
- 把空白「折叠成一个空格」当敏感词规范化（已改为删除全部空白）。
- 用 SQLite `REAL` 或 Go `float64` 做计费权威值（额度为微美元整数；目录与 Attempt 单价快照为规范十进制字符串）。
- 用 `InputNumber` / `Number` / `Math.min` / `Math.max` 作为模型价格编辑或比较权威，或把清空价格自动写成免费 `0`。
- 把 Anthropic 正数 `cache_creation_input_tokens` 无条件套进单一 cache-write 单价，尤其是忽略 5m/1h 混合 TTL 后仍标记 `priced`。
- 停用 `default` 组；为损坏或旧渠道静默补 default。
- 把旧无主体 `response_affinity` 行归给所有主体。
- 复用已删除列表密钥的 `proxy_keys.id`，或在物理删除密钥时保留其 `response_affinity`。
- 让 handler、`configreplace`、`RecoveryPointStore` 或其它消费者取得可执行任意 SQL / `func(tx)` 的通用 short/draining commit runner；只能注入各自的类型化窄 façade。
- 用 `0`、空值或 normalization/fallback 副作用推断流空闲继承；继承只能由 `stream_idle_timeout_mode=inherit` 明确表达。
- 顶栏窄屏汉堡菜单或横向滚动（已改为两行换行）。
- 分三次发布都叫 schema 28 的迁移。
- 兼容配置 schema 7/8、migration v1、备份 schema 0。
- 导入 HTTP 响应回显 token 明文。
- 把 JSON 字符串拼成一个大字符串再做敏感词匹配。
- 按普通字符串长度、字符集、能否 base64 解码或 data URL 前缀推测二进制并跳过敏感词；图片豁免只认协议位置和字段白名单。
- 在已有 RequestLease 的同步正文容量 trim 中重新申请顶层 maintenance lease，或让容量清理在父 lease 释放后继续。
- 全局模型映射只更新映射表/缓存而不在同一事务使所属协议族普通渠道换代。
- 改 `classifyProxyError` 的策略分类来换展示文案。
- 先让列表密钥鉴权、后接组过滤作为可运行中间版本。
- 分组页给不满足新数据约束的渠道隐式补 default 成员。
- 同实例恢复点只存 SQLite、不存加密 secret bundle 却仍提供恢复。
- 把 `Store.Get` 原始字节写成磁盘明文当作备份。
- journal 覆盖 live 后不保留 previousRef，导致无法回到完整旧集合。
- 导入时先改 live 再提交目录，或目录提交后再单独写 `auth.tokens`。
- 导入失败只用 `RestoreDatabaseSnapshot`，或自动恢复捕获更早的恢复点并抹掉期间新增日志；提交前必须 abort，提交后必须只前滚。
- 一槽新一槽旧时带着半套继续运行。
- 把「组有效但模型不匹配」记成 `group_mismatch`。
- 在 A–E 全部 gate、响应式和三平台验收完成前，把 schema 28 中间构建当可发布版本。
- 恢复成功后只回 `restartRequired`、继续用旧内存接请求。
- 用 sidecar 的 `databaseSwitched` 单独判断 SQLite 是否 COMMIT（必须核对同事务 commit marker）。
- 把非空 `oldContentDir` 直接 rename 覆盖非空 live 目录。
- 用渠道形态猜历史占位身份，或只把占位 id 暂存在内存。
- 渠道额度用尽时改写 `health_state` / `last_error_class`，或重置额度时恢复健康。
- 同一模型命中多个 provider 时按字母顺序任取价格。
- 接受额度金额与密钥 status/reason 相互矛盾的导入包。
- 新增未加密的 `mode=complete`，或完整导出时静默遗漏没有 secret 的占位密钥。
- 把裸 `Lock/RLock/Unlock`、锁顺序、“调用方已持锁”或 `...Locked` helper 暴露为跨模块接口，或跨网络/SSE/文件复制持全局 mutex。
- 在请求体读取期间持 `RequestLease`，或不经 `BeginProxyRequest` 当前代次复核就让旧主体快照跨越整包替换。
- 管理提交时不按当前令牌重新鉴权，或用初次鉴权的旧 context 包装、导出、恢复或修改新一代数据。
- 普通管理读请求只依赖 middleware 初次鉴权，或在当前令牌复核之外读取 secret、派生 envelope、复制新代次数据。
- 让两个数据目录共用未命名的固定 secret ref；从预放文件复用 namespace；不校验当前真实目录、namespace 文件与 SQLite binding；或把 `instanceSecretNamespace` / `instance.secret_binding` 当可导入、可备份恢复配置覆盖目标实例。
- `Store.Put` 降级成功后仍让 Get 优先读取另一后端，或在权威后端不可用、generation 不符时静默回退到旧副本；在 authority、pending cleanup、日志或其他明文元数据中保存 `sha256(value)` 等秘密猜测校验值。
- 要求 safe 占位密钥必须有明文才能创建或恢复同实例恢复点，或在 restore 模式把合法 `pending:<id>` 当损坏数据。
- 物理删除仍被 `RequestLease` 或 processing request/Attempt 引用的列表密钥，或让 DELETE 等待整个流结束。
- 对人工禁用、健康禁用、额度耗尽、凭证不可用或并发已满的渠道执行渠道词表并据此拦截整次请求。
- 创建恢复点时持全局锁复制正文/加密 bundle，或删除恢复点时没有 deletion journal、按前缀扫描密钥环。
- 渠道凭证短提交后立即删除旧 ref，或让代理 handler 在 `PrepareAttempt` 返回后再按 ref 读取 secret。
- 让旧 snapshot 的 Base URL/凭证领取当前配置的 `health_version` 或半开 owner，或只校验 identity/health version 就把旧配置结果写入新配置健康状态。
- 把同实例恢复点宣传成可应对数据目录丢失、换路径、authority 损坏或空目录重建的通用灾备，或声称当前仅靠 namespace + 规范化路径 binding 就能检测并拒绝所有跨机器同路径复制。
- 把 `limit=0` 的密钥标成 `quota_exhausted`，或让原 `quota_exhausted` 密钥改为不限额后仍保持额度停用。
- 在管理 HTTP、配置/迁移 wire 中把微美元金额编码为 JSON number，或在前端用 `Number × 1_000_000` 计算额度。
- 只等待紧邻旧 generation 就删除 retired ref，或忽略仍引用该 ref 的更老 Request/system/admin operation snapshot。
- 自动探针、手动测试、已保存渠道模型发现、渠道复制或正文读取绕过 coordinator 语义操作直接查询 live ref、调用 `Store.Get` 或复用 middleware 旧鉴权 context。
- 用未检查的 SQLite `SUM(INTEGER)` 直接回写请求金额。
- SQLite 已提交 expectedHash 后仍用 previousRef 回滚固定令牌。
- 只在内存状态或 `SecretRefLifecycle` JSON 中表示 `database_committed`，复用 `auth.token_journal` 代替普通 provisioning 提交证据，或在配置/迁移包、恢复点快照中携带源实例 `provisioning_batches` 行。
- 把 invalid usage 当成 missing/0，或让后续合法 SSE 帧覆盖此前 invalid 状态。
- 对金额乘法、求和或 used 累加使用未检查的 `int64` 运算。
- 有正文引用但缺 `content-master` 时自动生成新主密钥。
- 恢复旧 live 后未先持久化 `rolled_back` 就删除最后一个 staged 证据。
---

## 10. 实现时的编码约定

- 成员替换：删旧行再插入，不做差分框架。
- 校验：名称非空、组名唯一、渠道 id 存在、保存渠道时 `groupIds` 含 default、密钥总长 24、额度 ≥ 0、导入的 status/reason/limit/used 交叉一致。
- 热路径：用不可变认证索引完成无 lease 的轻量 Bearer 鉴权并解析 keyId，无效令牌不读 body；随后限长/限时读取 body，再由 `BeginProxyRequest` 查询 SQLite 当前 tombstone/hash/manual/quota/group，并在同一序列点登记 lease、返回不可变 `RuntimeSnapshot + RequestLease`；完整 `RouteScope` 贯穿刷新/fallback/affinity；所有主体硬排除日志占位渠道。
- 渠道 bundle 与分组页写同一张成员表。
- 列表密钥和渠道凭证的新 ref 统一走 `SecretRefLifecycle` provisional/retired 账本；消费 provisional 的事务同时写专用 `provisioning_batches` committed 行，禁止把提交证据塞进 lifecycle JSON。管理/代理令牌用双槽 `auth.token_journal`（含 previousRef 与导入）。导入时目录与 `auth.tokens` 同一事务，live 在事务后拷；数据库哈希未提交才回滚，已提交只前滚。
- 非默认组成员替换不得隐式补 default。
- 列表密钥鉴权与组过滤必须同一发布门槛。
- 迁移严格解析与配置导出白名单分开维护，改一处必须改另一处。
- 三类令牌两两 hash 唯一。
- `RuntimeCoordinator` 是运行时代次、短提交和 draining 的唯一接口：管理请求锁外 prepare，提交时按当前令牌重鉴权；配置/迁移整包替换和恢复走有时限 draining，普通 CRUD、固定令牌修改、密钥 tombstone、恢复点创建/删除不 drain。恢复点创建只经自持 operation lease 的 `RecoveryPointCapture`，`RecoveryPointStore.Create` 必须立即 `defer` 幂等 `Close()`；外部不得感知内部锁或单独释放该 lease。
- 管理读路径同样经 coordinator 当前令牌复核；secret envelope 的目标解析、精确 generation 读取与 Bearer 派生必须属于一次语义操作。渠道凭证更新使用新 ref，retired ref 按所有仍引用它的活跃 snapshot 延迟清理，不绑定单一 generation；自动探针和跨网络/文件管理读取使用不持 mutex 的 operation lease。
- `secret.Store` 调用方只使用逻辑 ref；启动先校验 namespace 文件、SQLite 本机 binding 与规范化真实目录身份，之后实例 namespace 映射、仅含 backend + 随机 generation 的 authority、后端内部封装、Put 读回验证和 Get generation/内部摘要校验都封装在 store 模块。SQLite 只允许保存不进入任何包/备份的 `instance.secret_binding`，不得保存物理 ref；包、备份、authority 与日志不得保存物理 ref 或可离线验证秘密猜测的摘要。
- safe 占位密钥在 restore 校验中是独立合法形态；complete/迁移仍必须全部物化。列表密钥删除先 tombstone，最后一个 lease/processing Attempt 结束后才物理清理。
- 所有 `*Micros` wire 字段为规范十进制字符串；前端精确换算；Attempt、主体 used 和请求合计分别检查 `int64` 溢出。
- restore journal 只放 `{dataDirectory}/restore.journal`；SQLite 中与恢复进度有关的状态只保留与表替换同事务的 `restore.commit_marker`，与之正交的 `instance.secret_binding` 始终保留目标值。`RecoverPendingBeforeRuntime` 在加载设置和认证之前核对 sidecar 与 marker；若返回 committed capability，则必须在当前初始 snapshot 发布后经 coordinator 门面 finalization，最后才允许监听。
- restore sidecar 每次用同目录临时文件 + fsync + 跨平台原子替换 + 父目录同步更新；新 ref 先登记再 Put，切库前记录旧非固定 ref，提交后验证完整新集合再清理旧集合。
- `content-master` 覆盖 live 仅允许 SQLite commit marker 与 sidecar 的 opId/generation/backupId 一致之后；previous 丢失且对不上则阻止启动。
- 同实例恢复点 `bundleKeyRef = backup-bundle-key:<backupId>`，禁止按目录名封装；创建、删除和启动收口只能经 `RecoveryPointStore`。
- 恢复点 restore sidecar 的 `RecoverPendingBeforeRuntime` 与恢复点自身 artifact 的 `RecoverArtifactJournals` 是同一深模块内的两类责任：前者在普通启动收口前确定 live 集合，后者只在 restore finalization 后收口 `.creating-*` / deletion artifact；不得用一个含糊的 `RecoverPending` 隐藏两种顺序。
- 同实例恢复点 manifest 保存并用 AEAD AAD 绑定当前 `instanceNamespace + dataDirectoryBinding`；restore previous/staging ref 全部由 sidecar `opId` 派生，`previousCaptured=true` 是进入 switching 的硬门槛。
- Attempt 计费先判定 usage、再判定价格、最后标主体：missing/invalid 是 `usage_unknown`，usage valid 但价格不可计是 `unpriced`，只有可计价通用令牌才是 `admin`。
- usage 保留 missing/valid/invalid，invalid 在流中粘性传播；金额进入 `int64` 或累加前必须检查范围，overflow 原子写显式状态且额度更新 no-op。
- 启动恢复后先 `EnsureContentMasterKey`；同实例恢复先跑共享领域校验；回旧先持久化 `rolled_back` 再删 staged 资源。
- complete 配置的 HTTP mode 固定为 `complete_encrypted`；没有 secret 的占位密钥先轮换，否则完整导出 409。
- 日志占位渠道必须用 `is_history_placeholder` 持久标识；该字段只读，导出、路由、管理 API、UI 禁止猜形态或从普通入口清除。

---

## 11. 实施状态

阶段 13 当前为**可进入本地实施、尚未实现，实施契约须逐项验证**。本文正文是阶段 13 的唯一详细实施规格；按 2026-09-19 用户确定的顺序，从 A1 开始，阶段 9/11 剩余跨平台验收后置到 E 最终发布 gate。A–E 全部完成且全部发布门槛通过前，不得创建正式数据、标记整个阶段完成或发布。后续方案变化必须直接同步主计划和本文对应的规范章节。

实施者在此维护进度，规范章节只因契约变更而修改，不能用进度记录改写验收标准。每个检查点记录：当前状态、改动文件与关键入口、验收条目及夹具重建步骤、执行命令/结果、失败或待办、下一步。临时反馈环记录输入、故障注入点和预期/实际输出，排除令牌明文；删除测试文件后仍应能按记录重建验证。跨平台项分别记录 OS/架构、产物版本、已执行场景和待补证据。

| 检查点 | 当前状态 | 已验证证据 / 下一步 |
| --- | --- | --- |
| A1 | 未开始 | 本次仅调整实施顺序与执行入口；下一步建立隔离 DDL/preflight 反馈环 |
| A2–A5 | 未开始 | 依次完成对应模块与本地 gate |
| B / C / D | 未开始 | 按前置工作包本地 gate 顺序进入，实施时分别登记 |
| E 本地 | 未开始 | 恢复 API/UI、全量本机回归与响应式验收 |
| E 跨平台发布 | 后置待执行 | 阶段 9/11 剩余平台验收与阶段 13 三平台矩阵，不阻塞本地实施 |
