export type Listener = {
  host: string
  port: number
}

export type RequestPolicy = {
  connectTimeoutMs: number
  firstByteTimeoutMs: number
  streamIdleTimeoutMs: number
  totalTimeoutMs: number
  maxChannelAttempts: number
}

export type ReasoningEffort = 'passthrough' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export type ChannelSettings = {
  reasoningEffort: ReasoningEffort
  serviceTierPassthrough: boolean
}

export type RuntimeSettings = {
  proxyListener: Listener
  adminListener: Listener
  requestPolicy: RequestPolicy
  channelSettings: ChannelSettings
  currentListener?: Listener
  configuredListener?: Listener
  restartRequired?: boolean
}

export type LoggingSettings = {
  contentPolicy: string
  requestRetentionDays: number
  auditRetentionDays: number
  runtimeRetentionDays: number
  maxRequestContentBytes: number
  maxResponseContentBytes: number
  diskQuotaBytes: number
  runtimeLogMaxBytes: number
}

export type LoggingSettingsResponse = {
  logging?: Partial<LoggingSettings>
  timezone?: string
}

export type CleanupLogsResponse = {
  requests?: number
  contentBlobs?: number
  runtimeLogs?: number
  runtimeFiles?: number
  runtimeError?: string
  message?: string
}

export type AuthStatus = {
  adminConfigured: boolean
  proxyConfigured: boolean
  adminCustom: boolean
  proxyCustom: boolean
}

export type MigrationPreview = {
  current: Record<string, number>
  incoming: Record<string, number>
  settings: {
    proxyListener: Listener
    adminListener: Listener
    timezone: string
    adminTokenPresent: boolean
    proxyTokenPresent: boolean
  }
  channels: Array<{
    id: string
    name: string
    protocol: string
    adminState: string
    credentialPresent: boolean
  }>
}

export const defaultRuntimeSettings: RuntimeSettings = {
  proxyListener: { host: '127.0.0.1', port: 9988 },
  adminListener: { host: '127.0.0.1', port: 9989 },
  requestPolicy: {
    connectTimeoutMs: 5000,
    firstByteTimeoutMs: 30000,
    streamIdleTimeoutMs: 60000,
    totalTimeoutMs: 120000,
    maxChannelAttempts: 0,
  },
  channelSettings: {
    reasoningEffort: 'passthrough',
    serviceTierPassthrough: true,
  },
}

export const defaultLoggingSettings: LoggingSettings = {
  contentPolicy: 'request_and_response_content',
  requestRetentionDays: 30,
  auditRetentionDays: 30,
  runtimeRetentionDays: 7,
  maxRequestContentBytes: 1 << 20,
  maxResponseContentBytes: 1 << 20,
  diskQuotaBytes: 100 << 20,
  runtimeLogMaxBytes: 100 << 20,
}

export type SettingsSection = 'runtime' | 'channels' | 'auth' | 'data' | 'mapping' | 'logging'

export type SettingsPageProps = {
  settingsSection?: SettingsSection
  active?: boolean
}

export type SettingsSectionProps = {
  active: boolean
  reloadToken?: number
}

// 将数字输入转为有限值；清空或非法时回退上次合法值，再回退字段默认值。
export function parseFiniteNumber(value: unknown, current: number, fallback: number): number {
  if (value === null || value === undefined || value === '') {
    return Number.isFinite(current) ? current : fallback
  }
  const parsed = typeof value === 'number' ? value : Number(value)
  if (Number.isFinite(parsed)) return parsed
  return Number.isFinite(current) ? current : fallback
}

// 判断监听和超时数字是否都可以提交。
export function hasFiniteRuntimeNumbers(settings: RuntimeSettings): boolean {
  const numbers = [
    settings.proxyListener.port,
    settings.adminListener.port,
    settings.requestPolicy.connectTimeoutMs,
    settings.requestPolicy.firstByteTimeoutMs,
    settings.requestPolicy.streamIdleTimeoutMs,
    settings.requestPolicy.totalTimeoutMs,
    settings.requestPolicy.maxChannelAttempts,
  ]
  return numbers.every((value) => Number.isFinite(value))
}

// 判断日志数字是否都可以提交。
export function hasFiniteLoggingNumbers(logging: LoggingSettings): boolean {
  const numbers = [
    logging.requestRetentionDays,
    logging.auditRetentionDays,
    logging.runtimeRetentionDays,
    logging.maxRequestContentBytes,
    logging.maxResponseContentBytes,
    logging.diskQuotaBytes,
  ]
  return numbers.every((value) => Number.isFinite(value))
}

// 规范化运行设置回包，补齐缺省字段。
export function normalizeRuntimeSettings(result: Partial<RuntimeSettings>): RuntimeSettings {
  return {
    ...defaultRuntimeSettings,
    ...result,
    proxyListener: { ...defaultRuntimeSettings.proxyListener, ...result.proxyListener },
    adminListener: { ...defaultRuntimeSettings.adminListener, ...result.adminListener },
    requestPolicy: { ...defaultRuntimeSettings.requestPolicy, ...result.requestPolicy },
    channelSettings: { ...defaultRuntimeSettings.channelSettings, ...result.channelSettings },
  }
}

// 规范化日志设置回包，补齐缺省字段。
export function normalizeLoggingSettings(result: Partial<LoggingSettings>): LoggingSettings {
  return { ...defaultLoggingSettings, ...result }
}
