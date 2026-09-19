package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// ProtocolUnconfirmed 表示在线资料未提供足够信息确认请求协议。
	ProtocolUnconfirmed Protocol = "protocol_unconfirmed"

	// ModelSourceAdapterModelsDev 表示 models.dev 目录格式。
	ModelSourceAdapterModelsDev = "models.dev"
	// ModelSourceAdapterOpenAI 表示 OpenAI data[].id 目录格式。
	ModelSourceAdapterOpenAI = "openai_models"
	// ModelSourceAdapterModelsArray 表示通用 models[].id 目录格式。
	ModelSourceAdapterModelsArray = "models_array"
	// ModelSourceAdapterCCHPlus 表示 CCH Plus 定价目录格式。
	ModelSourceAdapterCCHPlus = "cch_plus"

	// ModelSourceTypeOnline 表示通过在线目录同步的记录。
	ModelSourceTypeOnline = "online"
	// ModelSourceTypeManual 表示用户手工维护的记录。
	ModelSourceTypeManual = "manual"

	// ModelSourceStatusValid 表示模型资料有效。
	ModelSourceStatusValid = "valid"
	// ModelSourceStatusRetired 表示模型已从相同在线来源移除。
	ModelSourceStatusRetired = "retired"
	// ModelSourceStatusProtocolUnconfirmed 表示模型协议尚未确认。
	ModelSourceStatusProtocolUnconfirmed = "protocol_unconfirmed"
)

// ModelPricing 保存每百万 Token 或来源声明单位下的原始费用。
type ModelPricing struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cacheRead,omitempty"`
	CacheWrite *float64 `json:"cacheWrite,omitempty"`
}

// ModelModalities 保存模型支持的输入和输出模态。
type ModelModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// ModelCatalogEntry 是跨来源标准化后的单个供应商模型记录。
type ModelCatalogEntry struct {
	StableKey       string          `json:"stableKey"`
	BaseModelID     string          `json:"baseModelId,omitempty"`
	ModelID         string          `json:"modelId"`
	DisplayName     string          `json:"displayName,omitempty"`
	Vendor          string          `json:"vendor,omitempty"`
	Provider        string          `json:"provider,omitempty"`
	Protocol        Protocol        `json:"protocol"`
	ModelType       string          `json:"modelType,omitempty"`
	Description     string          `json:"description,omitempty"`
	Modalities      ModelModalities `json:"modalities"`
	Capabilities    []string        `json:"capabilities"`
	ContextWindow   int64           `json:"contextWindow,omitempty"`
	MaxInputTokens  int64           `json:"maxInputTokens,omitempty"`
	MaxOutputTokens int64           `json:"maxOutputTokens,omitempty"`
	Pricing         ModelPricing    `json:"pricing"`
	Currency        string          `json:"currency,omitempty"`
	BillingUnit     string          `json:"billingUnit,omitempty"`
	SourceType      string          `json:"sourceType"`
	SourceURL       string          `json:"sourceUrl,omitempty"`
	SourceAdapter   string          `json:"sourceAdapter,omitempty"`
	SourceVersion   string          `json:"sourceVersion,omitempty"`
	SourceStatus    string          `json:"sourceStatus"`
	SyncedAt        string          `json:"syncedAt,omitempty"`
	PublishedAt     string          `json:"publishedAt,omitempty"`
	CreatedAt       string          `json:"createdAt,omitempty"`
	UpdatedAt       string          `json:"updatedAt,omitempty"`
}

// ModelCatalogParseResult 是一次纯解析操作的标准化结果。
type ModelCatalogParseResult struct {
	Adapter string              `json:"adapter"`
	Entries []ModelCatalogEntry `json:"entries"`
	Skipped int                 `json:"skipped"`
	Errors  []string            `json:"errors"`
}

const (
	// ModelCatalogChangeNew 表示本地没有对应记录。
	ModelCatalogChangeNew = "new"
	// ModelCatalogChangeChanged 表示在线记录字段发生变化。
	ModelCatalogChangeChanged = "changed"
	// ModelCatalogChangeUnchanged 表示在线记录没有变化。
	ModelCatalogChangeUnchanged = "unchanged"
	// ModelCatalogChangeConflict 表示在线记录与手工记录发生冲突。
	ModelCatalogChangeConflict = "conflict"
	// ModelCatalogChangeProtocolUnconfirmed 表示协议尚未确认。
	ModelCatalogChangeProtocolUnconfirmed = "protocol_unconfirmed"
	// ModelCatalogChangeRetired 表示相同来源中已不存在的在线记录。
	ModelCatalogChangeRetired = "retired"
)

// ModelCatalogQuery 是模型目录列表的分页和筛选条件。
type ModelCatalogQuery struct {
	Search       string
	Protocol     Protocol
	Vendor       string
	ModelType    string
	Capability   string
	SourceStatus string
	Page         int
	PageSize     int
}

// ModelCatalogPage 是模型目录列表响应。
type ModelCatalogPage struct {
	Items    []ModelCatalogEntry `json:"items"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
	Total    int                 `json:"total"`
}

// ModelCatalogFacets 是模型目录筛选器使用的去重选项。
type ModelCatalogFacets struct {
	Vendors      []string `json:"vendors"`
	ModelTypes   []string `json:"modelTypes"`
	Capabilities []string `json:"capabilities"`
}

// ModelCatalogPreviewItem 是同步预览中的一条差异。
type ModelCatalogPreviewItem struct {
	ChangeType string             `json:"changeType"`
	Entry      ModelCatalogEntry  `json:"entry"`
	Existing   *ModelCatalogEntry `json:"existing,omitempty"`
}

// ModelCatalogPreview 是同步预览结果，不会直接写入数据库。
type ModelCatalogPreview struct {
	SourceURL string                    `json:"sourceUrl"`
	Adapter   string                    `json:"adapter"`
	Items     []ModelCatalogPreviewItem `json:"items"`
}

type openAIModelDocument struct {
	Object string `json:"object"`
	Data   []struct {
		ID       string   `json:"id"`
		Protocol Protocol `json:"protocol"`
	} `json:"data"`
}

type catalogModelWire struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Family           string          `json:"family"`
	Protocol         Protocol        `json:"protocol"`
	Attachment       bool            `json:"attachment"`
	Reasoning        bool            `json:"reasoning"`
	ToolCall         bool            `json:"tool_call"`
	StructuredOutput bool            `json:"structured_output"`
	Modalities       ModelModalities `json:"modalities"`
	Limit            struct {
		Context int64 `json:"context"`
		Input   int64 `json:"input"`
		Output  int64 `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input      *float64 `json:"input"`
		Output     *float64 `json:"output"`
		CacheRead  *float64 `json:"cache_read"`
		CacheWrite *float64 `json:"cache_write"`
	} `json:"cost"`
	ReleaseDate string `json:"release_date"`
}

