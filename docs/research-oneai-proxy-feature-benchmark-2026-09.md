# OneAI Proxy 特性基准调研

> 调研日期：2026-09-03
>
> 调研对象：LiteLLM、New API / One API、OpenRouter、Kong AI Gateway、Envoy AI Gateway，以及当前 OneAI Proxy 代码和权威实施计划。
>
> 文档定位：外部能力对照和产品决策输入；不覆盖或改写 [`plans/oneai-proxy-solution-plan.md`](../plans/oneai-proxy-solution-plan.md) 中已经冻结的契约。
> 范围：只比较网关、路由、鉴权、可观测性、缓存、限流、成本和部署等与本项目相关的能力；不把宣传页上未能由官方文档核验的能力计入结论。

## 1. 调研范围与判断口径

本次调研回答三个问题：

1. 同类产品已经解决了哪些问题，OneAI Proxy 当前是否已经具备这些能力。
2. 哪些差距值得在本地单用户代理中补齐，哪些差距会把产品推向 SaaS 或分布式平台。
3. 若要补齐，SQLite、单进程和双本地监听器能否承载，什么时候才需要 Redis、外部数据库或多实例控制面。

能力判断采用以下口径：

- **已有**：当前工作区代码、接口或计划中的完成记录可以直接核对。
- **可借鉴**：上游产品有明确官方文档说明，但不代表 OneAI Proxy 要照搬实现。
- **建议**：结合本项目的单机、本地管理和三协议范围提出的候选项，不是已授权实现。
- **不计入**：只在博客、截图、二手文章或产品宣传语中出现，且没有对应官方 API/配置/行为说明的内容。

外部资料优先使用各项目官方文档、官方仓库 README 和官方 API 参考；文末列出本次核验过的链接。价格、限额和可用 provider 等可能变化的资料只作为当日能力快照，不作为本项目的固定契约。

## 2. 当前 OneAI Proxy 能力基线

权威计划当前为 schema v22。阶段 12 的路由容灾和并发准入核心闭环、阶段 6R 日志最终重构、阶段 10 模型目录和阶段 11 迁移包核心实现已完成；阶段 9 和阶段 11 仍有跨平台发布包/真实环境验收边界。以下是代码中已核对的能力，不是未来路线图：

### 已有的网关和路由能力

- Go 单进程、SQLite 持久化，React 19 + TypeScript 管理台。
- 代理监听默认 `127.0.0.1:9988`，管理监听默认 `127.0.0.1:9989`；两者使用独立的代理令牌和管理令牌。
- 三个独立入口：OpenAI Chat Completions、OpenAI Responses、Anthropic Messages；支持非流式和 SSE。当前不做跨协议转换。
- 同协议渠道按显式优先级排序；同优先级按创建时间和 ID 稳定排序。一次逻辑请求对每个渠道最多一次 Attempt，失败后按请求预算切换候选。
- 渠道级模型映射优先于全局映射，未命中时可使用该渠道兜底模型；Responses 请求还维护渠道亲和。
- 渠道有并发准入、冷却、半开、连续失败阈值、自动禁用和人工重置；探针与业务请求共用准入和恢复状态机。

### 已有的配置、目录和安全能力

- 渠道管理、复制、启停、手动/上游模型目录、渠道模型映射、优先级、重试、失败动作、并发上限、自定义 Header、测试模型和探针策略。
- 全局模型映射、跨渠道标准化模型目录、有限格式的在线同步预览、选择性同步、手工新增和失效删除。
- 配置导入导出、加密备份、迁移包、事务回滚、诊断摘要和本地系统密钥环；管理 API 对密钥凭证脱敏，明确凭证编辑会话才返回秘密。
- 请求、Attempt、路由事件、健康事件、探针、审计和小时聚合指标；请求/响应正文快照加密保存，运行日志写入本地文件并遮罩凭证。

### 明确不存在的能力

当前没有虚拟 key、用户/团队/组织、RBAC、计费或配额分发；没有 Redis、多实例 HA、外部日志回调/Webhook、语义缓存、客户端 `/v1/models` 聚合接口，也没有按实时价格、延迟、权重的智能负载均衡。SQLite 使用单连接约束，渠道并发计数保存在进程内，不能把它描述成跨实例共享限流或高可用控制面。

## 3. 参照产品能力对照

### 3.1 总览矩阵

