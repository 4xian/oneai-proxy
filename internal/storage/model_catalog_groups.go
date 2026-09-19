package storage

import (
	"database/sql"
	"sort"
	"strings"
)

// ModelCatalogGroup 是模型管理主表使用的基础模型聚合结果。
type ModelCatalogGroup struct {
	GroupKey        string              `json:"groupKey"`
	BaseModelID     string              `json:"baseModelId"`
	DisplayName     string              `json:"displayName"`
	Vendors         []string            `json:"vendors"`
	ModelTypes      []string            `json:"modelTypes"`
	Capabilities    []string            `json:"capabilities"`
	ContextWindow   int64               `json:"contextWindow"`
	MaxOutputTokens int64               `json:"maxOutputTokens"`
	ProviderCount   int                 `json:"providerCount"`
	SourceStatuses  []string            `json:"sourceStatuses"`
	UpdatedAt       string              `json:"updatedAt"`
	Versions        []ModelCatalogEntry `json:"versions"`
}

// ModelCatalogGroupPage 是模型管理主表的分组分页结果。
type ModelCatalogGroupPage struct {
	Items    []ModelCatalogGroup `json:"items"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
	Total    int                 `json:"total"`
}

// ListModelCatalogGroups 按基础模型聚合筛选后的供应商版本并分页。
// PageSize 小于 1 时使用默认 20，大于 100 时夹到 100。
func ListModelCatalogGroups(database *sql.DB, query ModelCatalogQuery) (ModelCatalogGroupPage, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	entries, err := listAllModelCatalog(database)
	if err != nil {
		return ModelCatalogGroupPage{}, err
	}
	groups := make(map[string]*ModelCatalogGroup)
	providerSets := make(map[string]map[string]struct{})
	for _, entry := range entries {
		if !matchesModelCatalogGroupQuery(entry, query) {
			continue
		}
		baseModelID := strings.TrimSpace(entry.BaseModelID)
		if baseModelID == "" {
			baseModelID = strings.TrimSpace(entry.ModelID)
		}
		groupKey := strings.ToLower(baseModelID)
		group := groups[groupKey]
		if group == nil {
			group = &ModelCatalogGroup{GroupKey: groupKey, BaseModelID: baseModelID}
			groups[groupKey] = group
			providerSets[groupKey] = make(map[string]struct{})
		}
		appendModelCatalogVersion(group, providerSets[groupKey], entry)
	}
	items := make([]ModelCatalogGroup, 0, len(groups))
	for _, group := range groups {
		group.Vendors = sortedUniqueStrings(group.Vendors)
		group.ModelTypes = sortedUniqueStrings(group.ModelTypes)
		group.Capabilities = sortedUniqueStrings(group.Capabilities)
		group.SourceStatuses = sortedUniqueStrings(group.SourceStatuses)
		group.ProviderCount = len(providerSets[group.GroupKey])
		sort.Slice(group.Versions, func(left, right int) bool {
			leftKey := group.Versions[left].Provider + "\x00" + group.Versions[left].ModelID + "\x00" + string(group.Versions[left].Protocol)
			rightKey := group.Versions[right].Provider + "\x00" + group.Versions[right].ModelID + "\x00" + string(group.Versions[right].Protocol)
			return leftKey < rightKey
		})
		items = append(items, *group)
	}
	sort.Slice(items, func(left, right int) bool {
		leftName := strings.ToLower(defaultString(items[left].DisplayName, items[left].BaseModelID))
		rightName := strings.ToLower(defaultString(items[right].DisplayName, items[right].BaseModelID))
		if leftName == rightName {
			return items[left].GroupKey < items[right].GroupKey
		}
		return leftName < rightName
	})
	total := len(items)
	start := (query.Page - 1) * query.PageSize
	if start > total {
		start = total
	}
	end := start + query.PageSize
	if end > total {
		end = total
	}
	return ModelCatalogGroupPage{Items: items[start:end], Page: query.Page, PageSize: query.PageSize, Total: total}, nil
}

// matchesModelCatalogGroupQuery 判断供应商版本是否符合管理页筛选条件。
func matchesModelCatalogGroupQuery(entry ModelCatalogEntry, query ModelCatalogQuery) bool {
	if value := strings.ToLower(strings.TrimSpace(query.Search)); value != "" {
		fields := []string{entry.ModelID, entry.DisplayName, entry.BaseModelID}
		matched := false
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field), value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if query.Protocol != "" && entry.Protocol != query.Protocol {
		return false
	}
	if value := strings.TrimSpace(query.Vendor); value != "" && entry.Vendor != value {
		return false
	}
	if value := strings.TrimSpace(query.ModelType); value != "" && entry.ModelType != value {
		return false
	}
	if value := strings.TrimSpace(query.SourceStatus); value != "" && entry.SourceStatus != value {
		return false
	}
	if value := strings.TrimSpace(query.Capability); value != "" && !containsString(entry.Capabilities, value) {
		return false
	}
	return true
}

// appendModelCatalogVersion 将一条供应商版本合并到基础模型摘要。
func appendModelCatalogVersion(group *ModelCatalogGroup, providers map[string]struct{}, entry ModelCatalogEntry) {
	if group.DisplayName == "" && strings.TrimSpace(entry.DisplayName) != "" {
		group.DisplayName = entry.DisplayName
	}
	group.Vendors = append(group.Vendors, entry.Vendor)
	group.ModelTypes = append(group.ModelTypes, entry.ModelType)
	group.Capabilities = append(group.Capabilities, entry.Capabilities...)
	group.SourceStatuses = append(group.SourceStatuses, entry.SourceStatus)
	if entry.ContextWindow > group.ContextWindow {
		group.ContextWindow = entry.ContextWindow
	}
	if entry.MaxOutputTokens > group.MaxOutputTokens {
		group.MaxOutputTokens = entry.MaxOutputTokens
	}
	if entry.UpdatedAt > group.UpdatedAt {
		group.UpdatedAt = entry.UpdatedAt
	}
	providerKey := strings.TrimSpace(entry.Provider)
	if providerKey == "" {
		providerKey = entry.StableKey
	}
	providers[providerKey] = struct{}{}
	group.Versions = append(group.Versions, entry)
}

// sortedUniqueStrings 返回去除空白与重复项后的稳定排序结果。
func sortedUniqueStrings(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			unique[value] = struct{}{}
		}
	}
	return sortedSetValues(unique)
}

// containsString 判断切片是否包含指定完整值。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