type modelsDevDocument struct {
	Models    map[string]catalogModelWire `json:"models"`
	Providers map[string]struct {
		ID     string                      `json:"id"`
		Name   string                      `json:"name"`
		NPM    string                      `json:"npm"`
		Models map[string]catalogModelWire `json:"models"`
	} `json:"providers"`
}

type modelsArrayDocument struct {
	Models []catalogModelWire `json:"models"`
}

type cchPlusPricing struct {
	Provider string `json:"provider"`
	Charges  struct {
		Prompt struct {
			Unit  string `json:"unit"`
			Price string `json:"price"`
		} `json:"prompt"`
		Completion struct {
			Unit  string `json:"unit"`
			Price string `json:"price"`
		} `json:"completion"`
		CacheRead struct {
			Unit  string `json:"unit"`
			Price string `json:"price"`
		} `json:"cache_read"`
		CacheWrite struct {
			Unit  string `json:"unit"`
			Price string `json:"price"`
		} `json:"cache_write"`
	} `json:"charges"`
}

type cchPlusDocument struct {
	Schema   string `json:"schema"`
	Version  string `json:"version"`
	Currency string `json:"currency"`
	Models   []struct {
		Slug         string          `json:"slug"`
		ModelName    string          `json:"model_name"`
		DisplayName  string          `json:"display_name"`
		Vendor       string          `json:"vendor"`
		ModelType    string          `json:"model_type"`
		Description  string          `json:"description"`
		ReleasedAt   string          `json:"released_at"`
		Modalities   ModelModalities `json:"modalities"`
		Capabilities map[string]any  `json:"capabilities"`
		Endpoints    struct {
			Inbound []string `json:"inbound"`
		} `json:"endpoints"`
		Pricing []cchPlusPricing `json:"pricing"`
	} `json:"models"`
}

// ParseModelCatalogDocument 使用有限 Adapter 列表解析公开模型目录，不递归猜测未知字段。
func ParseModelCatalogDocument(sourceURL string, data []byte) (ModelCatalogParseResult, error) {
	normalizedURL, err := normalizeModelSourceURL(sourceURL)
	if err != nil {
		return ModelCatalogParseResult{}, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return ModelCatalogParseResult{}, errors.New("模型源不是有效 JSON")
	}
	if _, hasModels := probe["models"]; hasModels {
		if _, hasProviders := probe["providers"]; hasProviders {
			var modelsProbe any
			if json.Unmarshal(probe["models"], &modelsProbe) == nil {
				if _, isObject := modelsProbe.(map[string]any); isObject {
					return parseModelsDevDocument(normalizedURL, data)
				}
			}
		}
		if schema, exists := probe["schema"]; exists && strings.Contains(string(schema), "cchp.") {
			return parseCCHPlusDocument(normalizedURL, data)
		}
		return parseModelsArrayDocument(normalizedURL, data)
	}
	if _, exists := probe["data"]; exists {
		return parseOpenAIModelDocument(normalizedURL, data)
	}
	return ModelCatalogParseResult{}, errors.New("无法识别模型源格式")
}

func parseModelsDevDocument(sourceURL string, data []byte) (ModelCatalogParseResult, error) {
	var document modelsDevDocument
	if err := json.Unmarshal(data, &document); err != nil || len(document.Providers) == 0 {
		return ModelCatalogParseResult{}, errors.New("models.dev 模型目录格式无效")
	}
	result := ModelCatalogParseResult{Adapter: ModelSourceAdapterModelsDev, Entries: make([]ModelCatalogEntry, 0), Errors: make([]string, 0)}
	for providerKey, provider := range document.Providers {
		providerID := strings.TrimSpace(provider.ID)
		if providerID == "" {
			providerID = strings.TrimSpace(providerKey)
		}
		protocol := modelsDevProtocol(provider.NPM)
		for modelKey, model := range provider.Models {
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				modelID = strings.TrimSpace(modelKey)
			}
			if modelID == "" {
				result.Skipped++
				result.Errors = append(result.Errors, fmt.Sprintf("providers.%s.models.%s.id 不能为空", providerKey, modelKey))
				continue
			}
			base := matchingBaseModel(document.Models, modelID)
			entry := newOnlineModelEntry(sourceURL, ModelSourceAdapterModelsDev, protocol, providerID, modelID)
			applyCatalogModelWire(&entry, model)
			if base.ID != "" {
				entry.BaseModelID = base.ID
				if entry.DisplayName == "" {
					entry.DisplayName = base.Name
				}
				if entry.Description == "" {
					entry.Description = base.Description
				}
			}
			entry.Vendor = modelVendor(entry.BaseModelID)
			entry.Currency = "USD"
			entry.BillingUnit = "per_1m_tokens"
			result.Entries = append(result.Entries, entry)
		}
	}
	result.Entries = deduplicateCatalogEntries(result.Entries)
	if len(result.Entries) == 0 {
		return ModelCatalogParseResult{}, errors.New("模型源没有可识别的模型")
	}
	sortCatalogEntries(result.Entries)
	return result, nil
}

func parseModelsArrayDocument(sourceURL string, data []byte) (ModelCatalogParseResult, error) {
	var document modelsArrayDocument
	if err := json.Unmarshal(data, &document); err != nil || document.Models == nil {
		return ModelCatalogParseResult{}, errors.New("models 数组目录格式无效")
	}
	result := ModelCatalogParseResult{Adapter: ModelSourceAdapterModelsArray, Entries: make([]ModelCatalogEntry, 0, len(document.Models)), Errors: make([]string, 0)}
	for index, model := range document.Models {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("models[%d].id 不能为空", index))
			continue
		}
		entry := newOnlineModelEntry(sourceURL, ModelSourceAdapterModelsArray, normalizeCatalogProtocol(model.Protocol), "", modelID)
		applyCatalogModelWire(&entry, model)
		result.Entries = append(result.Entries, entry)
	}
	if len(result.Entries) == 0 {
		return ModelCatalogParseResult{}, errors.New("模型源没有可识别的模型")
	}
	sortCatalogEntries(result.Entries)
	return result, nil
}

