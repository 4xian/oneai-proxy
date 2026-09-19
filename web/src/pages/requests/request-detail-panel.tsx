import { Button, Collapse, Tag } from '@douyinfe/semi-ui-19'
import { IconClose, IconCopy } from '@douyinfe/semi-icons'
import { protocolLabel } from '../../formatters'
import {
  SnapshotGroup,
  attemptTTFT,
  formatDetailDate,
  formatDuration,
  formatListDate,
  formatNumber,
  formatPercent,
  statusColor,
  statusText,
  type Attempt,
  type Blob,
  type Request,
  type RouteEvent,
} from './request-log-shared'

type Props = {
  selected: Request
  expandedAttempts: string[]
  onExpandedAttemptsChange: (keys: string[]) => void
  clientRequestBlobs: Blob[]
  finalResponseBlobs: Blob[]
  snapshot: Record<string, string>
  onLoadSnapshot: (blob: Blob) => void
  onClose: () => void
  onCopyRequestID: () => void | Promise<void>
}

// 将 Attempt 的触发原因转换为时间线中的可读说明。
function attemptTriggerText(reason?: string): string {
  if (reason === 'channel_switch') return '跨渠道切换'
  return '首次尝试'
}

// 根据下一条 Attempt 判断当前尝试失败后的实际后续动作。
function attemptNextAction(attempts: Request['attempts'], index: number): string {
  const next = attempts[index + 1]
  if (!next) return '结束请求'
  if (next.retryReason === 'channel_switch') return '切换到下一个渠道'
  return '继续下一次 Attempt'
}

type TimelineItem =
  | { key: string; occurredAt: string; attempt: Attempt; event?: never }
  | { key: string; occurredAt: string; attempt?: never; event: RouteEvent }

// 将 Attempt 终态和路由事件按发生时间合并为一条时间线。
function buildRequestTimeline(request: Request): TimelineItem[] {
  const attempts: TimelineItem[] = (request.attempts ?? []).map((attempt) => ({
    key: `attempt-${attempt.id}`,
    occurredAt: attempt.completedAt || attempt.startedAt || request.startedAt,
    attempt,
  }))
  const events: TimelineItem[] = (request.routeEvents ?? []).map((event) => ({
    key: `event-${event.sequence}`,
    occurredAt: event.occurredAt,
    event,
  }))
  return [...attempts, ...events].sort((left, right) => {
    const timeDifference = Date.parse(left.occurredAt) - Date.parse(right.occurredAt)
    if (timeDifference !== 0) return timeDifference
    if (left.attempt && right.event) return -1
    if (left.event && right.attempt) return 1
    return left.key.localeCompare(right.key)
  })
}

// 读取受控路由事件详情字段，并转换成适合展示的文本。
function routeEventDetail(event: RouteEvent, key: string): string {
  const value = event.details?.[key]
  return typeof value === 'string' || typeof value === 'number' ? String(value) : ''
}

// 将路由跳过或耗尽原因转换为中文说明。
function routeReasonText(reason?: string): string {
  const labels: Record<string, string> = {
    concurrency_full: '并发已满',
    cooldown: '冷却中',
    auto_disabled: '已自动禁用',
    admin_disabled: '已人工禁用',
    half_open_claimed: '半开验证已被占用',
    all_channels_busy: '全部合格渠道并发已满',
    all_channels_unavailable: '全部合格渠道不可用',
    attempt_budget_exhausted: '已达到最大渠道尝试数',
    affinity_channel_busy: '亲和渠道并发已满',
    affinity_channel_unavailable: '亲和渠道不可用',
    candidate_exhausted: '候选渠道已耗尽',
  }
  return reason ? (labels[reason] ?? reason) : '未提供原因'
}

