package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/storage"
)

const (
	maxSSEPrecommitBytes = 256 << 10
	responseAffinityTTL  = 24 * time.Hour
)

func nullableHTTPStatus(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

// buildRequestFinishMeta 将最终响应元数据转换为请求账本终态字段。
func buildRequestFinishMeta(meta proxyResponseMeta, startedAt time.Time, attemptCount int, fallbackTriggered bool) storage.RequestFinishMeta {
	tokens := proxyMetaTokens(meta)
	var cacheRate *float64
	if tokens.input != nil && tokens.cache != nil && *tokens.input > 0 {
		value := float64(*tokens.cache) / float64(*tokens.input)
		cacheRate = &value
	}
	var ttft *time.Duration
	if meta.FirstEventAt != nil {
		value := meta.FirstEventAt.Sub(startedAt)
		if value >= 0 {
			ttft = &value
		}
	}
	var tps *float64
	if tokens.output != nil && meta.FirstEventAt != nil {
		completedAt := time.Now()
		if meta.CompletedAt != nil {
			completedAt = *meta.CompletedAt
		}
		if elapsed := completedAt.Sub(*meta.FirstEventAt).Seconds(); elapsed > 0 {
			value := float64(*tokens.output) / elapsed
			tps = &value
		}
	}
	fallback := fallbackTriggered
	return storage.RequestFinishMeta{
		InputTokens:           tokens.input,
		OutputTokens:          tokens.output,
		CacheReadInputTokens:  tokens.cache,
		CacheWriteInputTokens: cacheWriteProxyTokens(meta),
		ReasoningTokens:       reasoningProxyTokens(meta),
		TotalTokens:           totalProxyTokens(meta),
		CacheRate:             cacheRate,
		TTFT:                  ttft,
		TPS:                   tps,
		StreamEnded:           meta.StreamEnded,
		FinalHTTPStatus:       nullableHTTPStatus(meta.HTTPStatus),
		FinalChannelID:        meta.FinalChannelID,
		FinalChannelName:      meta.FinalChannelName,
		AttemptCount:          attemptCount,
		FallbackTriggered:     &fallback,
		ErrorClass:            meta.ErrorClass,
		ErrorMessage:          meta.ErrorMessage,
		Latency:               time.Since(startedAt),
	}
}

var hopByHopHeaders = map[string]struct{}{
	"Connection": {}, "Keep-Alive": {}, "Proxy-Authenticate": {}, "Proxy-Authorization": {},
	"Te": {}, "Trailer": {}, "Transfer-Encoding": {}, "Upgrade": {},
}

var sensitiveHeaders = map[string]struct{}{
	"Authorization": {}, "Api-Key": {}, "X-Api-Key": {},
}

var errLedgerWrite = errors.New("请求日志持久化失败")

var errFirstByteTimeout = errors.New("首字节超时")

var errStreamIdleTimeout = errors.New("流空闲超时")

var errUpstreamStreamInterrupted = errors.New("上游流中断")

var errDownstreamStreamWrite = errors.New("下游流写入失败")

type proxyUpstreamError struct {
	status              int
	headers             http.Header
	body                []byte
	modelNotFound       bool
	permissionConfirmed bool
	protocolClass       string
	message             string
	skipFallback        bool
	skipHealthFailure   bool
}

type routingError struct {
	status  int
	class   string
	message string
}

// newRoutingError 创建可直接返回客户端的本地路由终态。
func newRoutingError(status int, class, message string) error {
	return &routingError{status: status, class: class, message: message}
}

// Error 返回路由终态的安全摘要。
func (err *routingError) Error() string {
	return err.message
}

// Error 返回不包含密钥的上游 HTTP 错误摘要。
func (err *proxyUpstreamError) Error() string {
	if strings.TrimSpace(err.message) != "" {
		return err.message
	}
	return fmt.Sprintf("上游返回 HTTP %d", err.status)
}

// classifyProxyError 将 Attempt 错误归入阶段 4 的最小错误分类。
func classifyProxyError(err error, contextErr error) string {
	var routeErr *routingError
	if errors.As(err, &routeErr) {
		return routeErr.class
	}
	if errors.Is(err, errLedgerWrite) {
		return "ledger_error"
	}
	if errors.Is(err, errStreamIdleTimeout) {
		return "stream_idle_timeout"
	}
	if errors.Is(err, errFirstByteTimeout) {
		return "first_byte_timeout"
	}
	if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if contextErr != nil {
		return "cancelled"
	}
	if errors.Is(err, errResponseCommitted) {
		return "stream_postcommit_disconnect"
	}
	var upstreamErr *proxyUpstreamError
	if errors.As(err, &upstreamErr) {
		if upstreamErr.modelNotFound {
			return "model_not_found"
		}
		if upstreamErr.protocolClass != "" {
			return upstreamErr.protocolClass
		}
		switch {
		case upstreamErr.status == http.StatusUnauthorized:
			return "upstream_auth_401"
		case upstreamErr.status == http.StatusForbidden:
			return "upstream_403"
		case upstreamErr.status == http.StatusRequestTimeout:
			return "http_408"
		case upstreamErr.status == http.StatusConflict:
			return "http_409"
		case upstreamErr.status == http.StatusTooManyRequests:
			return "http_429"
		case upstreamErr.status >= 500:
			return "http_5xx"
		default:
			return "upstream_http"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "network_interrupted"
	}
	return "upstream_protocol"
}

// shouldFallback 判断错误是否允许切换到下一个渠道。
func shouldFallback(err error) bool {
	if err == nil || errors.Is(err, errResponseCommitted) || errors.Is(err, errLedgerWrite) {
		return false
	}
	var upstreamErr *proxyUpstreamError
	if errors.As(err, &upstreamErr) {
		if upstreamErr.skipFallback {
			return false
		}
		if upstreamErr.modelNotFound {
			return true
		}
		switch upstreamErr.status {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
			return false
		default:
			return true
		}
	}
	return true
}

// shouldCountHealthFailure 判断一次渠道级失败是否应影响自动健康状态。
func shouldCountHealthFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errLedgerWrite) {
		return false
	}
	if errors.Is(err, errResponseCommitted) {
		return errors.Is(err, errUpstreamStreamInterrupted) && !errors.Is(err, errDownstreamStreamWrite)
	}
	var upstreamErr *proxyUpstreamError
	if errors.As(err, &upstreamErr) {
		if upstreamErr.skipHealthFailure {
			return false
		}
		if upstreamErr.modelNotFound {
			return false
		}
		if upstreamErr.status == http.StatusForbidden {
			return upstreamErr.permissionConfirmed
		}
		switch upstreamErr.status {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
			return false
		}
	}
	return true
}

// isTotalRequestBudgetTimeout 判断失败是否来自整次请求总超时，而不是渠道自身超时。
func isTotalRequestBudgetTimeout(err error, requestErr error) bool {
	if !errors.Is(requestErr, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errFirstByteTimeout) || errors.Is(err, errStreamIdleTimeout) {
		return false
	}
	return true
}

// newProxyUpstreamError 保存上游错误并由对应协议适配规则标注健康判定所需信息。
func newProxyUpstreamError(protocol storage.Protocol, status int, headers http.Header, body []byte) *proxyUpstreamError {
	modelNotFound, permissionConfirmed := classifyProtocolHTTPError(protocol, status, body)
	return &proxyUpstreamError{status: status, headers: headers, body: body, modelNotFound: modelNotFound, permissionConfirmed: permissionConfirmed}
}

func newProxyProtocolError(status int, headers http.Header, body []byte, class, message string, modelNotFound bool) *proxyUpstreamError {
	if status <= 0 {
		status = http.StatusOK
	}
	if message == "" {
		message = "上游协议执行失败"
	}
	return &proxyUpstreamError{status: status, headers: headers, body: body, modelNotFound: modelNotFound, protocolClass: class, message: message}
}