func parseCCHPlusDocument(sourceURL string, data []byte) (ModelCatalogParseResult, error) {
	var document cchPlusDocument
	if err := json.Unmarshal(data, &document); err != nil || !strings.HasPrefix(document.Schema, "cchp.") {
		return ModelCatalogParseResult{}, errors.New("CCH Plus 模型目录格式无效")
	}
	result := ModelCatalogParseResult{Adapter: ModelSourceAdapterCCHPlus, Entries: make([]ModelCatalogEntry, 0), Errors: make([]string, 0)}
	for index, model := range document.Models {
		modelID := strings.TrimSpace(model.ModelName)
		if modelID == "" {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("models[%d].model_name 不能为空", index))
			continue
		}
		protocols := cchProtocols(model.Endpoints.Inbound)
		prices := model.Pricing
		if len(prices) == 0 {
			prices = []cchPlusPricing{{}}
		}
		for _, protocol := range protocols {
			for _, price := range prices {
				entry := newOnlineModelEntry(sourceURL, ModelSourceAdapterCCHPlus, protocol, strings.TrimSpace(price.Provider), modelID)
				entry.BaseModelID = strings.TrimSpace(model.Slug)
				if entry.BaseModelID == "" {
					entry.BaseModelID = modelID
				}
				entry.DisplayName = strings.TrimSpace(model.DisplayName)
				entry.Vendor = strings.TrimSpace(model.Vendor)
				entry.ModelType = strings.TrimSpace(model.ModelType)
				entry.Description = strings.TrimSpace(model.Description)
				entry.Modalities = normalizeModalities(model.Modalities)
				entry.Capabilities = mapCapabilities(model.Capabilities)
				entry.Pricing = ModelPricing{Input: parseCatalogPrice(price.Charges.Prompt.Price), Output: parseCatalogPrice(price.Charges.Completion.Price), CacheRead: parseCatalogPrice(price.Charges.CacheRead.Price), CacheWrite: parseCatalogPrice(price.Charges.CacheWrite.Price)}
				entry.Currency = strings.TrimSpace(document.Currency)
				entry.BillingUnit = firstNonEmpty(price.Charges.Prompt.Unit, price.Charges.Completion.Unit)
				entry.SourceVersion = strings.TrimSpace(document.Version)
				entry.PublishedAt = strings.TrimSpace(model.ReleasedAt)
				result.Entries = append(result.Entries, entry)
			}
		}
	}
	result.Entries = deduplicateCatalogEntries(result.Entries)
	if len(result.Entries) == 0 {
		return ModelCatalogParseResult{}, errors.New("模型源没有可识别的模型")
	}
	sortCatalogEntries(result.Entries)
	return result, nil
}

func modelsDevProtocol(npmPackage string) Protocol {
	value := strings.ToLower(strings.TrimSpace(npmPackage))
	switch {
	case strings.Contains(value, "anthropic"):
		return ProtocolAnthropicMessages
	case value == "@ai-sdk/openai":
		return ProtocolOpenAIResponses
	default:
		return ProtocolUnconfirmed
	}
}

func matchingBaseModel(models map[string]catalogModelWire, modelID string) catalogModelWire {
	if model, exists := models[modelID]; exists {
		return model
	}
	for key, model := range models {
		if model.ID == modelID || strings.HasSuffix(key, "/"+modelID) || strings.HasSuffix(model.ID, "/"+modelID) {
			return model
		}
	}
	return catalogModelWire{}
}

func applyCatalogModelWire(entry *ModelCatalogEntry, model catalogModelWire) {
	entry.DisplayName = strings.TrimSpace(model.Name)
	entry.Description = strings.TrimSpace(model.Description)
	entry.ModelType = "text_generation"
	entry.Modalities = normalizeModalities(model.Modalities)
	entry.Capabilities = modelWireCapabilities(model)
	entry.ContextWindow = model.Limit.Context
	entry.MaxInputTokens = model.Limit.Input
	entry.MaxOutputTokens = model.Limit.Output
	entry.Pricing = ModelPricing{Input: model.Cost.Input, Output: model.Cost.Output, CacheRead: model.Cost.CacheRead, CacheWrite: model.Cost.CacheWrite}
	entry.PublishedAt = strings.TrimSpace(model.ReleaseDate)
	if strings.TrimSpace(model.Family) != "" && entry.BaseModelID == "" {
		entry.BaseModelID = strings.TrimSpace(model.Family)
	}
}

func modelWireCapabilities(model catalogModelWire) []string {
	capabilities := make([]string, 0, 4)
	if model.Reasoning {
		capabilities = append(capabilities, "reasoning")
	}
	if model.ToolCall {
		capabilities = append(capabilities, "tools")
	}
	if model.Attachment {
		capabilities = append(capabilities, "attachments")
	}
	if model.StructuredOutput {
		capabilities = append(capabilities, "structured_output")
	}
	sort.Strings(capabilities)
	return capabilities
}

func normalizeModalities(modalities ModelModalities) ModelModalities {
	return ModelModalities{Input: uniqueStrings(modalities.Input), Output: uniqueStrings(modalities.Output)}
}

func mapCapabilities(values map[string]any) []string {
	capabilities := make([]string, 0, len(values))
	for name, value := range values {
		enabled, isBoolean := value.(bool)
		if isBoolean && enabled {
			capabilities = append(capabilities, strings.TrimSpace(name))
		}
	}
	return uniqueStrings(capabilities)
}

func cchProtocols(endpoints []string) []Protocol {
	protocols := make([]Protocol, 0, 2)
	for _, endpoint := range endpoints {
		switch strings.ToLower(strings.TrimSpace(endpoint)) {
		case "anthropic-messages", "anthropic_messages":
			protocols = append(protocols, ProtocolAnthropicMessages)
		case "openai-responses", "openai_responses":
			protocols = append(protocols, ProtocolOpenAIResponses)
		case "openai-chat", "openai_chat", "openai-compatible":
			protocols = append(protocols, ProtocolUnconfirmed)
		}
	}
	if len(protocols) == 0 {
		protocols = append(protocols, ProtocolUnconfirmed)
	}
	seen := make(map[Protocol]struct{}, len(protocols))
	result := make([]Protocol, 0, len(protocols))
	for _, protocol := range protocols {
		if _, exists := seen[protocol]; exists {
			continue
		}
		seen[protocol] = struct{}{}
		result = append(result, protocol)
	}
	return result
}