| 能力 | LiteLLM Proxy | New API / One API | OpenRouter | Kong AI Gateway | Envoy AI Gateway | OneAI Proxy 当前状态 |
| --- | --- | --- | --- | --- | --- | --- |
| 多 provider / 协议适配 | 多 provider、统一 OpenAI 风格入口和多种 endpoint | 多渠道、多协议，社区生态扩展快 | 统一模型目录和 provider 选择 | Universal API、provider 插件和协议适配 | OpenAI/Anthropic 等 endpoint，支持 provider 集成 | 三种独立协议；不做跨协议转换 |
| 路由、重试、故障切换 | priority、weight、fallback、重试、冷却和健康路由 | 渠道排序/加权、失败重试和 fallback | provider order、fallback 开关，可按价格/吞吐/延迟排序 | weighted、round-robin、least-connections、priority、lowest-latency 等 | provider fallback、InferencePool、retry/backoff、failover | 显式优先级、每渠道一次 Attempt、非阻塞 fallback、冷却/半开 |
| 并发和限流 | proxy/channel 最大并发，用户/团队 RPM/TPM 等 | 用户/渠道分组、token 额度和模型限制 | key 限额、模型/账户限制和预算 | Redis token/dollar rate limit、并发等插件 | token-aware rate limit、并发和待处理队列 | 仅进程内按渠道并发准入；无 RPM/TPM、虚拟 key |
| 健康检查和熔断 | health endpoint、定时检查、冷却和自动恢复 | 渠道测试、定时测试、失败率触发禁用 | provider 可用性/限额提示，更多由路由策略处理 | retry/failover/circuit breaker、主动观测 | active/passive health、circuit breaker、连接池 | 主动/手动探针，healthy/degraded/cooldown/half-open/auto-disabled |
| 鉴权和租户 | virtual key、用户/团队预算与权限 | 用户 key、分组和额度，支持多机部署 | 管理 key、BYOK、key 额度/过期/轮换、企业控制 | Consumer Key Auth、RBAC（Konnect） | hostname/声明式多租户与网关策略 | 本地管理令牌和代理令牌；单用户，无 RBAC/租户 |
| 日志和可观测性 | 标准日志、回调、OTel、成本和 token 统计 | 请求日志、统计、渠道成功率与管理 API | Activity 导出、Analytics/成本控制 API | TTFT/TPOT/token/cost 审计日志、插件和 OTel 生态 | OpenTelemetry/OpenInference、访问日志和指标 | 请求/Attempt/路由/健康/探针/审计、加密快照、小时聚合；无外部回调/OTel |
| 缓存 | 精确缓存、语义缓存、Redis 等后端 | 缓存计费和若干缓存选项 | prompt cache、provider cache 语义 | semantic cache 插件 | 未把缓存作为本项目核心能力 | 当前没有语义缓存或响应缓存 |
| 成本和价格 | cost map、custom model cost map、预算 | token 额度/缓存计费等运营能力 | 公开价格、模型目录、Analytics 和预算控制 | 成本优化路由和成本审计 | 可结合路由/指标实现成本治理 | 模型目录可保存费用，仅用于展示；不计费、不排序 |
| 配置和部署 | Redis/数据库/多实例控制面选项 | MySQL/PostgreSQL/Redis、多机部署 | 托管控制面和管理 API | Kubernetes/Konnect、声明式配置 | Kubernetes Gateway/InferencePool | 本地 SQLite、单进程、双回环监听；已有加密迁移和备份 |

矩阵只说明能力类别，不表示各产品在每个类别下提供相同的实现、默认值或授权边界。尤其是 OpenRouter 的 provider 排序与托管额度、Kong/Envoy 的插件和 Kubernetes 控制面，不能直接等价成桌面本地代理功能。

### 3.2 LiteLLM

LiteLLM 的 Proxy 文档把模型调用、路由和运营控制面集中在一个网关中：路由支持优先级、权重、重试、fallback、最大并发和健康检查；Proxy 用户文档支持 virtual key、用户/团队预算以及 RPM/TPM 限制；缓存文档覆盖精确缓存和语义缓存；日志、回调和 OpenTelemetry 文档覆盖外部可观测性；成本文档提供模型价格映射和自定义 cost map；告警文档列出 Slack、Discord、Teams 等通知方式。

对本项目最有参考价值的是“请求预算 + 渠道健康 + 路由事件”这条解释链。OneAI Proxy 已有显式优先级和每渠道单次 Attempt，因此下一步应先增强解释和筛选，而不是直接引入 weight、动态成本排序或 Redis。virtual key、团队预算和 OTel 属于后置的多租户/外部运维方向。