// classifyProtocolHTTPError 仅识别协议错误正文明确表达的模型不存在或权限失败。
func classifyProtocolHTTPError(protocol storage.Protocol, status int, body []byte) (bool, bool) {
	switch protocol {
	case storage.ProtocolOpenAIChat, storage.ProtocolOpenAIResponses, storage.ProtocolAnthropicMessages:
	default:
		return false, false
	}
	payload, ok := decodeProtocolErrorPayload(body)
	if !ok {
		return false, false
	}
	modelNotFound := protocolErrorIsModelNotFound(payload)
	permissionConfirmed := false
	if status == http.StatusForbidden {
		permissionConfirmed = protocolErrorIsPermissionDenied(payload)
	}
	return modelNotFound, permissionConfirmed
}

func decodeProtocolErrorPayload(body []byte) (map[string]any, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, false
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func protocolErrorIsModelNotFound(payload map[string]any) bool {
	code := strings.ToLower(protocolErrorField(payload, "code"))
	typ := strings.ToLower(protocolErrorField(payload, "type"))
	param := strings.ToLower(protocolErrorField(payload, "param"))
	if code == "model_not_found" || typ == "model_not_found" {
		return true
	}
	if param == "model" && (strings.Contains(code, "not_found") || strings.Contains(typ, "not_found")) {
		return true
	}
	return protocolErrorMessageIsModelNotFound(strings.ToLower(protocolErrorField(payload, "message")))
}

// protocolErrorMessageIsModelNotFound 仅匹配错误对象中有限、明确的模型不存在文案。
func protocolErrorMessageIsModelNotFound(message string) bool {
	if message == "" {
		return false
	}
	return strings.Contains(message, "model_not_found") ||
		strings.Contains(message, "model not found") ||
		strings.Contains(message, "model does not exist")
}

func protocolErrorIsPermissionDenied(payload map[string]any) bool {
	code := strings.ToLower(protocolErrorField(payload, "code"))
	typ := strings.ToLower(protocolErrorField(payload, "type"))
	switch code {
	case "permission_denied", "permission-denied", "access_denied", "access-denied", "permission_error", "permissions_error":
		return true
	}
	switch typ {
	case "permission_denied", "permission-denied", "access_denied", "access-denied", "permission_error", "permissions_error":
		return true
	}
	return false
}

func protocolErrorField(payload map[string]any, key string) string {
	obj := protocolErrorObject(payload)
	if obj == nil {
		return ""
	}
	if value, ok := obj[key]; ok {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// protocolErrorObject 取出真正的协议错误对象，忽略 envelope 顶层 type。
func protocolErrorObject(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	if nested, ok := payload["error"]; ok {
		if obj, ok := nested.(map[string]any); ok {
			return obj
		}
	}
	return payload
}

// protocolErrorSummary 组合上游错误代码和摘要；缺少 message 时回退到调用方文案。
func protocolErrorSummary(nested map[string]any, fallback string) string {
	message := protocolErrorField(nested, "message")
	code := protocolErrorField(nested, "code")
	if code == "" {
		code = protocolErrorField(nested, "type")
	}
	switch {
	case message != "" && code != "":
		return code + ": " + message
	case message != "":
		return message
	case code != "":
		return fallback + " (" + code + ")"
	default:
		return fallback
	}
}

// retryAfterDuration 解析上游 Retry-After 的秒数或 HTTP 日期。
func retryAfterDuration(err error, now time.Time) time.Duration {
	var upstreamErr *proxyUpstreamError
	if !errors.As(err, &upstreamErr) {
		return 0
	}
	value := strings.TrimSpace(upstreamErr.headers.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, parseErr := time.ParseDuration(value + "s"); parseErr == nil && seconds > 0 {
		return seconds
	}
	if deadline, parseErr := http.ParseTime(value); parseErr == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}

// forwardAPI 识别协议入口、执行本地路由并把请求原样转发到选中的上游渠道。
func (s *Service) forwardAPI(writer http.ResponseWriter, request *http.Request) {
	s.migrationBarrierMu.RLock()
	defer s.migrationBarrierMu.RUnlock()
	settings := s.snapshotSettings()
	requestSettings := proxyRequestSettings{
		RequestPolicy:   settings.RequestPolicy,
		ChannelSettings: settings.ChannelSettings,
		Logging:         settings.Logging,
		DataDirectory:   settings.DataDirectory,
	}
	requestPolicy := requestSettings.RequestPolicy
	protocol, endpoint, ok := proxyProtocol(request.URL.Path)
	if !ok {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "不支持的代理入口"})
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "代理入口仅支持 POST"})
		return
	}
	if timeout := requestPolicy.TotalTimeoutMs; timeout > 0 {
		ctx, cancel := context.WithTimeout(request.Context(), time.Duration(timeout)*time.Millisecond)
		defer cancel()
		request = request.WithContext(ctx)
	}
	originalBody, fields, err := readProxyBody(writer, request)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	model, err := requiredStringField(fields, "model")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "请求 model 必须是非空字符串"})
		return
	}
	stream, err := optionalBoolField(fields, "stream")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "请求 stream 必须是布尔值"})
		return
	}
	capabilities := proxyCapabilities(fields)
	routeInput := storage.RouteSimulationInput{Protocol: protocol, ClientModel: model, Capabilities: capabilities}
	var affinityChannelID string
	if protocol == storage.ProtocolOpenAIResponses {
		if rawPrevious, exists := fields["previous_response_id"]; exists {
			if bytes.Equal(bytes.TrimSpace(rawPrevious), []byte("null")) || json.Unmarshal(rawPrevious, &affinityChannelID) != nil {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "previous_response_id 必须是字符串"})
				return
			}
			affinityChannelID = strings.TrimSpace(affinityChannelID)
			if affinityChannelID == "" {
				writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "previous_response_id 不能为空"})
				return
			}
			responseID := affinityChannelID
			affinityChannelID, err = storage.GetResponseAffinity(s.database, responseID, time.Now())
			if errors.Is(err, storage.ErrNotFound) {
				writeJSON(writer, http.StatusUnprocessableEntity, map[string]string{"error": "未找到 previous_response_id 对应的亲和渠道"})
				return
			}
			if err != nil {
				s.logger.Error("读取 Responses 亲和关系失败", "responseID", responseID, "error", err)
				writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "读取 Responses 亲和关系失败"})
				return
			}
		}
	}
	var targets []storage.RouteTarget
	if affinityChannelID != "" {
		targets, err = storage.ResolveRouteAffinity(s.database, routeInput, affinityChannelID)
	} else {
		targets, err = storage.ResolveRoute(s.database, routeInput)
	}
	if err != nil {
		if errors.Is(err, storage.ErrRouteStorage) {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "读取路由配置失败", "code": "route_storage_error"})
		} else if affinityChannelID != "" && errors.Is(err, storage.ErrAffinityChannelUnavailable) {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "亲和渠道当前不可用", "code": "affinity_channel_unavailable"})
		} else {
			writeJSON(writer, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		}
		return
	}
	if len(targets) == 0 {
		if affinityChannelID != "" {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "亲和渠道当前不可用", "code": "affinity_channel_unavailable"})
		} else {
			writeJSON(writer, http.StatusUnprocessableEntity, map[string]string{"error": "没有可用路由候选"})
		}
		return
	}
	requestID, requestIDErr := storage.NewRequestID()
	if requestIDErr != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": requestIDErr.Error()})
		return
	}
	startedAt := time.Now()
	logicalModel := targets[0].LogicalModel
	if createErr := storage.CreateRequest(s.database, storage.RequestRecord{ID: requestID, Protocol: protocol, ClientModel: model, LogicalModel: logicalModel, FinalStatus: "processing", StartedAt: startedAt}); createErr != nil {
		s.logger.Error("写入请求开始账本失败，已阻止代理请求", "requestID", requestID, "error", createErr)
		s.emitRuntime("error", "database.request_create_failed", "请求开始账本写入失败", map[string]any{"requestID": requestID, "error": createErr.Error()})
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "请求日志初始化失败"})
		return
	}
	writer.Header().Set("X-Request-ID", requestID)
	recordRouteEvent := func(eventType, channelID, relatedChannelID, reason string, details any) error {
		_, eventErr := storage.RecordRequestRouteEvent(s.database, storage.RouteEvent{RequestID: requestID, EventType: eventType, ChannelID: channelID, RelatedChannelID: relatedChannelID, Reason: reason}, details)
		if eventErr != nil {
			return fmt.Errorf("%w: %v", errLedgerWrite, eventErr)
		}
		return nil
	}
	for _, target := range targets {
		if !target.InvalidCustomHeaders {
			continue
		}
		if eventErr := recordRouteEvent("custom_headers_ignored", target.ChannelID, "", "invalid_custom_headers", map[string]any{
			"channelName": target.ChannelName,
			"message":     "自定义请求头配置无效，已忽略，仍使用协议默认请求头",
		}); eventErr != nil {
			s.logger.Error("写入自定义请求头提示失败", "requestID", requestID, "channelID", target.ChannelID, "error", eventErr)
			s.emitRuntime("error", "database.route_event_failed", "自定义请求头提示写入失败", map[string]any{"requestID": requestID, "channelID": target.ChannelID, "error": eventErr.Error()})
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "请求日志初始化失败"})
			return
		}
	}
	attachRequestID := func(meta *proxyResponseMeta) {
		if meta.ResponseHeaders == nil {
			meta.ResponseHeaders = make(http.Header)
		}
		meta.ResponseHeaders.Set("X-Request-ID", requestID)
	}
	sequence := 0
	requestFallback := false
	lastAttemptID := ""
	lastAttemptChannelID := ""
	lastAttemptChannelName := ""
	attachLastAttemptID := func(details map[string]any) map[string]any {
		if lastAttemptID != "" {
			details["attemptID"] = lastAttemptID
		}
		return details
	}
	var lastErr error
	var lastLocalErr error
	rememberLocalErr := func(err error) {
		if err != nil && lastLocalErr == nil {
			lastLocalErr = err
		}
	}
	cleanupUnsentAttempt := func(attemptID string) error {
		return retryLedger(func() error {
			if err := storage.DeleteAttemptContent(s.database, s.secrets, requestID, attemptID); err != nil {
				return err
			}
			return storage.DeleteAttempt(s.database, requestID, attemptID)
		})
	}
	attachFinalAttempt := func(meta *proxyResponseMeta) {
		if meta == nil || lastAttemptChannelID == "" {
			return
		}
		if meta.FinalChannelID == "" {
			meta.FinalChannelID = lastAttemptChannelID
		}
		if meta.FinalChannelName == "" {
			meta.FinalChannelName = lastAttemptChannelName
		}
	}
	finishRequest := func(status string, meta proxyResponseMeta) error {
		attachFinalAttempt(&meta)
		contentErr := s.persistFinalResponseContent(requestID, meta.ResponseHeaders, meta.Content, meta.ContentOriginalSize, requestSettings)
		if contentErr != nil {
			status = "error"
			meta.ErrorClass = "ledger_error"
			meta.ErrorMessage = "最终响应快照保存失败"
			meta.HTTPStatus = http.StatusInternalServerError
		}
		finishMeta := buildRequestFinishMeta(meta, startedAt, sequence, requestFallback)
		finishErr := retryLedger(func() error {
			return storage.FinishRequestWithMeta(s.database, requestID, status, time.Now(), finishMeta)
		})
		if contentErr != nil || finishErr != nil {
			return errors.Join(contentErr, finishErr)
		}
		return nil
	}
	// finishLedgerOnly 在响应已经提交后兜底写入终态，避免账本永久停留在 processing。
	finishLedgerOnly := func(status string, meta proxyResponseMeta) error {
		attachFinalAttempt(&meta)
		finishMeta := buildRequestFinishMeta(meta, startedAt, sequence, requestFallback)
		return retryLedger(func() error {
			return storage.FinishRequestWithMeta(s.database, requestID, status, time.Now(), finishMeta)
		})
	}
	// finishCommittedRequest 只补写已提交流式响应的快照和终态，不再改写客户端响应结果。
	finishCommittedRequest := func(status string, meta proxyResponseMeta) error {
		attachRequestID(&meta)
		contentErr := s.persistFinalResponseContent(requestID, meta.ResponseHeaders, meta.Content, meta.ContentOriginalSize, requestSettings)
		if contentErr != nil && meta.ErrorClass == "" {
			meta.ErrorClass = "ledger_error"
			meta.ErrorMessage = "最终响应快照保存失败"
		}
		finishErr := finishLedgerOnly(status, meta)
		if finishErr != nil {
			meta.ErrorClass = "ledger_error"
			meta.ErrorMessage = "请求终态写入失败"
			if fallbackErr := finishLedgerOnly(status, meta); fallbackErr != nil {
				finishErr = errors.Join(finishErr, fallbackErr)
			}
		}
		if contentErr != nil || finishErr != nil {
			return errors.Join(contentErr, finishErr)
		}
		return nil
	}
	finishAndWriteResponse := func(status string, meta proxyResponseMeta) bool {
		attachFinalAttempt(&meta)
		attachRequestID(&meta)
		if finishErr := finishRequest(status, meta); finishErr == nil {
			writeProxyResponse(writer, meta)
			return true
		} else {
			s.logger.Error("写入请求完成账本失败", "requestID", requestID, "error", finishErr)
			s.emitRuntime("error", "request.ledger_failed", "请求完成账本写入失败", attachLastAttemptID(map[string]any{"requestID": requestID, "error": finishErr.Error()}))
		}
		failureMeta := ledgerFailureMeta("请求日志持久化失败")
		attachFinalAttempt(&failureMeta)
		attachRequestID(&failureMeta)
		if replaceErr := storage.DeleteFinalResponseContent(s.database, s.secrets, requestID); replaceErr != nil {
			s.logger.Error("删除未提交的最终响应快照失败", "requestID", requestID, "error", replaceErr)
		} else if replaceErr := s.persistFinalResponseContent(requestID, failureMeta.ResponseHeaders, failureMeta.Content, failureMeta.ContentOriginalSize, requestSettings); replaceErr != nil {
			s.logger.Error("保存账本失败响应快照失败", "requestID", requestID, "error", replaceErr)
		}
		fallback := requestFallback
		if stateErr := retryLedger(func() error {
			return storage.FinishRequestWithMeta(s.database, requestID, "error", time.Now(), storage.RequestFinishMeta{FinalHTTPStatus: nullableHTTPStatus(failureMeta.HTTPStatus), FinalChannelID: failureMeta.FinalChannelID, FinalChannelName: failureMeta.FinalChannelName, AttemptCount: sequence, FallbackTriggered: &fallback, ErrorClass: failureMeta.ErrorClass, ErrorMessage: failureMeta.ErrorMessage, Latency: time.Since(startedAt)})
		}); stateErr != nil {
			s.logger.Error("写入账本失败终态失败", "requestID", requestID, "error", stateErr)
		}
		writeProxyResponse(writer, failureMeta)
		return false
	}
	s.emitRuntime("info", "request.started", "代理请求开始", map[string]any{"requestID": requestID, "model": model, "protocol": protocol})
	if contentErr := s.persistClientRequestContent(requestID, request, originalBody, requestSettings); contentErr != nil {
		s.logger.Error("保存客户端请求快照失败，已阻止代理请求", "requestID", requestID, "error", contentErr)
		finishAndWriteResponse("error", ledgerFailureMeta("客户端请求快照保存失败"))
		return
	}
	maxChannelAttempts := requestPolicy.MaxChannelAttempts
	attemptsUsed := 0
	budgetExhausted := false
	allSkippedBusy := true
	lastFailedChannelID := ""
	lastFailedChannelName := ""
	lastFailedAttemptID := ""
	lastFailureClass := ""
	for targetIndex := 0; targetIndex < len(targets); targetIndex++ {
		if maxChannelAttempts > 0 && attemptsUsed >= maxChannelAttempts {
			budgetExhausted = true
			break
		}
		if lastFailedChannelID != "" {
			if eventErr := recordRouteEvent("channel_switched", lastFailedChannelID, targets[targetIndex].ChannelID, lastFailureClass, map[string]any{"attemptID": lastFailedAttemptID, "fromChannelName": lastFailedChannelName, "toChannelName": targets[targetIndex].ChannelName, "errorClass": lastFailureClass}); eventErr != nil {
				lastErr = eventErr
				break
			}
			s.emitRuntime("warn", "routing.channel_switched", "请求切换到下一个路由候选", map[string]any{"requestID": requestID, "attemptID": lastFailedAttemptID, "fromChannelID": lastFailedChannelID, "toChannelID": targets[targetIndex].ChannelID, "errorClass": lastFailureClass})
		}
		s.dataMu.RLock()
		latestTarget, refreshErr := storage.ResolveRouteTarget(s.database, routeInput, targets[targetIndex].ChannelID)
		if refreshErr != nil {
			s.dataMu.RUnlock()
			if !errors.Is(refreshErr, storage.ErrNotFound) {
				allSkippedBusy = false
				rememberLocalErr(fmt.Errorf("刷新渠道 %s 配置失败: %w", targets[targetIndex].ChannelID, refreshErr))
				s.emitRuntime("error", "database.route_refresh_failed", "路由渠道配置刷新失败", attachLastAttemptID(map[string]any{"requestID": requestID, "channelID": targets[targetIndex].ChannelID, "error": refreshErr.Error()}))
				break
			}
			allSkippedBusy = false
			skipDetails := map[string]any{"channelName": targets[targetIndex].ChannelName, "reason": "configuration_changed"}
			if lastFailedAttemptID != "" {
				skipDetails["attemptID"] = lastFailedAttemptID
			}
			if eventErr := recordRouteEvent("candidate_skipped", targets[targetIndex].ChannelID, "", "configuration_changed", skipDetails); eventErr != nil {
				lastErr = eventErr
				break
			}
			continue
		}
		targets[targetIndex] = latestTarget
		lease, skip, acquireErr := s.tryAcquireChannel(targets[targetIndex], storage.AdmissionBusiness, time.Now())
		s.dataMu.RUnlock()
		if lease != nil {
			defer lease.releaseUnfinishedLease()
		}
		if acquireErr != nil {
			allSkippedBusy = false
			rememberLocalErr(fmt.Errorf("渠道 %s 准入失败: %w", targets[targetIndex].ChannelID, acquireErr))
			s.emitRuntime("error", "database.route_admission_failed", "路由渠道准入失败", attachLastAttemptID(map[string]any{"requestID": requestID, "channelID": targets[targetIndex].ChannelID, "error": acquireErr.Error()}))
			break
		}
		if skip != nil {
			if skip.Reason != "concurrency_full" {
				allSkippedBusy = false
			}
			skipDetails := map[string]any{"channelName": targets[targetIndex].ChannelName, "model": targets[targetIndex].UpstreamModel, "inFlight": skip.InFlight, "concurrencyLimit": skip.Limit}
			if lastFailedAttemptID != "" {
				skipDetails["attemptID"] = lastFailedAttemptID
			}
			if eventErr := recordRouteEvent("candidate_skipped", targets[targetIndex].ChannelID, "", skip.Reason, skipDetails); eventErr != nil {
				lastErr = eventErr
				break
			}
			runtimeDetails := map[string]any{"requestID": requestID, "channelID": targets[targetIndex].ChannelID, "model": targets[targetIndex].UpstreamModel, "reason": skip.Reason, "inFlight": skip.InFlight, "concurrencyLimit": skip.Limit}
			if lastFailedAttemptID != "" {
				runtimeDetails["attemptID"] = lastFailedAttemptID
			}
			s.emitRuntime("warn", "routing.channel_skipped", "路由候选被非阻塞跳过", runtimeDetails)
			if affinityChannelID != "" {
				if skip.Reason == "concurrency_full" {
					lastErr = newRoutingError(http.StatusServiceUnavailable, "affinity_channel_busy", "亲和渠道当前并发已满")
				} else {
					lastErr = newRoutingError(http.StatusServiceUnavailable, "affinity_channel_unavailable", "亲和渠道当前不可用")
				}
				break
			}
			continue
		}
		targets[targetIndex] = lease.target
		forwardBody, bodyErr := marshalProxyBody(fields, targets[targetIndex].UpstreamModel, targets[targetIndex], requestSettings.ChannelSettings)
		if bodyErr != nil {
			_, _ = lease.Finish(storage.HealthNotSent, "", 0, time.Now())
			rememberLocalErr(bodyErr)
			break
		}
		allSkippedBusy = false
		attemptSequence := sequence + 1
		channelFallback := attemptsUsed > 0
		attemptStarted := time.Now()
		attemptID, attemptIDErr := storage.NewAttemptID()
		if attemptIDErr != nil {
			_, _ = lease.Finish(storage.HealthNotSent, "", 0, time.Now())
			rememberLocalErr(fmt.Errorf("生成 Attempt ID 失败: %w", attemptIDErr))
			break
		}
		initialAttempt := storage.AttemptRecord{ID: attemptID, RequestID: requestID, ChannelID: targets[targetIndex].ChannelID, ChannelName: targets[targetIndex].ChannelName, GroupName: targets[targetIndex].GroupName, Protocol: protocol, ClientModel: model, UpstreamModel: targets[targetIndex].UpstreamModel, LogicalModel: targets[targetIndex].LogicalModel, Sequence: attemptSequence, Status: "processing", StartedAt: attemptStarted}
		if recordErr := retryLedger(func() error { return storage.RecordAttempt(s.database, initialAttempt) }); recordErr != nil {
			_, _ = lease.Finish(storage.HealthNotSent, "", 0, time.Now())
			if cleanupErr := cleanupUnsentAttempt(attemptID); cleanupErr != nil {
				s.logger.Error("清理 Attempt 初始化失败遗留账本失败", "requestID", requestID, "attemptID", attemptID, "error", cleanupErr)
			}
			lastErr = recordErr
			s.logger.Error("写入 Attempt 开始账本失败，已阻止上游请求", "requestID", requestID, "attemptID", attemptID, "error", recordErr)
			finishAndWriteResponse("error", ledgerFailureMeta("Attempt 日志初始化失败"))
			return
		}
		onSent := func() {
			attemptsUsed++
			sequence = attemptSequence
			lastAttemptID = attemptID
			lastAttemptChannelID = targets[targetIndex].ChannelID
			lastAttemptChannelName = targets[targetIndex].ChannelName
			if channelFallback {
				requestFallback = true
			}
			s.emitRuntime("info", "attempt.started", "上游 Attempt 开始", map[string]any{"requestID": requestID, "attemptID": attemptID, "channelID": targets[targetIndex].ChannelID, "model": targets[targetIndex].UpstreamModel, "protocol": protocol, "status": "processing"})
		}
		var meta proxyResponseMeta
		lease.SetSentHook(onSent)
		meta, attemptErr := s.executeProxyRequest(writer, request, requestID, attemptID, protocol, endpoint, forwardBody, targets[targetIndex], stream, lease, requestSettings)
		wasSent := lease.WasSent()
		if wasSent {
			meta.FinalChannelID, meta.FinalChannelName = targets[targetIndex].ChannelID, targets[targetIndex].ChannelName
		}
		streamCompletedWithLedgerError := stream && errors.Is(attemptErr, errLedgerWrite) && meta.StreamEnded != nil && *meta.StreamEnded
		responseCommitted := stream && (attemptErr == nil || streamCompletedWithLedgerError || errors.Is(attemptErr, errResponseCommitted))
		attemptStatus := "success"
		errorClass := ""
		errorMessage := ""
		if attemptErr != nil {
			if !streamCompletedWithLedgerError {
				attemptStatus = "failed"
				if errors.Is(attemptErr, errResponseCommitted) {
					attemptStatus = "stream_interrupted"
				} else if errors.Is(attemptErr, errFirstByteTimeout) || errors.Is(attemptErr, errStreamIdleTimeout) {
					attemptStatus = "timeout"
				} else if request.Context().Err() != nil {
					attemptStatus = "cancelled"
					if errors.Is(request.Context().Err(), context.DeadlineExceeded) {
						attemptStatus = "timeout"
					}
				}
				if wasSent {
					lastErr = attemptErr
				} else {
					rememberLocalErr(attemptErr)
				}
			}
			errorClass = classifyProxyError(attemptErr, request.Context().Err())
			errorMessage = attemptErr.Error()
			meta.ErrorClass, meta.ErrorMessage = errorClass, errorMessage
			event := "attempt.failed"
			level := "warn"
			if attemptStatus == "timeout" {
				event, level = "attempt.timeout", "error"
			} else if attemptStatus == "cancelled" {
				event, level = "attempt.cancelled", "warn"
			}
			if wasSent {
				s.emitRuntime(level, event, "上游 Attempt 未成功完成", map[string]any{"requestID": requestID, "attemptID": attemptID, "channelID": targets[targetIndex].ChannelID, "errorClass": errorClass, "error": errorMessage, "status": attemptStatus})
			}
		}
		if meta.CompletedAt == nil {
			completed := time.Now().UTC()
			meta.CompletedAt = &completed
		}
		retryReason := ""
		if channelFallback {
			retryReason = "channel_switch"
		}
		updateAttempt := storage.AttemptRecord{ID: attemptID, RequestID: requestID, ChannelID: targets[targetIndex].ChannelID, GroupName: targets[targetIndex].GroupName, Sequence: attemptSequence, Status: attemptStatus, ErrorClass: errorClass, ErrorMessage: errorMessage, RetryReason: retryReason, FallbackTriggered: channelFallback, FirstByteAt: meta.FirstByteAt, FirstEventAt: meta.FirstEventAt, CompletedAt: meta.CompletedAt, HTTPStatus: nullableHTTPStatus(meta.HTTPStatus), Latency: time.Since(attemptStarted), InputTokens: proxyMetaTokens(meta).input, OutputTokens: proxyMetaTokens(meta).output, CacheReadInputTokens: proxyMetaTokens(meta).cache, TotalTokens: totalProxyTokens(meta), ReasoningTokens: reasoningProxyTokens(meta), CacheWriteInputTokens: cacheWriteProxyTokens(meta), RequestContentBlobID: meta.UpstreamRequestContentBlobID, ResponseContentBlobID: meta.UpstreamResponseContentBlobID, StreamEnded: meta.StreamEnded}
		healthOutcome := storage.HealthNeutralFailure
		if !wasSent {
			healthOutcome = storage.HealthNotSent
		} else if attemptErr == nil || streamCompletedWithLedgerError {
			healthOutcome = storage.HealthSuccess
		} else if isTotalRequestBudgetTimeout(attemptErr, request.Context().Err()) {
			healthOutcome = storage.HealthNeutralFailure
		} else if shouldCountHealthFailure(attemptErr) {
			healthOutcome = storage.HealthFailure
		}
		settledAt := time.Now()
		healthResult, settlementErr := lease.FinishAttempt(updateAttempt, targets[targetIndex].ChannelName, healthOutcome, errorClass, retryAfterDuration(attemptErr, settledAt), settledAt)
		if settlementErr != nil {
			s.logger.Error("结算 Attempt 与渠道健康账本失败", "requestID", requestID, "attemptID", attemptID, "error", settlementErr)
			ledgerEvent := "attempt.ledger_failed"
			ledgerDetails := map[string]any{"requestID": requestID, "error": settlementErr.Error()}
			if wasSent {
				ledgerDetails["attemptID"] = attemptID
			} else {
				ledgerEvent = "routing.ledger_failed"
				ledgerDetails["channelID"] = targets[targetIndex].ChannelID
			}
			s.emitRuntime("error", ledgerEvent, "Attempt 与渠道健康账本写入失败", ledgerDetails)
			if responseCommitted {
				committedStatus := "partial"
				if attemptErr == nil || streamCompletedWithLedgerError {
					committedStatus = "success"
				}
				meta.ErrorClass = "ledger_error"
				meta.ErrorMessage = "Attempt 与渠道健康账本写入失败"
				if finishErr := finishCommittedRequest(committedStatus, meta); finishErr != nil {
					s.logger.Error("写入流式请求终态失败", "requestID", requestID, "error", finishErr)
				}
			} else if !errors.Is(attemptErr, errResponseCommitted) {
				if !wasSent {
					if cleanupErr := cleanupUnsentAttempt(attemptID); cleanupErr != nil {
						s.logger.Error("清理未发送 Attempt 账本失败", "requestID", requestID, "attemptID", attemptID, "error", cleanupErr)
					}
				}
				failureMeta := ledgerFailureMeta("请求日志持久化失败")
				attachFinalAttempt(&failureMeta)
				finishAndWriteResponse("error", failureMeta)
			}
			return
		}
		if healthResult.Applied {
			healthDetails := map[string]any{"requestID": requestID, "channelID": targets[targetIndex].ChannelID, "fromState": healthResult.FromState, "toState": healthResult.ToState, "failureCountBefore": healthResult.FailureCountBefore, "failureCountAfter": healthResult.FailureCountAfter, "probeFailureCountBefore": healthResult.ProbeFailureCountBefore, "probeFailureCountAfter": healthResult.ProbeFailureCountAfter, "probeSuccessCountBefore": healthResult.ProbeSuccessCountBefore, "probeSuccessCountAfter": healthResult.ProbeSuccessCountAfter, "healthVersion": healthResult.HealthVersion, "cooldownUntil": healthResult.CooldownUntil}
			if wasSent {
				healthDetails["attemptID"] = attemptID
			}
			s.emitRuntime("info", "health.changed", "渠道健康状态已结算", healthDetails)
		}
		if !wasSent {
			cleanupErr := cleanupUnsentAttempt(attemptID)
			if cleanupErr != nil {
				s.logger.Error("清理未发送 Attempt 账本失败，已停止请求", "requestID", requestID, "attemptID", attemptID, "error", cleanupErr)
				finishAndWriteResponse("error", ledgerFailureMeta("未发送 Attempt 清理失败"))
				return
			}
		}
		if attemptErr == nil || streamCompletedWithLedgerError {
			if stream {
				if finishErr := finishCommittedRequest("success", meta); finishErr != nil {
					s.logger.Error("写入请求完成账本失败", "requestID", requestID, "error", finishErr)
					s.emitRuntime("error", "request.ledger_failed", "请求完成账本写入失败", attachLastAttemptID(map[string]any{"requestID": requestID, "error": finishErr.Error()}))
					return
				}
			} else if !finishAndWriteResponse("success", meta) {
				return
			}
			s.emitRuntime("info", "request.completed", "代理请求成功", map[string]any{"requestID": requestID, "attemptID": attemptID, "channelID": targets[targetIndex].ChannelID, "model": targets[targetIndex].UpstreamModel, "protocol": protocol, "status": "success"})
			if protocol == storage.ProtocolOpenAIResponses && meta.ResponseID != "" {
				if saveErr := storage.SaveResponseAffinity(s.database, meta.ResponseID, targets[targetIndex].ChannelID, time.Now().Add(responseAffinityTTL)); saveErr != nil {
					s.logger.Warn("保存 Responses 亲和关系失败", "channel", targets[targetIndex].ChannelID, "error", saveErr)
					s.emitRuntime("error", "database.response_affinity_save_failed", "保存 Responses 亲和关系失败", attachLastAttemptID(map[string]any{"requestID": requestID, "channelID": targets[targetIndex].ChannelID, "error": saveErr.Error()}))
				}
			}
			return
		}
		if errors.Is(attemptErr, errResponseCommitted) {
			if finishErr := finishCommittedRequest("partial", meta); finishErr != nil {
				s.logger.Error("写入部分请求终态失败", "requestID", requestID, "error", finishErr)
			}
			return
		}
		if request.Context().Err() != nil {
			status := "cancelled"
			if errors.Is(request.Context().Err(), context.DeadlineExceeded) {
				status = "timeout"
			}
			event := "request.cancelled"
			level := "warn"
			if status == "timeout" {
				event, level = "request.timeout", "error"
			}
			requestDetails := map[string]any{"requestID": requestID, "channelID": targets[targetIndex].ChannelID, "status": status}
			if wasSent {
				requestDetails["attemptID"] = attemptID
			}
			s.emitRuntime(level, event, "客户端请求未能继续执行", requestDetails)
			failureMeta := proxyFailureMeta(attemptErr, request.Context().Err())
			attachFinalAttempt(&failureMeta)
			finishAndWriteResponse(status, failureMeta)
			return
		}
		if affinityChannelID != "" {
			break
		}
		if !wasSent {
			rememberLocalErr(attemptErr)
			break
		}
		if !shouldFallback(attemptErr) {
			break
		}
		if wasSent {
			lastFailedChannelID = targets[targetIndex].ChannelID
			lastFailedChannelName = targets[targetIndex].ChannelName
			lastFailedAttemptID = attemptID
			lastFailureClass = errorClass
		}
	}
	exhaustedReason := "candidate_exhausted"
	if affinityChannelID != "" && lastErr != nil {
		exhaustedReason = classifyProxyError(lastErr, request.Context().Err())
	} else if attemptsUsed == 0 && lastErr == nil && allSkippedBusy {
		lastErr = newRoutingError(http.StatusServiceUnavailable, "all_channels_busy", "所有合格渠道当前并发已满")
		writer.Header().Set("Retry-After", "1")
		exhaustedReason = "all_channels_busy"
	} else if budgetExhausted {
		lastErr = newRoutingError(http.StatusServiceUnavailable, "attempt_budget_exhausted", "已达到最大渠道尝试数")
		exhaustedReason = "attempt_budget_exhausted"
	} else if attemptsUsed == 0 && lastErr == nil && lastLocalErr != nil {
		lastErr = lastLocalErr
		exhaustedReason = classifyProxyError(lastErr, request.Context().Err())
	} else if attemptsUsed == 0 && lastErr == nil {
		lastErr = newRoutingError(http.StatusServiceUnavailable, "all_channels_unavailable", "所有合格渠道当前均不可用")
		exhaustedReason = "all_channels_unavailable"
	}
	exhaustedDetails := map[string]any{"attemptsUsed": attemptsUsed, "maxChannelAttempts": maxChannelAttempts, "errorClass": classifyProxyError(lastErr, request.Context().Err())}
	if lastAttemptID != "" {
		exhaustedDetails["attemptID"] = lastAttemptID
	}
	exhaustedChannelID := lastFailedChannelID
	if exhaustedChannelID == "" {
		exhaustedChannelID = lastAttemptChannelID
	}
	if eventErr := recordRouteEvent("routing_exhausted", exhaustedChannelID, "", exhaustedReason, exhaustedDetails); eventErr != nil {
		lastErr = eventErr
	}
	exhaustedRuntimeDetails := map[string]any{"requestID": requestID, "channelID": exhaustedChannelID, "reason": exhaustedReason, "attemptsUsed": attemptsUsed, "maxChannelAttempts": maxChannelAttempts, "errorClass": classifyProxyError(lastErr, request.Context().Err())}
	if lastAttemptID != "" {
		exhaustedRuntimeDetails["attemptID"] = lastAttemptID
	}
	s.emitRuntime("warn", "routing.exhausted", "请求路由候选已耗尽", exhaustedRuntimeDetails)
	finalMeta := proxyFailureMeta(lastErr, request.Context().Err())
	finalMeta.FinalChannelID = lastAttemptChannelID
	finalMeta.FinalChannelName = lastAttemptChannelName
	finalStatus := finalRequestStatus(lastErr)
	if finalStatus == "timeout" || finalStatus == "cancelled" {
		event := "request.cancelled"
		level := "warn"
		if finalStatus == "timeout" {
			event, level = "request.timeout", "error"
		}
		s.emitRuntime(level, event, "代理请求以非成功状态结束", attachLastAttemptID(map[string]any{"requestID": requestID, "status": finalStatus, "error": finalMeta.ErrorClass}))
	}
	if !finishAndWriteResponse(finalStatus, finalMeta) {
		return
	}
	completedDetails := map[string]any{"requestID": requestID, "model": model, "protocol": protocol, "status": "error", "error": finalMeta.ErrorClass}
	if lastAttemptID != "" {
		completedDetails["attemptID"] = lastAttemptID
	}
	s.emitRuntime("warn", "request.completed", "代理请求失败", completedDetails)
}

