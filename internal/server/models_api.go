package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/storage"
)

const (
	defaultModelCatalogSource = "https://models.dev/catalog.json"
	modelCatalogTimeout       = 15 * time.Second
	modelCatalogMaxBytes      = 64 << 20
)

type modelCatalogSourcePayload struct {
	SourceURL string `json:"sourceUrl"`
}

type modelCatalogApplyPayload struct {
	SourceURL          string   `json:"sourceUrl"`
	SourceDigest       string   `json:"sourceDigest"`
	SelectedStableKeys []string `json:"selectedStableKeys"`
}

type globalMappingPayload struct {
	ProtocolFamily string          `json:"protocolFamily"`
	Mapping        json.RawMessage `json:"mapping"`
}

// modelsAPI 提供模型目录、在线同步和协议族全局映射管理接口。
// preview/apply 单独分发，拉取模型源期间不持有 dataMu。
func (s *Service) modelsAPI(writer http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/admin/v1/models"), "/")
	if path == "preview" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "模型源预览仅支持 POST"})
			return
		}
		s.handleModelCatalogPreview(writer, request)
		return
	}
	if path == "apply" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "模型源应用仅支持 POST"})
			return
		}
		s.handleModelCatalogApply(writer, request)
		return
	}
	if request.Method == http.MethodGet {
		s.dataMu.RLock()
		defer s.dataMu.RUnlock()
	} else {
		s.dataMu.Lock()
		defer s.dataMu.Unlock()
	}
	if path == "catalog" || path == "" && request.Method == http.MethodGet {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "模型目录仅支持 GET"})
			return
		}
		s.handleModelCatalogList(writer, request)
		return
	}
	if path == "" && request.Method == http.MethodPost {
		s.handleManualModelCreate(writer, request)
		return
	}
	if path == "retired" {
		if request.Method != http.MethodDelete {
			writer.Header().Set("Allow", http.MethodDelete)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "失效模型删除仅支持 DELETE"})
			return
		}
		s.handleRetiredModelDelete(writer, request)
		return
	}
	if path == "batch" {
		if request.Method != http.MethodDelete {
			writer.Header().Set("Allow", http.MethodDelete)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "模型批量删除仅支持 DELETE"})
			return
		}
		s.handleModelBatchDelete(writer, request)
		return
	}
	if path == "global-mappings" {
		s.handleGlobalMappings(writer, request)
		return
	}
	if request.Method == http.MethodPost && !strings.Contains(path, "/") {
		s.handleManualModelCreate(writer, request)
		return
	}
	if strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	if request.Method == http.MethodPut {
		s.handleModelUpdate(writer, request, path)
		return
	}
	if request.Method == http.MethodDelete {
		s.handleModelDelete(writer, request, path)
		return
	}
	writer.Header().Set("Allow", "GET, POST, PUT, DELETE")
	writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "模型目录操作不支持该方法"})
}

// handleModelCatalogList 返回分页、筛选后的模型目录和最近来源地址。
func (s *Service) handleModelCatalogList(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	page := parsePositiveInt(query.Get("page"), 1)
	pageSize := parsePositiveInt(query.Get("pageSize"), 20)
	catalogQuery := storage.ModelCatalogQuery{
		Search: query.Get("search"), Protocol: storage.Protocol(query.Get("protocol")), Vendor: query.Get("vendor"), ModelType: query.Get("modelType"), Capability: query.Get("capability"), SourceStatus: query.Get("sourceStatus"), Page: page, PageSize: pageSize,
	}
	var items any
	var total, resultPage, resultPageSize int
	if query.Get("grouped") == "true" {
		pageResult, err := storage.ListModelCatalogGroups(s.database, catalogQuery)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		items, total, resultPage, resultPageSize = pageResult.Items, pageResult.Total, pageResult.Page, pageResult.PageSize
	} else {
		pageResult, err := storage.ListModelCatalog(s.database, catalogQuery)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		items, total, resultPage, resultPageSize = pageResult.Items, pageResult.Total, pageResult.Page, pageResult.PageSize
	}
	sourceURL, err := storage.LoadModelCatalogSourceURL(s.database)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	facets, err := storage.ListModelCatalogFacets(s.database)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "page": resultPage, "pageSize": resultPageSize, "total": total, "sourceUrl": sourceURL, "defaultSourceUrl": defaultModelCatalogSource, "facets": facets})
}