### 3.3 New API / One API

New API 的官方 README 和 One API README 展示了以渠道为中心的多上游代理：支持多协议、渠道负载均衡、渠道测试、失败重试、模型限制、用户 key 和 token 额度，并可用 MySQL/PostgreSQL/Redis 部署到多机环境。其运营模型适合公共服务或团队共享网关，但用户、分组、额度、充值和渠道池并非 OneAI Proxy 的单机目标。

可借鉴的细节是高密度渠道/请求表、渠道测试和成功率/失败原因统计。实现时应保留 OneAI Proxy 已冻结的“每个渠道最多一次 Attempt”和健康状态机，不复制以同渠道重试或商业配额为中心的数据模型。

### 3.4 OpenRouter

OpenRouter 官方 provider-selection 文档支持 provider order 和 fallback；其模型与 pricing 文档提供模型目录和价格信息，BYOK 文档说明可以使用用户自己的 provider key；管理 key、Activity export 和 Analytics 文档覆盖 key 过期/轮换、活动导出、用量和成本控制；limits/FAQ 文档说明额度和限额边界。官方博客还说明可按价格、吞吐、延迟等指标进行模型/provider 路由。

这些能力说明“模型目录 + provider 选择 + 成本可见性”对用户有价值，但 OpenRouter 的托管路由和计费前提与本项目不同。OneAI Proxy 当前只把目录费用当作只读资料，不能在没有可靠实时价格和计费账本时按成本自动选路。

### 3.5 Kong AI Gateway

Kong AI Gateway 文档覆盖 Universal API、多 provider、模型别名、加权/轮询/最少连接/最低延迟/最低用量/优先级路由，以及 retry、failover 和 circuit breaker。插件文档还提供 Consumer Key Auth、Redis token/dollar rate limiting、semantic cache；审计日志参考记录 TTFT、TPOT、token 和 cost，并可接入企业 RBAC、声明式配置和 OTel 生态。

Kong 的可借鉴之处是把路由策略、鉴权、限流、缓存和审计作为可组合插件。对 OneAI Proxy，优先级路由和可解释 Attempt 已满足本地核心问题；插件脚本、Redis 限流和企业 RBAC 会明显扩大运行时和安全边界，暂不引入。

### 3.6 Envoy AI Gateway

Envoy AI Gateway 文档列出 provider fallback、model-name virtualization、OpenAI/Anthropic endpoint、active/passive health、retry/backoff、circuit breaker、InferencePool、token-aware rate limit、并发/待处理队列，以及 OpenTelemetry/OpenInference 观测。Envoy Gateway 还支持 header/body mutation 和声明式扩展。

对本项目最实用的参考是模型名虚拟化、provider fallback 和“并发准入与排队”分开计量。OneAI Proxy 已有渠道级模型映射和非阻塞准入；可以先补充面向用户的路由事件、拒绝/等待计数和模型别名说明，再评估是否需要 header/body 规则。跨 provider translation、Kubernetes InferencePool 和分布式熔断不在当前单机范围。

## 4. 差距与建议优先级

优先级只表示建议顺序，不代表已获得实现授权。复杂度按本项目现有 Go/SQLite/React 结构估算：低为局部 API/UI， 中为新增持久化和迁移，高为需要外部服务或重构运行时。