// marshalProxyBody 为每个目标渠道替换模型、思考等级并应用 service_tier 透传策略。
func marshalProxyBody(fields map[string]json.RawMessage, upstreamModel string, target storage.RouteTarget, globalSettings config.ChannelSettings) ([]byte, error) {
	cloned := make(map[string]json.RawMessage, len(fields)+1)
	for key, value := range fields {
		cloned[key] = value
	}
	modelValue, err := json.Marshal(upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("请求模型无法序列化: %w", err)
	}
	cloned["model"] = modelValue
	if !(globalSettings.ServiceTierPassthrough && target.ServiceTierPassthrough) {
		serviceTier := `"default"`
		if target.Protocol == storage.ProtocolAnthropicMessages {
			serviceTier = `"auto"`
		}
		cloned["service_tier"] = json.RawMessage(serviceTier)
	}
	effort := target.ReasoningEffort
	if effort == "" || effort == config.ReasoningEffortPassthrough {
		effort = globalSettings.ReasoningEffort
	}
	if effort != "" && effort != config.ReasoningEffortPassthrough {
		if err := replaceReasoningEffort(cloned, target.Protocol, effort); err != nil {
			return nil, err
		}
	}
	body, err := json.Marshal(cloned)
	if err != nil {
		return nil, fmt.Errorf("请求 JSON 无法转发: %w", err)
	}
	return body, nil
}

