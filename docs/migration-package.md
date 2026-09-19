# 跨平台迁移包

阶段 11 的迁移包使用单个 `.oneai-migrate` 文件在不同电脑之间搬运 OneAI Proxy 的可迁移配置。

## 使用流程

1. 在源环境的“设置 -> 配置与恢复”输入一次性口令，点击“导出迁移包”。
2. 将生成的 `.oneai-migrate` 文件复制到目标环境。不要复制 SQLite、密钥环、`content/` 或日志文件。
3. 在目标环境选择迁移包并输入相同口令，点击“预览替换”。预览只显示数量、监听地址、时区、令牌是否存在和渠道摘要，不显示密钥明文。
4. 确认替换后点击“确认迁移”。服务会先生成 SQLite 回滚快照，在目标机密钥环创建新的渠道凭证引用，再以单个事务替换可迁移配置。
5. 收到“需要重启”提示后重启应用，使新的监听地址和鉴权令牌生效。

## 安全与保留边界

- 迁移包外层是 `oneai-proxy-migration` JSON 信封，当前迁移版本为 1、配置版本为 8、数据库版本跟随当前 SQLite schema（目前为 27）。载荷 gzip 后使用 Argon2id（`memoryKiB=65536`、`iterations=3`、`parallelism=2`）和 XChaCha20-Poly1305 整体保护。旧包中的 `routeGroups` 会被忽略，不报错、不写入。
- 迁移包包含监听、请求策略、日志策略、渠道及凭证、模型、映射、探针策略、模型目录和两个本地令牌；不包含路由组、`secret_ref`、绝对路径、请求、Attempt、健康运行态、探针运行记录、Responses 亲和、审计、统计、正文、WAL/SHM、缓存、PID 或日志文件。
- 目标机已有请求日志、Attempt、统计、审计和正文保持不变。导入中不存在但仍有历史引用的渠道保留为禁用占位并清空秘密引用。
- 错误口令、篡改包、超大包、未知版本、非法配置和密钥环写入失败均在提交前拒绝或回滚。每个新凭证引用会在写入目标密钥环前先持久化暂存 marker；导入事务提交时将 marker 原子替换为旧凭证待清理清单，提交后清理旧凭证，若进程中断则下次启动按当前数据库引用重试，避免孤立凭证或删除仍被当前数据库引用的秘密。

管理 API 对应路径为：

- `POST /api/admin/v1/migration/export`，JSON `{ "password": "..." }`，返回迁移包文件。
- `POST /api/admin/v1/migration/preview`，请求体为迁移包，使用 `X-OneAI-Migration-Password`，只读返回脱敏预览。
- `POST /api/admin/v1/migration/import`，请求体为迁移包，同时提供 `X-OneAI-Migration-Password` 和 `X-OneAI-Migration-Confirm: true`。
