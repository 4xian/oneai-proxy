const adminTokenKey = 'oneai-proxy-admin-token'
const proxyTokenKey = 'oneai-proxy-proxy-token'
const pendingAdminTokenKey = 'oneai-proxy-pending-admin-token'
const localDefaultAdminToken = 'oneai-local-admin'
const localDefaultProxyToken = 'oneai-local-proxy'
const channelCredentialHKDFSalt = 'oneai-proxy-channel-credential'
const channelCredentialHKDFInfo = 'wrap-v1'

// 读取浏览器本地保存的管理令牌。
export function getAdminToken(): string {
  const saved = window.localStorage.getItem(adminTokenKey)
  if (saved) return saved
  return localDefaultAdminToken
}

// 保存或清除管理令牌。
export function setAdminToken(token: string): void {
  const normalized = token.trim()
  if (normalized) window.localStorage.setItem(adminTokenKey, normalized)
  else window.localStorage.removeItem(adminTokenKey)
}

// 保存重启后才生效的管理令牌，当前进程仍继续使用旧令牌。
export function setPendingAdminToken(token: string): void {
  const normalized = token.trim()
  if (normalized) window.localStorage.setItem(pendingAdminTokenKey, normalized)
  else window.localStorage.removeItem(pendingAdminTokenKey)
}

// 读取浏览器本地保存的代理令牌。
export function getProxyToken(): string {
  return window.localStorage.getItem(proxyTokenKey) ?? localDefaultProxyToken
}

// 保存或清除代理令牌。
export function setProxyToken(token: string): void {
  const normalized = token.trim()
  if (normalized) window.localStorage.setItem(proxyTokenKey, normalized)
  else window.localStorage.removeItem(proxyTokenKey)
}

// 读取迁移后等待服务重启生效的管理令牌。
function getPendingAdminToken(): string {
  return window.localStorage.getItem(pendingAdminTokenKey) ?? ''
}

// 判断请求体是否可安全重放到第二次 fetch。
function isReplayableBody(body: BodyInit | null | undefined): boolean {
  return (
    body == null ||
    typeof body === 'string' ||
    body instanceof URLSearchParams ||
    body instanceof Blob ||
    body instanceof ArrayBuffer
  )
}

// 为管理 API 请求统一附加显式 Bearer 管理令牌；401 时对可重放请求用 pending 令牌重试一次。
export function adminFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers)
  const token = getAdminToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  return fetch(input, { ...init, headers }).then(async (response) => {
    const pending = getPendingAdminToken()
    if (response.status !== 401 || !pending || !isReplayableBody(init.body)) return response
    const retryHeaders = new Headers(init.headers)
    retryHeaders.set('Authorization', `Bearer ${pending}`)
    const retry = await fetch(input, { ...init, headers: retryHeaders })
    if (retry.ok) {
      setAdminToken(pending)
      setPendingAdminToken('')
    }
    return retry
  })
}

// 将管理 API 的错误响应转换为页面可读的错误信息。
export async function adminError(response: Response, fallback: string): Promise<Error> {
  try {
    const result = (await response.json()) as { error?: string }
    return new Error(result.error ?? fallback)
  } catch {
    return new Error(fallback)
  }
}

// 将标准 Base64 文本解码为独立 ArrayBuffer，供渠道凭证包装密文使用。
function decodeBase64Bytes(value: string): ArrayBuffer {
  const binary = window.atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
  return bytes.buffer
}

// 用当前管理 Bearer 按 HKDF-SHA256 派生 AES-256-GCM 密钥，解密渠道凭证秘密。
export async function unwrapChannelCredentialSecret(
  ciphertext: string,
  nonce: string,
  token = getAdminToken(),
): Promise<string> {
  if (!window.crypto?.subtle) throw new Error('凭证无法解密，请重新填写')
  const encoder = new TextEncoder()
  const baseKey = await window.crypto.subtle.importKey('raw', encoder.encode(token), 'HKDF', false, [
    'deriveKey',
  ])
  const key = await window.crypto.subtle.deriveKey(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: encoder.encode(channelCredentialHKDFSalt),
      info: encoder.encode(channelCredentialHKDFInfo),
    },
    baseKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['decrypt'],
  )
  const plain = await window.crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: decodeBase64Bytes(nonce) },
    key,
    decodeBase64Bytes(ciphertext),
  )
  return new TextDecoder().decode(plain)
}