// replaceReasoningEffort 替换 OpenAI 请求中的思考等级字段并保留 reasoning 其他属性。
func replaceReasoningEffort(fields map[string]json.RawMessage, protocol storage.Protocol, effort string) error {
	effortValue, err := json.Marshal(effort)
	if err != nil {
		return fmt.Errorf("思考等级无法序列化: %w", err)
	}
	switch protocol {
	case storage.ProtocolOpenAIResponses:
		if raw, exists := fields["reasoning"]; exists {
			reasoning := make(map[string]json.RawMessage)
			if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				if err := json.Unmarshal(raw, &reasoning); err != nil || reasoning == nil {
					return errors.New("请求 reasoning 必须是对象")
				}
			}
			reasoning["effort"] = effortValue
			encoded, err := json.Marshal(reasoning)
			if err != nil {
				return fmt.Errorf("请求 reasoning 无法序列化: %w", err)
			}
			fields["reasoning"] = encoded
			return nil
		}
		reasoning, err := json.Marshal(map[string]json.RawMessage{"effort": effortValue})
		if err != nil {
			return fmt.Errorf("请求 reasoning 无法序列化: %w", err)
		}
		fields["reasoning"] = reasoning
	case storage.ProtocolOpenAIChat:
		fields["reasoning_effort"] = effortValue
	}
	return nil
}