// handleModelCatalogPreview 请求公开 JSON 模型源并返回不写库的差异预览。
// 拉取源文件期间不持有 dataMu，仅在对比本地目录时加读锁。
func (s *Service) handleModelCatalogPreview(writer http.ResponseWriter, request *http.Request) {
	var payload modelCatalogSourcePayload
	if !decodeJSON(writer, request, &payload) {
		return
	}
	sourceURL := strings.TrimSpace(payload.SourceURL)
	if sourceURL == "" {
		sourceURL = defaultModelCatalogSource
	}
	parsedURL, err := validateModelSourceURL(sourceURL)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	body, err := s.fetchModelCatalog(request.Context(), parsedURL)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	parsed, err := storage.ParseModelCatalogDocument(parsedURL, body)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	preview, err := s.buildModelCatalogPreview(parsed, parsedURL)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	stats := map[string]int{}
	for _, item := range preview.Items {
		stats[item.ChangeType]++
	}
	writeJSON(writer, http.StatusOK, map[string]any{"sourceUrl": preview.SourceURL, "sourceDigest": modelCatalogDigest(body), "adapter": preview.Adapter, "items": preview.Items, "stats": stats, "parsed": len(parsed.Entries), "skipped": parsed.Skipped, "errors": parsed.Errors})
}

// handleModelCatalogApply 重新读取并解析来源后，只事务应用用户勾选的稳定键。
// 拉取、摘要校验和解析期间不持有 dataMu，仅在对比并写库时加写锁。
func (s *Service) handleModelCatalogApply(writer http.ResponseWriter, request *http.Request) {
	var payload modelCatalogApplyPayload
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if len(payload.SelectedStableKeys) > 10000 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "一次应用的模型数量过多"})
		return
	}
	parsedURL, err := validateModelSourceURL(payload.SourceURL)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	body, err := s.fetchModelCatalog(request.Context(), parsedURL)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	if strings.TrimSpace(payload.SourceDigest) == "" || payload.SourceDigest != modelCatalogDigest(body) {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": "模型源内容已变化，请重新预览后再应用"})
		return
	}
	parsed, err := storage.ParseModelCatalogDocument(parsedURL, body)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	result, err := s.applyModelCatalogPreview(parsed, parsedURL, payload.SelectedStableKeys)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("models.catalog.apply", fmt.Sprintf("%d", result.Applied), request)
	writeJSON(writer, http.StatusOK, map[string]any{"applied": result.Applied, "skipped": result.Skipped, "conflicts": result.Conflicts, "sourceUrl": parsedURL})
}

// handleManualModelCreate 新增一条手工模型目录记录。
func (s *Service) handleManualModelCreate(writer http.ResponseWriter, request *http.Request) {
	var entry storage.ModelCatalogEntry
	if !decodeJSON(writer, request, &entry) {
		return
	}
	saved, err := storage.SaveManualModelCatalogEntry(s.database, entry)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("models.catalog.create", saved.StableKey, request)
	writeJSON(writer, http.StatusCreated, saved)
}

// handleModelUpdate 更新一条模型目录记录。
func (s *Service) handleModelUpdate(writer http.ResponseWriter, request *http.Request, stableKey string) {
	var entry storage.ModelCatalogEntry
	if !decodeJSON(writer, request, &entry) {
		return
	}
	saved, err := storage.UpdateModelCatalogEntry(s.database, stableKey, entry)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("models.catalog.update", saved.StableKey, request)
	writeJSON(writer, http.StatusOK, saved)
}

// handleModelDelete 删除一条模型目录记录。
func (s *Service) handleModelDelete(writer http.ResponseWriter, request *http.Request, stableKey string) {
	if err := storage.DeleteModelCatalogEntry(s.database, stableKey); err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("models.catalog.delete", stableKey, request)
	writeJSON(writer, http.StatusOK, map[string]bool{"deleted": true})
}

// handleRetiredModelDelete 删除用户明确勾选的来源已移除模型。
func (s *Service) handleRetiredModelDelete(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		StableKeys []string `json:"stableKeys"`
	}
	if !decodeJSON(writer, request, &payload) {
		return
	}
	deleted, err := storage.DeleteRetiredModelCatalogEntries(s.database, payload.StableKeys)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("models.catalog.retired.delete", fmt.Sprintf("%d", deleted), request)
	writeJSON(writer, http.StatusOK, map[string]int{"deleted": deleted})
}