| 优先级 | 候选能力 | 用户价值 | 复杂度 | SQLite/单实例适配性 | 是否需要 Redis/多实例 |
| --- | --- | --- | --- | --- | --- |
| P0 | 面向客户端的 `/v1/models` 聚合 | SDK/客户端可发现当前可用逻辑模型，减少手工配置 | 中 | 可由渠道目录、映射和健康状态只读聚合；需明确协议和可见性契约 | 否；跨实例一致性是后续问题 |
| P0 | 日志多维筛选、分页和导出 | 能按时间、渠道、模型、协议、状态、错误分类定位故障 | 中 | SQLite 索引和已有请求/Attempt 表足够；导出可流式生成 | 否 |
| P0 | 路由事件与准入可观测性增强 | 解释候选为何跳过、排队/拒绝和 fallback 原因 | 低-中 | 复用已有 route events、health、Attempt；增加查询聚合 | 否 |
| P1 | 价格/成本只读展示增强 | 在模型详情、请求详情和概览中显示可追溯的估算依据 | 中 | 复用模型目录费用和 token 字段；明确“估算、非计费” | 否；实时价格同步可选外部网络 |
| P1 | 单用户 profile key、过期与轻量限额 | 为本机不同脚本/客户端隔离令牌并提供撤销 | 中 | key hash、状态和本地 RPM/请求数可存 SQLite；秘密放密钥环 | 否；多进程共享需重新设计 |
| P1 | 轻量 RPM/TPM 限制 | 防止单个本地客户端占满渠道或触发上游限额 | 中 | 单实例按时间桶/滑动窗口记录即可；需定义拒绝语义 | 多实例才需要 Redis 原子计数 |
| P1/P2 | 确定性 weighted routing | 在同优先级渠道间做可复现分流 | 中 | 单实例可用稳定 hash；需新增配置和路由事件字段 | 否（多实例要共享种子/配置） |
| P2 | 精确响应缓存 | 降低重复请求延迟和上游成本 | 中-高 | 小规模可用 SQLite 加密 blob，并设置严格 TTL/大小上限 | 高吞吐或共享缓存需要 Redis/专用缓存 |
| P2 | Webhook/Slack 等告警 | 上游故障、自动禁用和磁盘配额异常能主动通知 | 中 | 事件队列和发送状态可落 SQLite；外发失败需重试策略 | 否；高可靠多实例建议队列/Redis |
| P2/P3 | 外部 OTel/OpenInference | 接入现有观测平台，统一 trace/metric | 中-高 | 本地可选 OTLP exporter；不应阻塞代理请求 | 多实例观测本身不强制 Redis，但需外部 collector |
| P3 | 语义缓存 | 相似 prompt 复用结果 | 高 | SQLite 不适合作为向量索引和高并发缓存 | 通常需要向量/缓存服务，且有隐私和正确性风险 |
| P3 | 更多协议或 WebSocket | 覆盖更多客户端 | 高 | 每种协议需独立契约、测试夹具和日志字段 | 不必然需要 Redis，但增加发布与维护成本 |

### 建议的最小落地顺序

1. 先完成 `/v1/models`、日志筛选/导出和路由事件可视化；它们直接利用现有目录、日志和健康数据，不改变转发契约。
2. 再讨论单用户 profile key 与轻量 RPM/TPM。两项都应先冻结令牌生命周期、拒绝状态码、计数窗口和审计字段，避免悄然演变成多租户计费。
3. 若确有流量分布需求，再加入确定性 weighted routing；保持显式优先级为默认，并在每次 Attempt 记录选路原因。
4. 缓存、告警和 OTel 作为可选集成，必须有关闭开关、失败隔离和敏感内容边界；不让外部服务故障影响本地代理主路径。

## 5. 不建议现在做

- **Redis/多实例 HA 或迁移到 PostgreSQL/MySQL**：当前进程内 `channelInFlight` 和 SQLite 单连接是明确的单实例设计；在没有多用户、远程管理或高并发部署需求时，引入分布式一致性只会扩大故障面。若未来需要，需先重写租约、健康状态和幂等账本，而不是直接替换存储驱动。
- **RBAC、多租户、用户/团队/组织、计费售卖和商业配额**：这些是 New API、LiteLLM 和 OpenRouter 的服务化能力，会改变密钥、审计、数据隔离和产品定位。当前本地管理令牌/代理令牌模型不应被半成品权限系统取代。
- **跨协议转换和任意脚本改写**：Chat、Responses、Messages 的语义、流事件和错误模型不同；Kong/Envoy 的转换或 body mutation 需要独立契约、沙箱和兼容性矩阵，超出当前范围。
- **按实时成本、延迟或吞吐的智能选路**：价格和基准数据会漂移，且会削弱当前“显式优先级 + 可解释 fallback”契约。先把只读价格和实际延迟展示做好。
- **语义缓存和大规模 prompt cache**：涉及隐私、工具调用、流式响应、模型版本和失效一致性；没有明确缓存键、租户边界和退出策略前，不应默认缓存用户内容。
- **复制完整运营后台**：渠道、日志、模型目录和健康闭环已是本项目核心，不需要复制参照产品的充值、订阅、iframe、企业 SSO 或 Kubernetes 控制面。

## 6. 来源与核验说明

以下链接均为调研时访问的官方文档、官方仓库 README 或官方 API/规范页面；页面内容和产品版本会变化，使用前应重新核对：