// applyCredential 从平台秘密存储读取凭证，并只把计算后的认证 Header 注入上游请求。
func (s *Service) applyCredential(request *http.Request, secretRef string) error {
	credential, err := s.loadStoredCredential(secretRef)
	if err != nil {
		return err
	}
	return applyCredentialValue(request, credential)
}

// loadStoredCredential 读取并校验渠道凭证，供 lease 固定本次请求使用。
func (s *Service) loadStoredCredential(secretRef string) (storedCredential, error) {
	if strings.TrimSpace(secretRef) == "" {
		return storedCredential{}, errors.New("渠道未配置凭证")
	}
	value, err := s.secrets.Get(secretRef)
	if err != nil {
		return storedCredential{}, fmt.Errorf("读取渠道凭证失败: %w", err)
	}
	var credential storedCredential
	if err := decodeStrictBytes(value, &credential); err != nil {
		return storedCredential{}, errors.New("渠道凭证存储值无效")
	}
	if !isValidHeaderName(credential.HeaderName) || credential.Secret == "" || strings.ContainsAny(credential.HeaderName+credential.Prefix+credential.Secret, "\r\n") {
		return storedCredential{}, errors.New("渠道凭证配置无效")
	}
	return credential, nil
}

// applyCredentialValue 将已校验的凭证值注入请求，不接触持久化秘密存储。
func applyCredentialValue(request *http.Request, credential storedCredential) error {
	if !isValidHeaderName(credential.HeaderName) || credential.Secret == "" || strings.ContainsAny(credential.HeaderName+credential.Prefix+credential.Secret, "\r\n") {
		return errors.New("渠道凭证配置无效")
	}
	request.Header.Set(credential.HeaderName, credential.Prefix+credential.Secret)
	return nil
}

