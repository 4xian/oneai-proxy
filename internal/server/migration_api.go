package server

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"

	"github.com/4xian/oneai-proxy/internal/storage"
)

const migrationMediaType = "application/vnd.oneai-proxy.migration+json"

// migrationExportAPI 生成独立于配置导出的跨平台完整迁移包。
func (s *Service) migrationExportAPI(writer http.ResponseWriter, request *http.Request) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	if err := s.acquireMigrationSlot(request.Context()); err != nil {
		writeJSON(writer, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}
	defer s.releaseMigrationSlot()
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "迁移包导出仅支持 POST"})
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if strings.TrimSpace(input.Password) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "导出迁移包必须提供非空口令"})
		return
	}
	document, err := s.buildExportDocument("complete_encrypted")
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	encoded, err := encodeMigrationPackage(migrationPayloadFromExport(document), input.Password)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("migration.export", "complete", request)
	writer.Header().Set("Content-Type", migrationMediaType)
	writer.Header().Set("Content-Disposition", `attachment; filename="oneai-proxy.oneai-migrate"`)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

// migrationPreviewAPI 解密并校验迁移包，返回不含秘密的替换预览。
func (s *Service) migrationPreviewAPI(writer http.ResponseWriter, request *http.Request) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	if err := s.acquireMigrationSlot(request.Context()); err != nil {
		writeJSON(writer, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}
	defer s.releaseMigrationSlot()
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "迁移包预览仅支持 POST"})
		return
	}
	encoded, ok := readMigrationRequest(writer, request)
	if !ok {
		return
	}
	payload, err := parseMigrationPackage(request.Context(), encoded, request.Header.Get("X-OneAI-Migration-Password"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	current, err := s.buildExportDocument("safe")
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, migrationPreviewResponse(current, payload))
}