// handleModelBatchDelete 删除用户选择的模型目录记录。
func (s *Service) handleModelBatchDelete(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		StableKeys []string `json:"stableKeys"`
	}
	if !decodeJSON(writer, request, &payload) {
		return
	}
	deleted, err := storage.DeleteModelCatalogEntries(s.database, payload.StableKeys)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("models.catalog.batch.delete", fmt.Sprintf("%d", deleted), request)
	writeJSON(writer, http.StatusOK, map[string]int{"deleted": deleted})
}

// handleGlobalMappings 读取或替换一个协议族的扁平 JSON 映射。
func (s *Service) handleGlobalMappings(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		mapping, err := storage.LoadGlobalModelMappingSet(s.database)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"globalModelMappings": mapping})
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "全局模型映射仅支持 GET 和 PUT"})
		return
	}
	var payload globalMappingPayload
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if len(payload.Mapping) == 0 || strings.TrimSpace(string(payload.Mapping)) == "null" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "mapping 必须是 JSON 对象"})
		return
	}
	mapping, err := decodeFlatStringMap(payload.Mapping)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "mapping 必须是键和值均为字符串的 JSON 对象"})
		return
	}
	if err := storage.SaveGlobalModelMappingFamily(s.database, strings.TrimSpace(payload.ProtocolFamily), mapping); err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("models.global-mapping.save", payload.ProtocolFamily, request)
	writeJSON(writer, http.StatusOK, map[string]any{"saved": true, "protocolFamily": payload.ProtocolFamily, "count": len(mapping)})
}

// decodeFlatStringMap 解码扁平字符串对象，并在标准 JSON 解码覆盖前拒绝重复键。
func decodeFlatStringMap(raw json.RawMessage) (map[string]string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("mapping 必须是 JSON 对象")
	}
	result := make(map[string]string)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("mapping 键必须是字符串")
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("mapping 键重复: %s", key)
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("mapping 值必须是字符串")
		}
		result[key] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("mapping 必须是完整 JSON 对象")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("mapping 不能包含额外内容")
	}
	return result, nil
}

// modelCatalogDigest 返回预览与确认阶段共同使用的原始源内容摘要。
func modelCatalogDigest(body []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func validateModelSourceURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", errors.New("模型源必须是公开 HTTP(S) 地址，不能包含用户信息")
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

// buildModelCatalogPreview 在读锁内对比本地目录，HTTP 拉取已在调用方完成。
func (s *Service) buildModelCatalogPreview(parsed storage.ModelCatalogParseResult, parsedURL string) (storage.ModelCatalogPreview, error) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	return storage.BuildModelCatalogPreview(s.database, parsed, parsedURL)
}

// applyModelCatalogPreview 在写锁内重新对比并事务应用勾选项，HTTP 拉取已在调用方完成。
func (s *Service) applyModelCatalogPreview(parsed storage.ModelCatalogParseResult, parsedURL string, selectedStableKeys []string) (storage.ModelCatalogApplyResult, error) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	preview, err := storage.BuildModelCatalogPreview(s.database, parsed, parsedURL)
	if err != nil {
		return storage.ModelCatalogApplyResult{}, err
	}
	return storage.ApplyModelCatalogPreview(s.database, preview, selectedStableKeys, time.Now().UTC())
}

// fetchModelCatalog 拉取公开模型源 JSON，超时 15 秒且不超过 modelCatalogMaxBytes。
func (s *Service) fetchModelCatalog(parent context.Context, sourceURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, modelCatalogTimeout)
	defer cancel()
	client := &http.Client{Timeout: modelCatalogTimeout, CheckRedirect: func(request *http.Request, _ []*http.Request) error {
		if _, err := validateModelSourceURL(request.URL.String()); err != nil {
			return err
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建模型源请求失败: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("读取模型源失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("模型源返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, modelCatalogMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取模型源响应失败: %w", err)
	}
	if len(body) > modelCatalogMaxBytes {
		return nil, fmt.Errorf("模型源响应超过 %d MiB 限制", modelCatalogMaxBytes>>20)
	}
	return body, nil
}

func parsePositiveInt(value string, fallback int) int {
	var parsed int
	if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}