// hasUsableCredential 只检查凭证是否能从秘密存储读取且满足 Header 注入要求。
func (s *Service) hasUsableCredential(secretRef string) bool {
	_, err := s.loadStoredCredential(secretRef)
	return err == nil
}

// forwardJSON 校验非流式响应并缓冲完整内容，正文大小限制由快照持久化策略执行。
func forwardJSON(response *http.Response, protocol storage.Protocol) (proxyResponseMeta, error) {
	body, firstByteAt, err := readResponseBody(response.Body)
	if err != nil {
		return proxyResponseMeta{}, fmt.Errorf("读取上游响应失败: %w", err)
	}
	meta := proxyResponseMeta{FirstByteAt: firstByteAt, Content: append([]byte(nil), body...)}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || !validJSONPayload(payload, protocol) {
		return meta, errors.New("上游返回的 JSON 响应不符合协议")
	}
	if protocol == storage.ProtocolOpenAIResponses {
		if protocolErr := classifyResponsesJSONResult(payload, response, body); protocolErr != nil {
			parsed := parseProtocolResponseMeta(payload, protocol)
			mergeProxyResponseMeta(&meta, parsed)
			meta.ResponseID = parsed.ResponseID
			return meta, protocolErr
		}
	}
	parsed := parseProtocolResponseMeta(payload, protocol)
	mergeProxyResponseMeta(&meta, parsed)
	meta.ResponseID = parsed.ResponseID
	return meta, nil
}