// migrationImportAPI 在显式确认后原子替换可迁移配置并重建目标机秘密引用。
func (s *Service) migrationImportAPI(writer http.ResponseWriter, request *http.Request) {
	s.migrationBarrierMu.Lock()
	defer s.migrationBarrierMu.Unlock()
	s.dataMu.Lock()
	cleanupPendingCount := 0
	emitCleanupFailed := false
	func() {
		defer s.dataMu.Unlock()
		if err := s.acquireMigrationSlot(request.Context()); err != nil {
			writeJSON(writer, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
		defer s.releaseMigrationSlot()
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "迁移包导入仅支持 POST"})
			return
		}
		if !strings.EqualFold(strings.TrimSpace(request.Header.Get("X-OneAI-Migration-Confirm")), "true") {
			writeJSON(writer, http.StatusPreconditionRequired, map[string]string{"error": "请先预览迁移包并显式确认替换"})
			return
		}
		encoded, ok := readMigrationRequest(writer, request)
		if !ok {
			return
		}
		payload, err := parseMigrationPackage(request.Context(), encoded, request.Header.Get("X-OneAI-Migration-Password"))
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		current := s.snapshotSettings()
		backupPath, err := storage.CreateCompleteBackup(s.database, current.DataDirectory)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		backupManifest, err := storage.VerifyBackupManifest(backupPath, current.DataDirectory)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		oldChannels, err := storage.ListChannels(s.database)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		oldSecrets, err := readMigrationSecrets(s, oldChannels)
		if err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		pendingRefs, err := storage.LoadMigrationPendingSecretRefs(s.database)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		stagedRefs := make([]string, 0, len(payload.Channels))
		persistStagedRef := func(ref string) error {
			stagedRefs = append(stagedRefs, ref)
			markerRefs := append(append([]string{}, pendingRefs...), stagedRefs...)
			return storage.SaveMigrationPendingSecretRefs(s.database, markerRefs)
		}
		channels, _, err := s.prepareImportedChannelsWithHook(payload.Channels, "complete_encrypted", persistStagedRef)
		if err != nil {
			s.cleanupMigrationStagedRefs(stagedRefs)
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		settings, err := importSettings(payload.Settings, current.DataDirectory)
		if err != nil {
			s.cleanupMigrationStagedRefs(stagedRefs)
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		auth, err := storage.NewAuthTokens(payload.Tokens.AdminToken, payload.Tokens.ProxyToken)
		if err != nil {
			s.cleanupMigrationStagedRefs(stagedRefs)
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		previousAuth := s.auth
		if persistErr := storage.PersistAuthTokenSecrets(s.secrets, auth); persistErr != nil {
			s.cleanupMigrationStagedRefs(stagedRefs)
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": persistErr.Error()})
			return
		}
		cleanupCandidates := make(map[string][]byte, len(oldSecrets)+len(pendingRefs))
		for ref, value := range oldSecrets {
			cleanupCandidates[ref] = value
		}
		for _, ref := range pendingRefs {
			if _, alreadyCurrent := cleanupCandidates[ref]; alreadyCurrent {
				continue
			}
			referenced, referenceErr := storage.IsSecretRefReferenced(s.database, ref)
			if referenceErr != nil {
				s.cleanupMigrationStagedRefs(stagedRefs)
				writeStorageError(writer, referenceErr)
				return
			}
			if referenced {
				continue
			}
			cleanupCandidates[ref] = nil
		}
		result, err := storage.ReplaceMigrationConfiguration(s.database, storage.MigrationConfiguration{
			Channels: channels, ChannelModels: migrationChannelModels(payload.ChannelModels),
			GlobalModelMappings: payload.GlobalModelMappings,
			ChannelModelMappings: payload.ChannelModelMappings, ProbePolicies: payload.ProbePolicies,
			Settings: settings, ModelCatalog: payload.ModelCatalog,
			ModelCatalogSourceURL: payload.Settings.ModelCatalogSourceURL, Auth: auth,
			PendingSecretRefs: migrationSecretRefs(cleanupCandidates),
		})
		if err != nil {
			if previousAuth.AdminToken != "" && previousAuth.ProxyToken != "" {
				if restoreSecretErr := storage.PersistAuthTokenSecrets(s.secrets, previousAuth); restoreSecretErr != nil {
					s.logger.Error("迁移失败后恢复鉴权令牌失败", "error", restoreSecretErr)
				}
			}
			restoreErr := storage.RestoreDatabaseSnapshot(s.database, backupManifest.Database)
			if restoreErr != nil {
				s.logger.Error("迁移失败后恢复 SQLite 快照失败", "error", restoreErr)
				preserveRefs := append(append([]string{}, stagedRefs...), migrationSecretRefs(cleanupCandidates)...)
				if preserveErr := s.retainPendingSecretRefs(preserveRefs); preserveErr != nil {
					s.logger.Error("SQLite 快照恢复失败后保留暂存凭证记录失败", "error", preserveErr)
				}
			} else if cleanupErr := s.cleanupMigrationStagedRefs(stagedRefs); cleanupErr != nil {
				s.logger.Warn("迁移失败后清理暂存凭证失败", "error", cleanupErr)
			}
			writeStorageError(writer, err)
			return
		}
		// 迁移提交后立即应用可热更新策略；监听地址和鉴权令牌仍按契约等待重启生效。
		next := current
		next.RequestPolicy = settings.RequestPolicy
		next.ChannelSettings = settings.ChannelSettings
		next.Logging = settings.Logging
		next.Timezone = settings.Timezone
		next.ProxyListen = settings.ProxyListen
		next.AdminListen = settings.AdminListen
		s.publishSettings(next)
		deletedRefs, cleanupErr := deleteMigrationSecrets(s, cleanupCandidates)
		remainingRefs := subtractMigrationSecretRefs(migrationSecretRefs(cleanupCandidates), deletedRefs)
		if cleanupErr != nil {
			cleanupPendingCount = len(remainingRefs)
			emitCleanupFailed = true
			if saveErr := storage.SaveMigrationPendingSecretRefs(s.database, remainingRefs); saveErr != nil {
				s.logger.Warn("更新迁移待清理凭证记录失败", "error", saveErr)
			}
			s.logger.Warn("迁移完成后清理旧渠道凭证失败", "error", cleanupErr)
		} else if len(cleanupCandidates) > 0 {
			if clearErr := storage.ClearMigrationPendingSecretRefs(s.database); clearErr != nil {
				s.logger.Warn("清除迁移待清理凭证记录失败", "error", clearErr)
			}
		}
		s.recordAudit("migration.import", fmt.Sprintf("channels=%d", len(channels)), request)
		writeJSON(writer, http.StatusOK, map[string]any{
			"backupPath": backupPath, "channelCount": len(channels),
			"placeholderCount": len(result.PlaceholderChannelIDs),
			"cleanupPendingCount": cleanupPendingCount, "restartRequired": true,
			"adminToken": payload.Tokens.AdminToken, "proxyToken": payload.Tokens.ProxyToken,
		})
	}()
	if emitCleanupFailed {
		s.emitRuntime("warn", "migration.old_secrets_cleanup.failed", "迁移完成但旧渠道凭证清理失败", map[string]any{"pendingCount": cleanupPendingCount})
	}
}

func migrationPayloadFromExport(document exportDocument) migrationPayload {
	return migrationPayload{
		Format: migrationPayloadFormat, MigrationVersion: migrationVersion,
		ConfigSchemaVersion: exportSchemaVersion, DatabaseSchemaVersion: storage.CurrentSchemaVersion(),
		Settings: document.Settings, Channels: document.Channels, ChannelModels: document.ChannelModels,
		GlobalModelMappings: document.GlobalModelMappings,
		ChannelModelMappings: document.ChannelModelMappings, ProbePolicies: document.ProbePolicies,
		ModelCatalog: document.ModelCatalog, Tokens: *document.Tokens,
	}
}

func migrationPreviewResponse(current exportDocument, incoming migrationPayload) map[string]any {
	channelPreview := make([]map[string]any, 0, len(incoming.Channels))
	for _, channel := range incoming.Channels {
		channelPreview = append(channelPreview, map[string]any{
			"id": channel.ID, "name": channel.Name, "protocol": channel.Protocol,
			"adminState": channel.AdminState, "credentialPresent": channel.Credential != nil,
		})
	}
	return map[string]any{
		"current":  migrationCounts(current.Channels, current.ChannelModels, current.GlobalModelMappings, current.ChannelModelMappings, current.ProbePolicies, current.ModelCatalog),
		"incoming": migrationCounts(incoming.Channels, incoming.ChannelModels, incoming.GlobalModelMappings, incoming.ChannelModelMappings, incoming.ProbePolicies, incoming.ModelCatalog),
		"settings": map[string]any{
			"proxyListener": incoming.Settings.ProxyListener, "adminListener": incoming.Settings.AdminListener,
			"timezone": incoming.Settings.Timezone, "adminTokenPresent": incoming.Tokens.AdminToken != "",
			"proxyTokenPresent": incoming.Tokens.ProxyToken != "",
		},
		"channels": channelPreview, "restartRequired": true,
	}
}

func migrationCounts(channels []exportChannel, channelModels []storage.ChannelModel, global storage.GlobalModelMappingSet, channelMappings []storage.ChannelModelMapping, policies []storage.ProbePolicy, catalog []storage.ModelCatalogEntry) map[string]int {
	return map[string]int{
		"channels": len(channels), "channelModels": len(channelModels),
		"globalModelMappings":  len(global.OpenAI) + len(global.Anthropic),
		"channelModelMappings": len(channelMappings), "probePolicies": len(policies), "modelCatalog": len(catalog),
	}
}

func migrationChannelModels(models []storage.ChannelModel) map[string][]string {
	result := make(map[string][]string)
	for _, model := range models {
		result[model.ChannelID] = append(result[model.ChannelID], model.Model)
	}
	return result
}

func readMigrationRequest(writer http.ResponseWriter, request *http.Request) ([]byte, bool) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" && mediaType != migrationMediaType {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "迁移包 Content-Type 必须是 application/json 或迁移包媒体类型"})
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, migrationMaxEnvelopeBytes))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "读取迁移包失败或文件超过大小限制"})
		return nil, false
	}
	return body, true
}