// 生成路由事件的标题、说明和显示色调。
function routeEventPresentation(event: RouteEvent): {
  title: string
  detail: string
  tone: 'neutral' | 'warning' | 'danger' | 'success'
} {
  const channelName = routeEventDetail(event, 'channelName') || event.channelId || '当前渠道'
  if (event.eventType === 'candidate_skipped') {
    const inFlight = routeEventDetail(event, 'inFlight')
    const limit = routeEventDetail(event, 'concurrencyLimit')
    const concurrency = inFlight && limit ? `，在途 ${inFlight}/${limit}` : ''
    return {
      title: `${channelName} 被跳过`,
      detail: `${routeReasonText(event.reason)}${concurrency}`,
      tone: event.reason === 'concurrency_full' ? 'warning' : 'neutral',
    }
  }
  if (event.eventType === 'channel_switched') {
    const fromName = routeEventDetail(event, 'fromChannelName') || event.channelId || '上一渠道'
    const toName = routeEventDetail(event, 'toChannelName') || event.relatedChannelId || '下一渠道'
    return {
      title: `${fromName} → ${toName}`,
      detail: `跨渠道重试，触发分类：${event.reason || '未知'}`,
      tone: 'warning',
    }
  }
  if (event.eventType === 'custom_headers_ignored') {
    return {
      title: `${channelName} 自定义请求头已忽略`,
      detail: routeEventDetail(event, 'message') || '自定义请求头配置无效，已忽略，仍使用协议默认请求头',
      tone: 'warning',
    }
  }
  if (event.eventType === 'health_changed') {
    const fromState = routeEventDetail(event, 'fromState') || '未知'
    const toState = routeEventDetail(event, 'toState') || '未知'
    const before = routeEventDetail(event, 'failureCountBefore') || '0'
    const after = routeEventDetail(event, 'failureCountAfter') || '0'
    const cooldownUntil = routeEventDetail(event, 'cooldownUntil')
    return {
      title: `${channelName} 健康状态 ${fromState} → ${toState}`,
      detail: `连续失败 ${before} → ${after}${cooldownUntil ? `，冷却至 ${formatDetailDate(cooldownUntil)}` : ''}`,
      tone: toState === 'healthy' ? 'success' : 'danger',
    }
  }
  return {
    title: '路由已结束',
    detail: `${routeReasonText(event.reason)}，已调用 ${routeEventDetail(event, 'attemptsUsed') || '0'} 个渠道`,
    tone: 'danger',
  }
}