func classifyResponsesJSONResult(payload map[string]json.RawMessage, response *http.Response, body []byte) error {
	status := jsonString(payload["status"])
	headers := http.Header{}
	if response != nil {
		headers = responseHeaderSnapshot(response)
	}
	switch status {
	case "failed":
		nested := decodeJSONObject(payload["error"])
		modelNotFound := protocolErrorIsModelNotFound(nested)
		class := "upstream_protocol"
		code := strings.ToLower(protocolErrorField(nested, "code"))
		typ := strings.ToLower(protocolErrorField(nested, "type"))
		if code == "server_error" || typ == "server_error" {
			class = "http_5xx"
		} else if modelNotFound {
			class = "model_not_found"
		}
		protocolErr := newProxyProtocolError(http.StatusOK, headers, body, class, protocolErrorSummary(nested, "上游 Responses 执行失败"), modelNotFound)
		// server_error 计渠道失败；其他 failed 可切换但不把参数类错误算成渠道故障。
		if class != "http_5xx" {
			protocolErr.skipHealthFailure = true
		}
		return protocolErr
	case "incomplete":
		// 兼容性基线将 incomplete 视为失败，与流式 response.incomplete 一致，不扩大为仅 completed 才转发。
		nested := decodeJSONObject(payload["error"])
		protocolErr := newProxyProtocolError(http.StatusOK, headers, body, "upstream_protocol", protocolErrorSummary(nested, "上游 Responses 返回 incomplete"), false)
		protocolErr.skipHealthFailure = true
		return protocolErr
	case "queued", "in_progress":
		// V1 入口只承诺同步完成结果；异步状态不是业务成功，也不表示渠道故障，因此不切换、不计健康失败。
		protocolErr := newProxyProtocolError(http.StatusOK, headers, body, "upstream_protocol", "上游 Responses 尚未完成", false)
		protocolErr.skipFallback = true
		protocolErr.skipHealthFailure = true
		return protocolErr
	default:
		return nil
	}
}

// decodeJSONObject 解析协议错误对象；无效 JSON 视为没有结构化错误。
func decodeJSONObject(raw json.RawMessage) map[string]any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var nested map[string]any
	if json.Unmarshal(raw, &nested) != nil || nested == nil {
		return nil
	}
	return nested
}