func parseCatalogPrice(value string) *float64 {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || number < 0 {
		return nil
	}
	return &number
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func deduplicateCatalogEntries(entries []ModelCatalogEntry) []ModelCatalogEntry {
	seen := make(map[string]struct{}, len(entries))
	result := make([]ModelCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.StableKey]; exists {
			continue
		}
		seen[entry.StableKey] = struct{}{}
		result = append(result, entry)
	}
	return result
}

func parseOpenAIModelDocument(sourceURL string, data []byte) (ModelCatalogParseResult, error) {
	var document openAIModelDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return ModelCatalogParseResult{}, errors.New("OpenAI 模型目录格式无效")
	}
	result := ModelCatalogParseResult{Adapter: ModelSourceAdapterOpenAI, Entries: make([]ModelCatalogEntry, 0, len(document.Data)), Errors: make([]string, 0)}
	for index, model := range document.Data {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			result.Skipped++
			result.Errors = append(result.Errors, fmt.Sprintf("data[%d].id 不能为空", index))
			continue
		}
		protocol := normalizeCatalogProtocol(model.Protocol)
		entry := newOnlineModelEntry(sourceURL, ModelSourceAdapterOpenAI, protocol, "", modelID)
		result.Entries = append(result.Entries, entry)
	}
	if len(result.Entries) == 0 {
		return ModelCatalogParseResult{}, errors.New("模型源没有可识别的模型")
	}
	sortCatalogEntries(result.Entries)
	return result, nil
}

func newOnlineModelEntry(sourceURL, adapter string, protocol Protocol, provider, modelID string) ModelCatalogEntry {
	status := ModelSourceStatusValid
	if protocol == ProtocolUnconfirmed {
		status = ModelSourceStatusProtocolUnconfirmed
	}
	vendor := modelVendor(modelID)
	return ModelCatalogEntry{
		StableKey: stableModelKey(protocol, provider, modelID), BaseModelID: modelID,
		ModelID: modelID, Vendor: vendor, Provider: provider, Protocol: protocol,
		Modalities: ModelModalities{Input: []string{}, Output: []string{}}, Capabilities: []string{},
		SourceType: ModelSourceTypeOnline, SourceURL: sourceURL, SourceAdapter: adapter,
		SourceStatus: status,
	}
}

func stableModelKey(protocol Protocol, provider, modelID string) string {
	digest := sha256.Sum256([]byte(string(protocol) + "\x00" + strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(modelID)))
	return hex.EncodeToString(digest[:])
}

func normalizeCatalogProtocol(protocol Protocol) Protocol {
	switch protocol {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropicMessages:
		return protocol
	default:
		return ProtocolUnconfirmed
	}
}

func normalizeModelSourceURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("模型源必须是公开 HTTP(S) 地址")
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func modelVendor(modelID string) string {
	parts := strings.SplitN(strings.TrimSpace(modelID), "/", 2)
	if len(parts) == 2 {
		return parts[0]
	}
	return ""
}

func sortCatalogEntries(entries []ModelCatalogEntry) {
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].BaseModelID != entries[right].BaseModelID {
			return entries[left].BaseModelID < entries[right].BaseModelID
		}
		if entries[left].Provider != entries[right].Provider {
			return entries[left].Provider < entries[right].Provider
		}
		return entries[left].StableKey < entries[right].StableKey
	})
}

// SaveManualModelCatalogEntry 保存一条手工模型目录记录。
func SaveManualModelCatalogEntry(database *sql.DB, entry ModelCatalogEntry) (ModelCatalogEntry, error) {
	entry.ModelID = strings.TrimSpace(entry.ModelID)
	entry.BaseModelID = strings.TrimSpace(entry.BaseModelID)
	entry.DisplayName = strings.TrimSpace(entry.DisplayName)
	entry.Vendor = strings.TrimSpace(entry.Vendor)
	entry.Provider = strings.TrimSpace(entry.Provider)
	entry.ModelType = strings.TrimSpace(entry.ModelType)
	entry.Description = strings.TrimSpace(entry.Description)
	entry.Currency = strings.TrimSpace(entry.Currency)
	entry.BillingUnit = strings.TrimSpace(entry.BillingUnit)
	entry.PublishedAt = strings.TrimSpace(entry.PublishedAt)
	entry.Protocol = normalizeCatalogProtocol(entry.Protocol)
	if entry.ModelID == "" || entry.Protocol == ProtocolUnconfirmed {
		return ModelCatalogEntry{}, errors.New("手工模型必须填写模型 ID 和已确认协议")
	}
	entry.SourceType = ModelSourceTypeManual
	entry.SourceStatus = ModelSourceStatusValid
	entry.Modalities = normalizeModalities(entry.Modalities)
	entry.Capabilities = uniqueStrings(entry.Capabilities)
	entry.StableKey = stableModelKey(entry.Protocol, entry.Provider, entry.ModelID)
	if err := ValidateModelCatalogEntry(entry); err != nil {
		return ModelCatalogEntry{}, err
	}
	var existingSourceType string
	err := database.QueryRow(`SELECT source_type FROM model_catalog WHERE stable_key = ?`, entry.StableKey).Scan(&existingSourceType)
	if err == nil {
		return ModelCatalogEntry{}, fmt.Errorf("模型目录已存在相同稳定键: %s", entry.ModelID)
	}
	if err != sql.ErrNoRows {
		return ModelCatalogEntry{}, fmt.Errorf("检查模型目录重复项失败: %w", err)
	}
	now := nowUTC()
	entry.CreatedAt, entry.UpdatedAt = now, now
	if _, err := upsertModelCatalogEntry(database, entry, false); err != nil {
		return ModelCatalogEntry{}, err
	}
	return entry, nil
}