// 渲染请求详情面板，详情和遮罩都限制在 requests-page 容器内。
export function RequestDetailPanel({
  selected,
  expandedAttempts,
  onExpandedAttemptsChange,
  clientRequestBlobs,
  finalResponseBlobs,
  snapshot,
  onLoadSnapshot,
  onClose,
  onCopyRequestID,
}: Props) {
  const timeline = buildRequestTimeline(selected)
  return (
    <>
      <button
        type="button"
        className="request-detail-backdrop"
        aria-label="关闭请求详情"
        onClick={onClose}
      />
      <div
        className="request-detail-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="request-detail-heading"
      >
        <header className="request-detail-panel-header">
          <div className="request-detail-title">
            <div>
              <h2 id="request-detail-heading">请求详情</h2>
              <span>{selected.id}</span>
            </div>
            <div>
              <Tag color={statusColor(selected.finalStatus)}>
                {statusText[selected.finalStatus] ?? selected.finalStatus}
              </Tag>
              {selected.fallbackTriggered && <Tag color="blue">跨渠道切换</Tag>}
            </div>
          </div>
          <Button
            className="request-detail-close"
            theme="borderless"
            aria-label="关闭请求详情"
            title="关闭请求详情"
            onClick={onClose}
          >
            <IconClose />
          </Button>
        </header>
        <div className="request-detail-body">
          <div className="request-detail-content">
            <section className="request-summary-section">
              <h3>请求摘要</h3>
              <div className="request-summary-fields">
                {[
                  ['请求时间', formatDetailDate(selected.startedAt)],
                  ['完成时间', formatDetailDate(selected.completedAt)],
                  ['协议', protocolLabel(selected.protocol)],
                  ['分组', selected.groupName || '—'],
                  ['原始模型', selected.clientModel],
                  [
                    '全局映射结果',
                    selected.logicalModel && selected.logicalModel !== selected.clientModel
                      ? `${selected.clientModel} → ${selected.logicalModel}`
                      : selected.clientModel,
                  ],
                  [
                    '最终上游模型',
                    selected.attempts?.[selected.attempts.length - 1]?.upstreamModel || '—',
                  ],
                  ['首次渠道', selected.initialChannelName || '—'],
                  ['最终渠道', selected.finalChannelName || '—'],
                  ['最终 HTTP 状态', selected.finalHttpStatus ?? '—'],
                  ['最终错误分类', selected.errorClass || '—'],
                  ['最终错误摘要', selected.errorMessage || '—'],
                  ['Attempt 数', String(selected.attemptCount || selected.attempts.length)],
                  ['跨渠道切换次数', String(selected.channelSwitchCount ?? 0)],
                  ['总耗时', formatDuration(selected.latencyMs)],
                  [
                    '流式正常结束',
                    selected.streamEnded == null ? '未知' : selected.streamEnded ? '是' : '否',
                  ],
                ].map(([label, value]) => (
                  <div key={label}>
                    <span>{label}</span>
                    <strong>{value}</strong>
                  </div>
                ))}
              </div>
            </section>

            <section className="request-usage-section">
              <h3>用量与性能</h3>
              <div className="request-usage-fields">
                {[
                  ['输入 Tokens', formatNumber(selected.inputTokens)],
                  ['输出 Tokens', formatNumber(selected.outputTokens)],
                  ['缓存读取', formatNumber(selected.cacheReadInputTokens)],
                  ['缓存写入', formatNumber(selected.cacheWriteInputTokens)],
                  ['思考 Tokens', formatNumber(selected.reasoningTokens)],
                  ['总 Tokens', formatNumber(selected.totalTokens)],
                  ['缓存率', formatPercent(selected.cacheRate)],
                  ['TTFT', formatDuration(selected.ttftMs)],
                  ['TPS', selected.tps == null ? '—' : selected.tps.toFixed(1)],
                  ['总耗时', formatDuration(selected.latencyMs)],
                ].map(([label, value]) => (
                  <div key={label}>
                    <span>{label}</span>
                    <strong>{value}</strong>
                  </div>
                ))}
              </div>
            </section>

            <div className="request-detail-lower">
              <section className="request-attempt-pane">
                <h3>路由时间线</h3>
                <div className="request-route-timeline">
                  {timeline.map((item) => {
                    if (item.event) {
                      const presentation = routeEventPresentation(item.event)
                      return (
                        <div
                          className={`request-route-event request-route-event-${presentation.tone}`}
                          key={item.key}
                        >
                          <i />
                          <div>
                            <strong>{presentation.title}</strong>
                            <span>{presentation.detail}</span>
                            <small>{formatListDate(item.occurredAt)}</small>
                          </div>
                        </div>
                      )
                    }
                    const attempt = item.attempt
                    const attemptIndex = selected.attempts.findIndex(
                      (candidate) => candidate.id === attempt.id,
                    )
                    const nextAction = attemptNextAction(selected.attempts, attemptIndex)
                    return (
                      <Collapse
                        className="request-attempt-collapse request-timeline-attempt"
                        activeKey={expandedAttempts.includes(attempt.id) ? [attempt.id] : []}
                        keepDOM
                        key={item.key}
                        onChange={(keys) => {
                          const activeKeys = Array.isArray(keys) ? keys.map(String) : [String(keys)]
                          onExpandedAttemptsChange(
                            activeKeys.includes(attempt.id)
                              ? [...new Set([...expandedAttempts, attempt.id])]
                              : expandedAttempts.filter((key) => key !== attempt.id),
                          )
                        }}
                      >
                        <Collapse.Panel
                          itemKey={attempt.id}
                          header={
                            <div
                              className={`request-attempt-header request-attempt-${attempt.status}`}
                            >
                              <i />
                              <div className="request-attempt-heading-copy">
                                <strong>
                                  Attempt {attempt.sequence} ·{' '}
                                  {attempt.channelName || attempt.channelId}
                                </strong>
                                <small>
                                  {formatListDate(attempt.startedAt || '')}–
                                  {attempt.completedAt
                                    ? formatListDate(attempt.completedAt)
                                    : '进行中'}
                                </small>
                              </div>
                              {attempt.fallbackTriggered && <Tag color="blue">跨渠道</Tag>}
                              <span className="request-attempt-brief">
                                模型
                                <strong>
                                  {attempt.upstreamModel || selected.logicalModel || '—'}
                                </strong>
                              </span>
                              <span className="request-attempt-brief">
                                HTTP
                                <strong>{attempt.httpStatus ?? '—'}</strong>
                              </span>
                              <Tag color={statusColor(attempt.status)}>
                                {statusText[attempt.status] ?? attempt.status}
                              </Tag>
                            </div>
                          }
                        >
                          <div className="request-attempt-body">
                            {(attempt.errorClass ||
                              attempt.errorMessage ||
                              attempt.retryReason ||
                              nextAction !== '结束请求') && (
                              <div className="request-attempt-error-line">
                                <span>错误分类</span>
                                <strong>{attempt.errorClass || '—'}</strong>
                                <span>摘要</span>
                                <strong>{attempt.errorMessage || '—'}</strong>
                                <span>触发方式</span>
                                <strong>{attemptTriggerText(attempt.retryReason)}</strong>
                                <span>后续动作</span>
                                <strong>{nextAction}</strong>
                              </div>
                            )}
                            <div className="request-attempt-fields">
                              {[
                                ['TTFT', formatDuration(attemptTTFT(attempt))],
                                ['耗时', formatDuration(attempt.latencyMs)],
                                ['协议', protocolLabel(attempt.protocol)],
                                ['分组', attempt.groupName || selected.groupName || '—'],
                                ['原始模型', attempt.clientModel || selected.clientModel],
                                ['实际模型', attempt.upstreamModel || attempt.logicalModel || '—'],
                                ['首字节时间', formatDetailDate(attempt.firstByteAt)],
                                ['首事件时间', formatDetailDate(attempt.firstEventAt)],
                                ['结束时间', formatDetailDate(attempt.completedAt)],
                                ['输入 Tokens', formatNumber(attempt.inputTokens)],
                                ['输出 Tokens', formatNumber(attempt.outputTokens)],
                                ['缓存读取', formatNumber(attempt.cacheReadInputTokens)],
                                ['缓存写入', formatNumber(attempt.cacheWriteInputTokens)],
                                ['思考 Tokens', formatNumber(attempt.reasoningTokens)],
                                ['总 Tokens', formatNumber(attempt.totalTokens)],
                                [
                                  '流式正常结束',
                                  attempt.streamEnded == null
                                    ? '未知'
                                    : attempt.streamEnded
                                      ? '是'
                                      : '否',
                                ],
                              ].map(([label, value]) => (
                                <div key={label}>
                                  <span>{label}</span>
                                  <strong>{value}</strong>
                                </div>
                              ))}
                            </div>
                          </div>
                        </Collapse.Panel>
                      </Collapse>
                    )
                  })}
                </div>
                {timeline.length === 0 && (
                  <p className="request-detail-empty">当前请求尚未产生路由记录。</p>
                )}
              </section>

              <aside className="request-snapshot-pane">
                <h3>内容快照</h3>
                <SnapshotGroup
                  title="客户端请求"
                  blobs={clientRequestBlobs}
                  snapshot={snapshot}
                  onLoad={onLoadSnapshot}
                />
                <SnapshotGroup
                  title="最终客户端响应"
                  blobs={finalResponseBlobs}
                  snapshot={snapshot}
                  onLoad={onLoadSnapshot}
                />
                {(selected.attempts ?? []).map((attempt) => (
                  <SnapshotGroup
                    key={attempt.id}
                    title={`Attempt ${attempt.sequence} · 上游详情`}
                    blobs={(selected.contentBlobs ?? []).filter(
                      (blob) => blob.attemptId === attempt.id,
                    )}
                    snapshot={snapshot}
                    onLoad={onLoadSnapshot}
                  />
                ))}
                {(selected.contentBlobs ?? []).length === 0 && (
                  <p className="request-detail-empty">当前请求未保存正文快照。</p>
                )}
              </aside>
            </div>
          </div>
        </div>
        <footer className="request-detail-footer">
          <Button icon={<IconCopy />} onClick={() => void onCopyRequestID()}>
            复制请求 ID
          </Button>
          <Button type="primary" onClick={onClose}>
            关闭
          </Button>
        </footer>
      </div>
    </>
  )
}
