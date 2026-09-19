// 将内部协议标识转换为管理页面中的可读名称。
export function protocolLabel(protocol: string): string {
  if (protocol === 'openai_responses') return 'OpenAI Responses'
  if (protocol === 'openai_chat') return 'OpenAI Chat'
  if (protocol === 'anthropic_messages') return 'Anthropic Messages'
  return protocol
}