// GetModelCatalogEntry 返回指定稳定键的模型记录。
func GetModelCatalogEntry(database *sql.DB, stableKey string) (ModelCatalogEntry, error) {
	row := database.QueryRow(`SELECT stable_key, base_model_id, model_id, display_name, vendor, provider, protocol, model_type, description, modalities_json, capabilities_json, context_window, max_input_tokens, max_output_tokens, pricing_json, currency, billing_unit, source_type, source_url, source_adapter, source_version, source_status, synced_at, published_at, created_at, updated_at FROM model_catalog WHERE stable_key = ?`, strings.TrimSpace(stableKey))
	entry, err := scanModelCatalogEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelCatalogEntry{}, ErrNotFound
	}
	return entry, err
}

// UpdateModelCatalogEntry 更新模型目录记录，并保留原有来源元数据。
func UpdateModelCatalogEntry(database *sql.DB, stableKey string, entry ModelCatalogEntry) (ModelCatalogEntry, error) {
	old, err := GetModelCatalogEntry(database, stableKey)
	if err != nil {
		return ModelCatalogEntry{}, err
	}
	entry.ModelID = strings.TrimSpace(entry.ModelID)
	entry.BaseModelID = strings.TrimSpace(entry.BaseModelID)
	entry.DisplayName = strings.TrimSpace(entry.DisplayName)
	entry.Vendor = strings.TrimSpace(entry.Vendor)
	entry.Provider = strings.TrimSpace(entry.Provider)
	entry.ModelType = strings.TrimSpace(entry.ModelType)
	entry.Description = strings.TrimSpace(entry.Description)
	entry.Currency = strings.TrimSpace(entry.Currency)
	entry.BillingUnit = strings.TrimSpace(entry.BillingUnit)
	entry.PublishedAt = strings.TrimSpace(entry.PublishedAt)
	entry.Protocol = normalizeCatalogProtocol(entry.Protocol)
	if entry.ModelID == "" {
		return ModelCatalogEntry{}, errors.New("模型 ID 不能为空")
	}
	if entry.Protocol == ProtocolUnconfirmed {
		return ModelCatalogEntry{}, errors.New("模型必须填写模型 ID 和已确认协议")
	}
	if old.SourceType == ModelSourceTypeOnline {
		entry.Provider = old.Provider
	}
	entry.SourceType = old.SourceType
	entry.SourceURL = old.SourceURL
	entry.SourceAdapter = old.SourceAdapter
	entry.SourceVersion = old.SourceVersion
	entry.SourceStatus = old.SourceStatus
	entry.SyncedAt = old.SyncedAt
	if entry.SourceType == ModelSourceTypeOnline && old.SourceStatus == ModelSourceStatusProtocolUnconfirmed {
		entry.SourceStatus = ModelSourceStatusValid
	}
	entry.Modalities = normalizeModalities(entry.Modalities)
	entry.Capabilities = uniqueStrings(entry.Capabilities)
	entry.StableKey = stableModelKey(entry.Protocol, entry.Provider, entry.ModelID)
	if err := ValidateModelCatalogEntry(entry); err != nil {
		return ModelCatalogEntry{}, err
	}
	entry.CreatedAt = old.CreatedAt
	entry.UpdatedAt = nowUTC()
	tx, err := database.Begin()
	if err != nil {
		return ModelCatalogEntry{}, fmt.Errorf("开始更新模型事务失败: %w", err)
	}
	defer tx.Rollback()
	if entry.StableKey != old.StableKey {
		var duplicate string
		lookupErr := tx.QueryRow(`SELECT stable_key FROM model_catalog WHERE stable_key = ?`, entry.StableKey).Scan(&duplicate)
		if lookupErr == nil {
			return ModelCatalogEntry{}, fmt.Errorf("模型目录已存在相同稳定键: %s", entry.ModelID)
		}
		if lookupErr != sql.ErrNoRows {
			return ModelCatalogEntry{}, fmt.Errorf("检查模型目录重复项失败: %w", lookupErr)
		}
		if _, err := tx.Exec(`DELETE FROM model_catalog WHERE stable_key = ?`, old.StableKey); err != nil {
			return ModelCatalogEntry{}, fmt.Errorf("删除旧模型记录失败: %w", err)
		}
	}
	if _, err := upsertModelCatalogEntryTx(tx, entry, entry.SourceType == ModelSourceTypeOnline); err != nil {
		return ModelCatalogEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModelCatalogEntry{}, fmt.Errorf("提交模型更新失败: %w", err)
	}
	return entry, nil
}

