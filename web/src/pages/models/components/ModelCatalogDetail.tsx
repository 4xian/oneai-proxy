import { Tag } from '@douyinfe/semi-ui-19'
import {
  dateText,
  formatCount,
  listText,
  priceText,
  protocolText,
  statusText,
  type ModelCatalogEntry,
} from './modelCatalog'

type Props = {
  entry: ModelCatalogEntry
}

// ModelCatalogDetail 展示一条模型目录记录的完整资料。
function ModelCatalogDetail({ entry }: Props) {
  return (
    <div className="model-detail model-detail-modal">
      <div className="model-detail-main">
        <div className="model-detail-title">
          <strong>{entry.displayName || entry.modelId}</strong>
          <span>
            {entry.displayName ? `${entry.modelId} · ` : ''}
            {protocolText(entry.protocol)}
          </span>
        </div>
        <div className="model-detail-tags">
          {entry.modelType ? <Tag type="light">{entry.modelType}</Tag> : null}
          {entry.capabilities.map((item) => (
            <Tag key={item} color="light-blue" type="light">
              {item}
            </Tag>
          ))}
        </div>
      </div>
      <p className="model-detail-description">{entry.description || '暂无描述'}</p>
      <dl className="model-detail-facts">
        <div>
          <dt>厂商 / 供应商</dt>
          <dd>{[entry.vendor, entry.provider].filter(Boolean).join(' / ') || '—'}</dd>
        </div>
        <div>
          <dt>上下文 / 最大输入 / 最大输出</dt>
          <dd>
            {formatCount(entry.contextWindow)} / {formatCount(entry.maxInputTokens)} /{' '}
            {formatCount(entry.maxOutputTokens)}
          </dd>
        </div>
        <div>
          <dt>费用（输入 / 输出 / 缓存读 / 缓存写）</dt>
          <dd>
            {priceText(entry.pricing.input, entry.currency)} /{' '}
            {priceText(entry.pricing.output, entry.currency)} /{' '}
            {priceText(entry.pricing.cacheRead, entry.currency)} /{' '}
            {priceText(entry.pricing.cacheWrite, entry.currency)}
          </dd>
        </div>
        <div>
          <dt>计费单位</dt>
          <dd>{entry.billingUnit || '—'}</dd>
        </div>
        <div>
          <dt>输入模态 / 输出模态</dt>
          <dd>
            {listText(entry.modalities.input)} / {listText(entry.modalities.output)}
          </dd>
        </div>
        <div>
          <dt>来源</dt>
          <dd>
            {entry.sourceType === 'manual' ? '手工新增' : entry.sourceAdapter || '在线同步'}
            {entry.sourceUrl ? ` · ${entry.sourceUrl}` : ''}
          </dd>
        </div>
        <div>
          <dt>来源状态</dt>
          <dd>{statusText(entry.sourceStatus)}</dd>
        </div>
        <div>
          <dt>发布时间 / 最近同步</dt>
          <dd>
            {dateText(entry.publishedAt)} / {dateText(entry.syncedAt)}
          </dd>
        </div>
        <div>
          <dt>稳定键</dt>
          <dd>{entry.stableKey}</dd>
        </div>
      </dl>
    </div>
  )
}

export default ModelCatalogDetail