### LiteLLM

- [Documentation](https://docs.litellm.ai/docs)
- [Routing](https://docs.litellm.ai/docs/routing)
- [Proxy users](https://docs.litellm.ai/docs/proxy/users)
- [Virtual keys](https://docs.litellm.ai/docs/proxy/virtual_keys)
- [Caching](https://docs.litellm.ai/docs/proxy/caching)
- [Logging and callbacks](https://docs.litellm.ai/docs/proxy/logging)
- [Health checks](https://docs.litellm.ai/docs/proxy/health)
- [Alerting](https://docs.litellm.ai/docs/proxy/alerting)
- [Cost tracking / custom cost map](https://docs.litellm.ai/docs/proxy/cost_tracking)
- [Supported endpoints](https://docs.litellm.ai/docs/supported_endpoints)
- [Multi-tenant architecture](https://docs.litellm.ai/docs/proxy/multi_tenant_architecture)
- [High availability control plane](https://docs.litellm.ai/docs/proxy/high_availability_control_plane)
- [OpenTelemetry integration](https://docs.litellm.ai/docs/observability/opentelemetry_integration)

### New API / One API

- [New API 中文 README](https://raw.githubusercontent.com/QuantumNous/new-api/main/README.zh_CN.md)
- [One API English README](https://raw.githubusercontent.com/songquanpeng/one-api/main/README.en.md)

### OpenRouter

- [Provider selection and routing](https://openrouter.ai/docs/guides/routing/provider-selection)
- [Model routing overview](https://openrouter.ai/blog/insights/model-routing)
- [BYOK](https://openrouter.ai/docs/guides/overview/auth/byok)
- [Limits](https://openrouter.ai/docs/api_reference/limits)
- [Models](https://openrouter.ai/docs/guides/overview/models)
- [Management API keys](https://openrouter.ai/docs/guides/overview/auth/management-api-keys)
- [Activity export](https://openrouter.ai/docs/cookbook/administration/activity-export)
- [Analytics and cost control](https://openrouter.ai/docs/cookbook/administration/analytics-cost-control)
- [Pricing](https://openrouter.ai/pricing)

### Kong AI Gateway

- [AI Gateway overview](https://developer.konghq.com/ai-gateway)
- [AI Proxy Advanced](https://developer.konghq.com/plugins/ai-proxy-advanced)
- [Load balancing](https://developer.konghq.com/ai-gateway/load-balancing)
- [AI rate limiting advanced](https://developer.konghq.com/plugins/ai-rate-limiting-advanced)
- [AI semantic cache](https://developer.konghq.com/plugins/ai-semantic-cache)
- [AI audit log reference](https://developer.konghq.com/ai-gateway/ai-audit-log-reference)
- [Basic LLM routing cookbook](https://developer.konghq.com/cookbooks/basic-llm-routing)
- [LLM cost optimization cookbook](https://developer.konghq.com/cookbooks/llm-cost-optimization)
- [Konnect audit logs](https://developer.konghq.com/konnect-platform/audit-logs)

### Envoy AI Gateway / Envoy Gateway

- [AI Gateway capabilities](https://aigateway.envoyproxy.io/docs/capabilities)
- [Provider fallback](https://aigateway.envoyproxy.io/docs/capabilities/traffic/provider-fallback)
- [Model name virtualization](https://aigateway.envoyproxy.io/docs/capabilities/traffic/model-name-virtualization)
- [Supported endpoints](https://aigateway.envoyproxy.io/docs/next/capabilities/llm-integrations/supported-endpoints)
- [Observability](https://aigateway.envoyproxy.io/docs/capabilities/observability)
- [Release notes v1.0](https://aigateway.envoyproxy.io/release-notes/v1.0)
- [Circuit breaker](https://gateway.envoyproxy.io/docs/tasks/traffic/circuit-breaker)
- [Retry](https://gateway.envoyproxy.io/latest/tasks/traffic/retry)
- [Failover](https://gateway.envoyproxy.io/docs/tasks/traffic/failover)
- [Extension types](https://gateway.envoyproxy.io/latest/api/extension_types)

### 本项目核对范围

本地判断来自 `plans/oneai-proxy-solution-plan.md`，以及 `internal/server/`、`internal/storage/`、`internal/config/config.go` 和 `web/src/` 中的现行实现。写入本文件未修改业务代码、schema、计划或前端资源；如建议进入实现，仍需按权威计划另行授权和验收。