// DeleteModelCatalogEntry 删除单条模型目录记录。
func DeleteModelCatalogEntry(database *sql.DB, stableKey string) error {
	result, err := database.Exec(`DELETE FROM model_catalog WHERE stable_key = ?`, strings.TrimSpace(stableKey))
	if err != nil {
		return fmt.Errorf("删除模型失败: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteModelCatalogEntries 删除用户选择的模型目录记录。
func DeleteModelCatalogEntries(database *sql.DB, stableKeys []string) (int, error) {
	keys := make([]string, 0, len(stableKeys))
	seen := make(map[string]struct{}, len(stableKeys))
	for _, value := range stableKeys {
		key := strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return 0, errors.New("至少选择一个模型")
	}
	transaction, err := database.Begin()
	if err != nil {
		return 0, fmt.Errorf("开始删除模型事务失败: %w", err)
	}
	defer transaction.Rollback()
	for _, key := range keys {
		var exists int
		if err := transaction.QueryRow(`SELECT 1 FROM model_catalog WHERE stable_key = ?`, key).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrNotFound
			}
			return 0, fmt.Errorf("读取待删除模型失败: %w", err)
		}
	}
	deleted := 0
	for _, key := range keys {
		result, err := transaction.Exec(`DELETE FROM model_catalog WHERE stable_key = ?`, key)
		if err != nil {
			return 0, fmt.Errorf("删除模型失败: %w", err)
		}
		count, _ := result.RowsAffected()
		deleted += int(count)
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("提交删除模型事务失败: %w", err)
	}
	return deleted, nil
}

// ListModelCatalog 返回模型目录分页结果。
// PageSize 小于 1 时使用默认 20，大于 100 时夹到 100。
func ListModelCatalog(database *sql.DB, query ModelCatalogQuery) (ModelCatalogPage, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	where := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if value := strings.TrimSpace(query.Search); value != "" {
		where = append(where, `(model_id LIKE ? OR display_name LIKE ?)`)
		pattern := "%" + value + "%"
		args = append(args, pattern, pattern)
	}
	if query.Protocol != "" {
		where = append(where, `protocol = ?`)
		args = append(args, query.Protocol)
	}
	if value := strings.TrimSpace(query.Vendor); value != "" {
		where = append(where, `vendor = ?`)
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.ModelType); value != "" {
		where = append(where, `model_type = ?`)
		args = append(args, value)
	}
	if value := strings.TrimSpace(query.Capability); value != "" {
		where = append(where, `capabilities_json LIKE ?`)
		args = append(args, "%\""+value+"\"%")
	}
	if value := strings.TrimSpace(query.SourceStatus); value != "" {
		where = append(where, `source_status = ?`)
		args = append(args, value)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := database.QueryRow(`SELECT COUNT(*) FROM model_catalog`+whereSQL, args...).Scan(&total); err != nil {
		return ModelCatalogPage{}, fmt.Errorf("统计模型目录失败: %w", err)
	}
	rows, err := database.Query(`SELECT stable_key, base_model_id, model_id, display_name, vendor, provider, protocol, model_type, description, modalities_json, capabilities_json, context_window, max_input_tokens, max_output_tokens, pricing_json, currency, billing_unit, source_type, source_url, source_adapter, source_version, source_status, synced_at, published_at, created_at, updated_at FROM model_catalog`+whereSQL+` ORDER BY base_model_id, provider, model_id LIMIT ? OFFSET ?`, append(args, query.PageSize, (query.Page-1)*query.PageSize)...)
	if err != nil {
		return ModelCatalogPage{}, fmt.Errorf("读取模型目录失败: %w", err)
	}
	defer rows.Close()
	items := make([]ModelCatalogEntry, 0, query.PageSize)
	for rows.Next() {
		entry, scanErr := scanModelCatalogEntry(rows)
		if scanErr != nil {
			return ModelCatalogPage{}, scanErr
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return ModelCatalogPage{}, fmt.Errorf("遍历模型目录失败: %w", err)
	}
	return ModelCatalogPage{Items: items, Page: query.Page, PageSize: query.PageSize, Total: total}, nil
}

// ListModelCatalogFacets 返回模型目录筛选器的稳定选项。
func ListModelCatalogFacets(database *sql.DB) (ModelCatalogFacets, error) {
	rows, err := database.Query(`SELECT vendor, model_type, capabilities_json FROM model_catalog ORDER BY vendor, model_type`)
	if err != nil {
		return ModelCatalogFacets{}, fmt.Errorf("读取模型目录筛选项失败: %w", err)
	}
	defer rows.Close()
	vendors := make(map[string]struct{})
	modelTypes := make(map[string]struct{})
	capabilities := make(map[string]struct{})
	for rows.Next() {
		var vendor, modelType, capabilitiesJSON string
		if err := rows.Scan(&vendor, &modelType, &capabilitiesJSON); err != nil {
			return ModelCatalogFacets{}, fmt.Errorf("读取模型目录筛选字段失败: %w", err)
		}
		if value := strings.TrimSpace(vendor); value != "" {
			vendors[value] = struct{}{}
		}
		if value := strings.TrimSpace(modelType); value != "" {
			modelTypes[value] = struct{}{}
		}
		var values []string
		if err := json.Unmarshal([]byte(capabilitiesJSON), &values); err != nil {
			return ModelCatalogFacets{}, fmt.Errorf("解析模型目录筛选能力失败: %w", err)
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				capabilities[value] = struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return ModelCatalogFacets{}, fmt.Errorf("遍历模型目录筛选项失败: %w", err)
	}
	return ModelCatalogFacets{
		Vendors:      sortedSetValues(vendors),
		ModelTypes:   sortedSetValues(modelTypes),
		Capabilities: sortedSetValues(capabilities),
	}, nil
}

// ListAllModelCatalog 返回全部模型目录记录，供配置导出使用。
func ListAllModelCatalog(database *sql.DB) ([]ModelCatalogEntry, error) {
	return listAllModelCatalog(database)
}

// ValidateModelCatalogEntry 校验可迁移的模型目录记录。
func ValidateModelCatalogEntry(entry ModelCatalogEntry) error {
	if strings.TrimSpace(entry.StableKey) == "" || strings.TrimSpace(entry.ModelID) == "" {
		return errors.New("模型目录稳定键和模型 ID 不能为空")
	}
	if entry.StableKey != stableModelKey(entry.Protocol, entry.Provider, entry.ModelID) {
		return errors.New("模型目录稳定键与协议、供应商和模型 ID 不匹配")
	}
	if entry.Protocol != ProtocolOpenAIChat && entry.Protocol != ProtocolOpenAIResponses && entry.Protocol != ProtocolAnthropicMessages && entry.Protocol != ProtocolUnconfirmed {
		return fmt.Errorf("模型目录协议无效: %s", entry.Protocol)
	}
	if entry.SourceType != ModelSourceTypeOnline && entry.SourceType != ModelSourceTypeManual {
		return fmt.Errorf("模型目录来源类型无效: %s", entry.SourceType)
	}
	if entry.SourceStatus != ModelSourceStatusValid && entry.SourceStatus != ModelSourceStatusRetired && entry.SourceStatus != ModelSourceStatusProtocolUnconfirmed {
		return fmt.Errorf("模型目录来源状态无效: %s", entry.SourceStatus)
	}
	if entry.SourceStatus == ModelSourceStatusProtocolUnconfirmed && entry.Protocol != ProtocolUnconfirmed {
		return errors.New("协议已确认的模型不能标记为协议未确认")
	}
	if entry.SourceType == ModelSourceTypeManual && entry.Protocol == ProtocolUnconfirmed {
		return errors.New("手工模型必须指定已确认协议")
	}
	if entry.ContextWindow < 0 || entry.MaxInputTokens < 0 || entry.MaxOutputTokens < 0 {
		return errors.New("模型上下文和 Token 限制不能为负数")
	}
	for name, value := range map[string]*float64{"input": entry.Pricing.Input, "output": entry.Pricing.Output, "cacheRead": entry.Pricing.CacheRead, "cacheWrite": entry.Pricing.CacheWrite} {
		if value != nil && (*value < 0 || *value != *value) {
			return fmt.Errorf("模型费用无效: %s", name)
		}
	}
	return nil
}

// BuildModelCatalogPreview 计算来源差异，不修改数据库。
func BuildModelCatalogPreview(database *sql.DB, parsed ModelCatalogParseResult, sourceURL string) (ModelCatalogPreview, error) {
	normalizedURL, err := normalizeModelSourceURL(sourceURL)
	if err != nil {
		return ModelCatalogPreview{}, err
	}
	entries := deduplicateCatalogEntries(parsed.Entries)
	existing, err := listModelCatalogBySource(database, normalizedURL, parsed.Adapter)
	if err != nil {
		return ModelCatalogPreview{}, err
	}
	allEntries, err := listAllModelCatalog(database)
	if err != nil {
		return ModelCatalogPreview{}, err
	}
	byKey := make(map[string]ModelCatalogEntry, len(allEntries))
	for _, entry := range allEntries {
		byKey[entry.StableKey] = entry
	}
	items := make([]ModelCatalogPreviewItem, 0, len(entries)+len(existing))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry.SourceURL, entry.SourceAdapter = normalizedURL, parsed.Adapter
		entry.SourceType = ModelSourceTypeOnline
		if entry.Protocol == ProtocolUnconfirmed {
			entry.SourceStatus = ModelSourceStatusProtocolUnconfirmed
		} else {
			entry.SourceStatus = ModelSourceStatusValid
		}
		if old, exists := byKey[entry.StableKey]; exists {
			oldCopy := old
			changeType := ModelCatalogChangeUnchanged
			if old.SourceType == ModelSourceTypeManual {
				changeType = ModelCatalogChangeConflict
			} else if !sameCatalogEntry(old, entry) {
				changeType = ModelCatalogChangeChanged
			}
			items = append(items, ModelCatalogPreviewItem{ChangeType: changeType, Entry: entry, Existing: &oldCopy})
		} else if entry.Protocol == ProtocolUnconfirmed {
			items = append(items, ModelCatalogPreviewItem{ChangeType: ModelCatalogChangeProtocolUnconfirmed, Entry: entry})
		} else {
			items = append(items, ModelCatalogPreviewItem{ChangeType: ModelCatalogChangeNew, Entry: entry})
		}
		seen[entry.StableKey] = struct{}{}
	}
	for _, old := range existing {
		if _, exists := seen[old.StableKey]; exists || old.SourceType != ModelSourceTypeOnline {
			continue
		}
		items = append(items, ModelCatalogPreviewItem{ChangeType: ModelCatalogChangeRetired, Entry: old})
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Entry.StableKey < items[right].Entry.StableKey })
	return ModelCatalogPreview{SourceURL: normalizedURL, Adapter: parsed.Adapter, Items: items}, nil
}

// ModelCatalogApplyResult 是确认应用后的写入与跳过统计。
type ModelCatalogApplyResult struct {
	Applied   int
	Skipped   int
	Conflicts int
}

// ApplyModelCatalogPreview 事务应用用户选择的在线差异。
// 勾选 conflict/unchanged 会计入 Conflicts/Skipped，不写入且不视为失败。
func ApplyModelCatalogPreview(database *sql.DB, preview ModelCatalogPreview, selectedStableKeys []string, syncedAt time.Time) (ModelCatalogApplyResult, error) {
	selected := make(map[string]struct{}, len(selectedStableKeys))
	for _, key := range selectedStableKeys {
		selected[strings.TrimSpace(key)] = struct{}{}
	}
	transaction, err := database.Begin()
	if err != nil {
		return ModelCatalogApplyResult{}, fmt.Errorf("开始应用模型目录事务失败: %w", err)
	}
	defer transaction.Rollback()
	var result ModelCatalogApplyResult
	for _, item := range preview.Items {
		if _, exists := selected[item.Entry.StableKey]; !exists {
			continue
		}
		if item.ChangeType == ModelCatalogChangeConflict {
			result.Conflicts++
			continue
		}
		if item.ChangeType == ModelCatalogChangeUnchanged {
			result.Skipped++
			continue
		}
		if item.ChangeType == ModelCatalogChangeRetired {
			if _, err := transaction.Exec(`UPDATE model_catalog SET source_status = ?, synced_at = ?, updated_at = ? WHERE stable_key = ? AND source_type = 'online'`, ModelSourceStatusRetired, syncedAt.UTC().Format(time.RFC3339Nano), nowUTC(), item.Entry.StableKey); err != nil {
				return ModelCatalogApplyResult{}, fmt.Errorf("标记失效模型失败: %w", err)
			}
			result.Applied++
			continue
		}
		entry := item.Entry
		entry.SyncedAt, entry.UpdatedAt = syncedAt.UTC().Format(time.RFC3339Nano), nowUTC()
		if entry.CreatedAt == "" {
			entry.CreatedAt = entry.UpdatedAt
		}
		if _, err := upsertModelCatalogEntryTx(transaction, entry, true); err != nil {
			return ModelCatalogApplyResult{}, err
		}
		result.Applied++
	}
	sourceJSON, err := json.Marshal(strings.TrimSpace(preview.SourceURL))
	if err != nil {
		return ModelCatalogApplyResult{}, fmt.Errorf("序列化模型目录来源地址失败: %w", err)
	}
	if _, err := transaction.Exec(`INSERT INTO app_settings(key, value_json, updated_at) VALUES ('model-catalog.source-url', ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, string(sourceJSON), nowUTC()); err != nil {
		return ModelCatalogApplyResult{}, fmt.Errorf("保存模型目录来源地址失败: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return ModelCatalogApplyResult{}, fmt.Errorf("提交模型目录事务失败: %w", err)
	}
	return result, nil
}

func sortedSetValues(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// DeleteRetiredModelCatalogEntries 删除用户选择的来源已移除记录。
func DeleteRetiredModelCatalogEntries(database *sql.DB, stableKeys []string) (int, error) {
	transaction, err := database.Begin()
	if err != nil {
		return 0, fmt.Errorf("开始删除失效模型事务失败: %w", err)
	}
	defer transaction.Rollback()
	deleted := 0
	for _, key := range stableKeys {
		result, execErr := transaction.Exec(`DELETE FROM model_catalog WHERE stable_key = ? AND source_status = ?`, strings.TrimSpace(key), ModelSourceStatusRetired)
		if execErr != nil {
			return 0, fmt.Errorf("删除失效模型失败: %w", execErr)
		}
		count, _ := result.RowsAffected()
		deleted += int(count)
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("提交删除失效模型事务失败: %w", err)
	}
	return deleted, nil
}

func listModelCatalogBySource(database *sql.DB, sourceURL, adapter string) ([]ModelCatalogEntry, error) {
	rows, err := database.Query(`SELECT stable_key, base_model_id, model_id, display_name, vendor, provider, protocol, model_type, description, modalities_json, capabilities_json, context_window, max_input_tokens, max_output_tokens, pricing_json, currency, billing_unit, source_type, source_url, source_adapter, source_version, source_status, synced_at, published_at, created_at, updated_at FROM model_catalog WHERE source_url = ? AND source_adapter = ?`, sourceURL, adapter)
	if err != nil {
		return nil, fmt.Errorf("读取模型来源记录失败: %w", err)
	}
	defer rows.Close()
	entries := make([]ModelCatalogEntry, 0)
	for rows.Next() {
		entry, scanErr := scanModelCatalogEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func listAllModelCatalog(database *sql.DB) ([]ModelCatalogEntry, error) {
	rows, err := database.Query(`SELECT stable_key, base_model_id, model_id, display_name, vendor, provider, protocol, model_type, description, modalities_json, capabilities_json, context_window, max_input_tokens, max_output_tokens, pricing_json, currency, billing_unit, source_type, source_url, source_adapter, source_version, source_status, synced_at, published_at, created_at, updated_at FROM model_catalog`)
	if err != nil {
		return nil, fmt.Errorf("读取全部模型目录失败: %w", err)
	}
	defer rows.Close()
	entries := make([]ModelCatalogEntry, 0)
	for rows.Next() {
		entry, scanErr := scanModelCatalogEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

type modelCatalogScanner interface {
	Scan(dest ...any) error
}

func scanModelCatalogEntry(scanner modelCatalogScanner) (ModelCatalogEntry, error) {
	var entry ModelCatalogEntry
	var modalitiesJSON, capabilitiesJSON, pricingJSON string
	if err := scanner.Scan(&entry.StableKey, &entry.BaseModelID, &entry.ModelID, &entry.DisplayName, &entry.Vendor, &entry.Provider, &entry.Protocol, &entry.ModelType, &entry.Description, &modalitiesJSON, &capabilitiesJSON, &entry.ContextWindow, &entry.MaxInputTokens, &entry.MaxOutputTokens, &pricingJSON, &entry.Currency, &entry.BillingUnit, &entry.SourceType, &entry.SourceURL, &entry.SourceAdapter, &entry.SourceVersion, &entry.SourceStatus, &entry.SyncedAt, &entry.PublishedAt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
		return ModelCatalogEntry{}, fmt.Errorf("读取模型目录字段失败: %w", err)
	}
	if err := json.Unmarshal([]byte(modalitiesJSON), &entry.Modalities); err != nil {
		return ModelCatalogEntry{}, fmt.Errorf("解析模型模态失败: %w", err)
	}
	if err := json.Unmarshal([]byte(capabilitiesJSON), &entry.Capabilities); err != nil {
		return ModelCatalogEntry{}, fmt.Errorf("解析模型能力失败: %w", err)
	}
	if err := json.Unmarshal([]byte(pricingJSON), &entry.Pricing); err != nil {
		return ModelCatalogEntry{}, fmt.Errorf("解析模型费用失败: %w", err)
	}
	return entry, nil
}

func upsertModelCatalogEntry(database *sql.DB, entry ModelCatalogEntry, allowOnline bool) (sql.Result, error) {
	return upsertModelCatalogEntryTx(databaseTx{database}, entry, allowOnline)
}

type databaseTx struct{ database *sql.DB }

func (database databaseTx) Exec(query string, args ...any) (sql.Result, error) {
	return database.database.Exec(query, args...)
}

type catalogExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertModelCatalogEntryTx(executor catalogExecutor, entry ModelCatalogEntry, allowOnline bool) (sql.Result, error) {
	if entry.SourceType == ModelSourceTypeOnline && !allowOnline {
		return nil, errors.New("在线模型必须经过预览确认")
	}
	modalities, _ := json.Marshal(normalizeModalities(entry.Modalities))
	capabilities, _ := json.Marshal(uniqueStrings(entry.Capabilities))
	pricing, _ := json.Marshal(entry.Pricing)
	result, err := executor.Exec(`INSERT INTO model_catalog(stable_key, base_model_id, model_id, display_name, vendor, provider, protocol, model_type, description, modalities_json, capabilities_json, context_window, max_input_tokens, max_output_tokens, pricing_json, currency, billing_unit, source_type, source_url, source_adapter, source_version, source_status, synced_at, published_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(stable_key) DO UPDATE SET base_model_id=excluded.base_model_id, model_id=excluded.model_id, display_name=excluded.display_name, vendor=excluded.vendor, provider=excluded.provider, protocol=excluded.protocol, model_type=excluded.model_type, description=excluded.description, modalities_json=excluded.modalities_json, capabilities_json=excluded.capabilities_json, context_window=excluded.context_window, max_input_tokens=excluded.max_input_tokens, max_output_tokens=excluded.max_output_tokens, pricing_json=excluded.pricing_json, currency=excluded.currency, billing_unit=excluded.billing_unit, source_type=excluded.source_type, source_url=excluded.source_url, source_adapter=excluded.source_adapter, source_version=excluded.source_version, source_status=excluded.source_status, synced_at=excluded.synced_at, published_at=excluded.published_at, updated_at=excluded.updated_at`, entry.StableKey, entry.BaseModelID, entry.ModelID, entry.DisplayName, entry.Vendor, entry.Provider, entry.Protocol, entry.ModelType, entry.Description, string(modalities), string(capabilities), entry.ContextWindow, entry.MaxInputTokens, entry.MaxOutputTokens, string(pricing), entry.Currency, entry.BillingUnit, entry.SourceType, entry.SourceURL, entry.SourceAdapter, entry.SourceVersion, entry.SourceStatus, entry.SyncedAt, entry.PublishedAt, entry.CreatedAt, entry.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("保存模型目录失败: %w", err)
	}
	return result, nil
}

func sameCatalogEntry(left, right ModelCatalogEntry) bool {
	left.CreatedAt, right.CreatedAt = "", ""
	left.UpdatedAt, right.UpdatedAt = "", ""
	left.SyncedAt, right.SyncedAt = "", ""
	return string(mustJSON(left)) == string(mustJSON(right))
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