// writeProxyResponse 在账本和快照写入完成后提交非流式成功响应。
func writeProxyResponse(writer http.ResponseWriter, meta proxyResponseMeta) {
	copyResponseHeaders(writer.Header(), meta.ResponseHeaders)
	status := meta.HTTPStatus
	if status == 0 {
		status = http.StatusOK
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(meta.Content)
}

// forwardSSE 缓冲首个协议有效事件，提交后再持续透传后续事件。
func forwardSSE(writer http.ResponseWriter, response *http.Response, protocol storage.Protocol, snapshotLimit int) (proxyResponseMeta, error) {
	reader := bufio.NewReader(response.Body)
	var frame bytes.Buffer
	var precommit bytes.Buffer
	var meta proxyResponseMeta
	upstreamSnap := newBoundedSnapshot(snapshotLimit)
	downstreamSnap := newBoundedSnapshot(snapshotLimit)
	committed := false
	terminated := false
	messageStarted := false
	writeContent := func(content []byte) error {
		written, writeErr := writer.Write(content)
		recorded := written
		if recorded < 0 {
			recorded = 0
		} else if recorded > len(content) {
			recorded = len(content)
		}
		downstreamSnap.append(content[:recorded])
		if writeErr != nil {
			meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
			return errors.Join(errResponseCommitted, errDownstreamStreamWrite, fmt.Errorf("写入客户端 SSE 失败: %w", writeErr))
		}
		if written != len(content) {
			meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
			return errors.Join(errResponseCommitted, errDownstreamStreamWrite, fmt.Errorf("写入客户端 SSE 失败: %w", io.ErrShortWrite))
		}
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	}
	processFrame := func(frameBytes []byte) error {
		valid, terminal, frameMeta, parseErr := inspectSSEFrame(frameBytes, protocol)
		if parseErr != nil {
			if committed {
				return errors.Join(errResponseCommitted, errUpstreamStreamInterrupted, parseErr)
			}
			return parseErr
		}
		if protocol == storage.ProtocolAnthropicMessages && valid && !committed {
			messageStarted = true
		}
		if protocol == storage.ProtocolAnthropicMessages && terminal && !messageStarted {
			if committed {
				return errors.Join(errResponseCommitted, errUpstreamStreamInterrupted, errors.New("上游 Messages 在合法 message_start 前结束"))
			}
			return errors.New("上游 Messages 在合法 message_start 前结束")
		}
		canCommit := valid || terminal && protocol == storage.ProtocolOpenAIResponses
		if canCommit && !committed {
			if timed, ok := response.Body.(*timedResponseBody); ok {
				if !timed.stopFirstByteTimer() {
					return errFirstByteTimeout
				}
				timed.markStreamCommitted()
			}
			copyResponseHeaders(writer.Header(), response.Header)
			writer.WriteHeader(response.StatusCode)
			precommit.Write(frameBytes)
			if writeErr := writeContent(precommit.Bytes()); writeErr != nil {
				return writeErr
			}
			precommit.Reset()
			committed = true
		} else if terminal && !committed {
			return errors.New("上游 SSE 在首个有效事件前结束")
		} else if !committed {
			precommit.Write(frameBytes)
		} else {
			if writeErr := writeContent(frameBytes); writeErr != nil {
				return writeErr
			}
		}
		mergeProxyResponseMeta(&meta, frameMeta)
		frame.Reset()
		if terminal {
			terminated = true
		}
		return nil
	}
	for {
		chunk, err := readSSEChunk(reader)
		if len(chunk) > 0 {
			upstreamSnap.append(chunk)
			if meta.FirstByteAt == nil {
				now := time.Now().UTC()
				meta.FirstByteAt = &now
			}
			frame.Write(chunk)
			if frame.Len() > maxSSEEventBytes {
				eventErr := errors.New("上游 SSE 单事件超过缓冲上限")
				if committed {
					meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
					meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
					return meta, errors.Join(errResponseCommitted, errUpstreamStreamInterrupted, eventErr)
				}
				meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
				return meta, eventErr
			}
			if !committed && precommit.Len()+frame.Len() > maxSSEPrecommitBytes {
				meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
				return meta, errors.New("上游 SSE 首事件超过缓冲上限")
			}
		}
		if isSSEFrameBoundary(chunk) && frame.Len() > 0 {
			if processErr := processFrame(frame.Bytes()); processErr != nil {
				meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
				meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
				return meta, processErr
			}
			if terminated {
				meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
				meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
				return meta, nil
			}
		}
		if err != nil {
			if err == io.EOF {
				if frame.Len() > 0 {
					if processErr := processFrame(frame.Bytes()); processErr != nil {
						meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
						meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
						return meta, processErr
					}
					if terminated {
						meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
						meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
						return meta, nil
					}
					if committed {
						meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
						meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
						return meta, errors.Join(errResponseCommitted, errUpstreamStreamInterrupted)
					}
					meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
					meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
					return meta, errors.New("上游 SSE 在完整事件前结束")
				}
				meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
				meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
				if !committed {
					return meta, errors.New("上游 SSE 在首个有效事件前结束")
				}
				if !terminated {
					return meta, errors.Join(errResponseCommitted, errUpstreamStreamInterrupted)
				}
				return meta, nil
			}
			meta.Content, meta.ContentOriginalSize = downstreamSnap.Bytes, downstreamSnap.OriginalSize
			meta.UpstreamContent, meta.UpstreamOriginalSize = upstreamSnap.Bytes, upstreamSnap.OriginalSize
			readErr := fmt.Errorf("读取上游 SSE 失败: %w", err)
			if committed {
				return meta, errors.Join(errResponseCommitted, errUpstreamStreamInterrupted, readErr)
			}
			return meta, readErr
		}
	}
}

type boundedSnapshot struct {
	Bytes        []byte
	OriginalSize int64
	Limit        int
	Truncated    bool
}

func newBoundedSnapshot(limit int) *boundedSnapshot {
	if limit <= 0 {
		limit = 1 << 20
	}
	return &boundedSnapshot{Limit: limit}
}

func (snap *boundedSnapshot) append(chunk []byte) {
	if snap == nil || len(chunk) == 0 {
		return
	}
	snap.OriginalSize += int64(len(chunk))
	remain := snap.Limit - len(snap.Bytes)
	if remain <= 0 {
		snap.Truncated = true
		return
	}
	if len(chunk) > remain {
		snap.Bytes = append(snap.Bytes, chunk[:remain]...)
		snap.Truncated = true
		return
	}
	snap.Bytes = append(snap.Bytes, chunk...)
}

func readSSEChunk(reader *bufio.Reader) ([]byte, error) {
	var chunk bytes.Buffer
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > 0 {
			if chunk.Len()+len(part) > maxSSEEventBytes+1 {
				chunk.Write(part[:maxSSEEventBytes+1-chunk.Len()])
				return chunk.Bytes(), errors.New("上游 SSE 单事件超过缓冲上限")
			}
			chunk.Write(part)
			if err == nil || errors.Is(err, bufio.ErrBufferFull) && part[len(part)-1] == '\n' {
				return chunk.Bytes(), nil
			}
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return chunk.Bytes(), err
		}
		return chunk.Bytes(), nil
	}
}

func isSSEFrameBoundary(chunk []byte) bool {
	trimmed := bytes.TrimRight(chunk, "\r\n")
	return len(bytes.TrimSpace(trimmed)) == 0 && len(chunk) > 0
}

// validJSONResponse 判断三个协议的最小非流式成功载荷。
func validJSONResponse(body []byte, protocol storage.Protocol) bool {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	return validProbeJSONPayload(payload, protocol)
}

// validJSONPayload 判断已经解析的协议 JSON 是否为成功响应。
func validJSONPayload(payload map[string]json.RawMessage, protocol storage.Protocol) bool {
	var object, kind, id string
	switch protocol {
	case storage.ProtocolOpenAIResponses:
		object = jsonString(payload["object"])
		id = jsonString(payload["id"])
		return object == "response" && id != ""
	case storage.ProtocolAnthropicMessages:
		kind = jsonString(payload["type"])
		id = jsonString(payload["id"])
		return kind == "message" && id != ""
	case storage.ProtocolOpenAIChat:
		var choices []json.RawMessage
		rawChoices, exists := payload["choices"]
		return exists && json.Unmarshal(rawChoices, &choices) == nil
	default:
		return false
	}
}

// validProbeJSONPayload 判断最小推理探针是否返回可证明成功的协议载荷。
func validProbeJSONPayload(payload map[string]json.RawMessage, protocol storage.Protocol) bool {
	if !validJSONPayload(payload, protocol) {
		return false
	}
	switch protocol {
	case storage.ProtocolOpenAIResponses:
		return jsonString(payload["status"]) == "completed"
	case storage.ProtocolOpenAIChat:
		var choices []json.RawMessage
		return json.Unmarshal(payload["choices"], &choices) == nil && len(choices) > 0
	default:
		return true
	}
}

// readProxyBody 读取并解析代理请求 JSON，同时保留所有未知协议字段；入口请求仍受协议解析上限保护。
func readProxyBody(writer http.ResponseWriter, request *http.Request) ([]byte, map[string]json.RawMessage, error) {
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 8<<20))
	if err != nil {
		return nil, nil, errors.New("读取请求体失败")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, nil, errors.New("请求体必须是 JSON 对象")
	}
	return body, fields, nil
}

func requiredStringField(fields map[string]json.RawMessage, name string) (string, error) {
	value, ok := fields[name]
	if !ok {
		return "", errors.New("缺少字段")
	}
	var result string
	if json.Unmarshal(value, &result) != nil || strings.TrimSpace(result) == "" {
		return "", errors.New("字段无效")
	}
	return result, nil
}

func optionalBoolField(fields map[string]json.RawMessage, name string) (bool, error) {
	value, ok := fields[name]
	if !ok {
		return false, nil
	}
	var result bool
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &result) != nil {
		return false, errors.New("字段无效")
	}
	return result, nil
}

func proxyProtocol(path string) (storage.Protocol, string, bool) {
	switch strings.TrimRight(path, "/") {
	case "/v1/responses":
		return storage.ProtocolOpenAIResponses, "/v1/responses", true
	case "/v1/chat/completions":
		return storage.ProtocolOpenAIChat, "/v1/chat/completions", true
	case "/v1/messages":
		return storage.ProtocolAnthropicMessages, "/v1/messages", true
	default:
		return "", "", false
	}
}

func joinUpstreamURL(base, endpoint, query string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("渠道 Base URL 无效")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	endpointPath := endpoint
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(endpointPath, "/v1/") {
		endpointPath = strings.TrimPrefix(endpointPath, "/v1")
	}
	parsed.Path = basePath + endpointPath
	parsed.RawQuery = query
	return parsed.String(), nil
}

func copyRequestHeaders(destination, source http.Header) {
	for name, values := range source {
		if _, skip := sensitiveHeaders[http.CanonicalHeaderKey(name)]; skip || strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") {
			continue
		}
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(name)]; skip {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func copyResponseHeaders(destination, source http.Header) {
	for name, values := range source {
		if _, skip := sensitiveHeaders[http.CanonicalHeaderKey(name)]; skip {
			continue
		}
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(name)]; skip {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

var errResponseCommitted = errors.New("响应已提交")