// retainPendingSecretRefs 合并保存尚未清理的凭证引用，供启动时安全重试。
func (s *Service) retainPendingSecretRefs(refs []string) error {
	refs = uniqueMigrationSecretRefs(refs)
	if len(refs) == 0 {
		return nil
	}
	existing, err := storage.LoadMigrationPendingSecretRefs(s.database)
	if err != nil {
		return fmt.Errorf("读取暂存凭证待清理记录失败: %w", err)
	}
	refs = uniqueMigrationSecretRefs(append(existing, refs...))
	if err := storage.SaveMigrationPendingSecretRefs(s.database, refs); err != nil {
		return fmt.Errorf("保存暂存凭证待清理记录失败: %w", err)
	}
	return nil
}

func (s *Service) cleanupMigrationStagedRefs(refs []string) error {
	refs = uniqueMigrationSecretRefs(refs)
	if len(refs) == 0 {
		return nil
	}
	existing, err := storage.LoadMigrationPendingSecretRefs(s.database)
	if err != nil {
		return fmt.Errorf("读取暂存凭证待清理记录失败: %w", err)
	}
	refs = uniqueMigrationSecretRefs(append(refs, existing...))
	failed := make([]string, 0, len(refs))
	for _, ref := range refs {
		referenced, referenceErr := storage.IsSecretRefReferenced(s.database, ref)
		if referenceErr != nil {
			failed = append(failed, ref)
			s.logger.Warn("检查暂存凭证引用失败", "secretRef", ref, "error", referenceErr)
			continue
		}
		if referenced {
			continue
		}
		if err := s.secrets.Delete(ref); err != nil {
			failed = append(failed, ref)
			s.logger.Warn("清理迁移暂存凭证失败", "secretRef", ref, "error", err)
		}
	}
	if len(failed) == 0 {
		if err := storage.ClearMigrationPendingSecretRefs(s.database); err != nil {
			return fmt.Errorf("清除暂存凭证待清理记录失败: %w", err)
		}
		return nil
	}
	if err := storage.SaveMigrationPendingSecretRefs(s.database, failed); err != nil {
		return fmt.Errorf("更新暂存凭证待清理记录失败: %w", err)
	}
	return fmt.Errorf("%d 个暂存凭证待后续清理", len(failed))
}

func uniqueMigrationSecretRefs(refs []string) []string {
	seen := make(map[string]struct{}, len(refs))
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if _, exists := seen[ref]; exists {
			continue
		}
		seen[ref] = struct{}{}
		result = append(result, ref)
	}
	return result
}

// acquireMigrationSlot 限制 Argon2 迁移运算并响应客户端取消，避免并发导入耗尽内存。
func (s *Service) acquireMigrationSlot(ctx context.Context) error {
	select {
	case s.migrationSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return fmt.Errorf("迁移操作正在进行，请稍后重试")
	}
}

// releaseMigrationSlot 释放一次迁移运算配额。
func (s *Service) releaseMigrationSlot() {
	<-s.migrationSlots
}

func readMigrationSecrets(service *Service, channels []storage.Channel) (map[string][]byte, error) {
	secrets := make(map[string][]byte)
	for _, channel := range channels {
		ref := strings.TrimSpace(channel.SecretRef)
		if ref == "" {
			continue
		}
		if _, exists := secrets[ref]; exists {
			continue
		}
		value, err := service.secrets.Get(ref)
		if err != nil {
			return nil, fmt.Errorf("读取旧渠道凭证失败: %s: %w", channel.ID, err)
		}
		secrets[ref] = value
	}
	return secrets, nil
}

func migrationSecretRefs(secrets map[string][]byte) []string {
	refs := make([]string, 0, len(secrets))
	for ref := range secrets {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func deleteMigrationSecrets(service *Service, secrets map[string][]byte) ([]string, error) {
	deleted := make([]string, 0, len(secrets))
	for _, ref := range migrationSecretRefs(secrets) {
		if err := service.secrets.Delete(ref); err != nil {
			return deleted, fmt.Errorf("清理旧渠道凭证失败: %w", err)
		}
		deleted = append(deleted, ref)
	}
	return deleted, nil
}

func subtractMigrationSecretRefs(refs, deleted []string) []string {
	deletedSet := make(map[string]struct{}, len(deleted))
	for _, ref := range deleted {
		deletedSet[ref] = struct{}{}
	}
	remaining := make([]string, 0, len(refs))
	for _, ref := range refs {
		if _, deleted := deletedSet[ref]; !deleted {
			remaining = append(remaining, ref)
		}
	}
	return remaining
}
