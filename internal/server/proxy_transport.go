package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/storage"
)

type proxyRequestSettings struct {
	RequestPolicy   config.RequestPolicy
	ChannelSettings config.ChannelSettings
	Logging         config.LoggingSettings
	DataDirectory   string
}

// executeProxyRequest 构造上游请求、注入渠道凭证并按响应类型转发。
func (s *Service) executeProxyRequest(writer http.ResponseWriter, inbound *http.Request, requestID, attemptID string, protocol storage.Protocol, endpoint string, body []byte, target storage.RouteTarget, stream bool, lease *channelLease, requestSettings proxyRequestSettings) (proxyResponseMeta, error) {
	upstreamURL, err := joinUpstreamURL(target.BaseURL, endpoint, inbound.URL.RawQuery)
	if err != nil {
		return proxyResponseMeta{}, err
	}
	ctx := inbound.Context()
	var requestTimeoutCancel context.CancelFunc
	if target.RequestTimeoutMs > 0 {
		ctx, requestTimeoutCancel = context.WithTimeout(ctx, time.Duration(target.RequestTimeoutMs)*time.Millisecond)
		defer requestTimeoutCancel()
	}
	ctx, bodyCancel := context.WithCancelCause(ctx)
	defer bodyCancel(nil)
	upstreamRequest, err := http.NewRequestWithContext(ctx, inbound.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		bodyCancel(nil)
		return proxyResponseMeta{}, fmt.Errorf("创建上游请求失败: %w", err)
	}
	copyRequestHeaders(upstreamRequest.Header, inbound.Header)
	for name, value := range target.CustomHeaders {
		if strings.TrimSpace(name) != "" && !strings.ContainsAny(name+value, "\r\n") {
			upstreamRequest.Header.Set(name, value)
		}
	}
	if err := applyCredentialValue(upstreamRequest, lease.credential); err != nil {
		bodyCancel(nil)
		return proxyResponseMeta{}, err
	}
	if upstreamRequest.Header.Get("User-Agent") == "" {
		upstreamRequest.Header.Set("User-Agent", "Go-http-client/1.1")
	}
	metaRequestBlobID, contentErr := s.persistAttemptRequestContent(requestID, attemptID, requestHeaderSnapshot(upstreamRequest), body, requestSettings, lease.credential.HeaderName)
	if contentErr != nil {
		bodyCancel(nil)
		return proxyResponseMeta{}, contentErr
	}
	connectTimeout := time.Duration(requestSettings.RequestPolicy.ConnectTimeoutMs) * time.Millisecond
	transport := &http.Transport{
		Proxy:              http.ProxyFromEnvironment,
		DialContext:        (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:  true,
		DisableCompression: true,
	}
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: rejectUpstreamRedirect,
	}
	defer transport.CloseIdleConnections()
	var firstByteTimer *firstByteTimerState
	if timeout := requestSettings.RequestPolicy.FirstByteTimeoutMs; timeout > 0 {
		// 计时从上游请求即将发出时开始，避免凭证读取或快照落盘消耗首字节预算。
		firstByteTimer = newFirstByteTimer(time.Duration(timeout)*time.Millisecond, bodyCancel)
		defer firstByteTimer.stop()
	}
	if err := ctx.Err(); err != nil {
		bodyCancel(nil)
		return proxyResponseMeta{UpstreamRequestContentBlobID: metaRequestBlobID}, err
	}
	// 本地准备完成后、client.Do 前同步标记传输开始；DNS/TCP/TLS 失败也计为 Attempt。
	lease.MarkSent()
	response, err := client.Do(upstreamRequest)
	if err != nil {
		cause := context.Cause(ctx)
		bodyCancel(nil)
		if errors.Is(cause, errFirstByteTimeout) {
			return proxyResponseMeta{}, fmt.Errorf("上游请求失败: %w", errFirstByteTimeout)
		}
		return proxyResponseMeta{}, fmt.Errorf("上游请求失败: %w", err)
	}
	defer response.Body.Close()
	idleTimeoutMs := 0
	if stream {
		idleTimeoutMs = target.StreamIdleTimeoutMs
		if idleTimeoutMs <= 0 {
			idleTimeoutMs = requestSettings.RequestPolicy.StreamIdleTimeoutMs
		}
	}
	waitForEvent := stream && response.StatusCode >= 200 && response.StatusCode < 300
	response.Body = newResponseBody(response.Body, ctx, firstByteTimer, bodyCancel, idleTimeoutMs, waitForEvent)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, firstByteAt, readErr := readResponseBody(response.Body)
		if readErr != nil {
			return proxyResponseMeta{}, fmt.Errorf("读取上游错误响应失败: %w", readErr)
		}
		responseHeaders := responseHeaderSnapshot(response)
		meta := proxyResponseMeta{HTTPStatus: response.StatusCode, ResponseHeaders: responseHeaders, Content: append([]byte(nil), body...), FirstByteAt: firstByteAt, UpstreamRequestContentBlobID: metaRequestBlobID}
		meta.UpstreamResponseContentBlobID, contentErr = s.persistAttemptResponseContent(requestID, attemptID, responseHeaders, body, int64(len(body)), requestSettings)
		if contentErr != nil {
			return meta, contentErr
		}
		return meta, newProxyUpstreamError(protocol, response.StatusCode, responseHeaders, body)
	}
	if stream {
		meta, forwardErr := forwardSSE(writer, response, protocol, responseSnapshotLimit(requestSettings.Logging))
		meta.HTTPStatus = response.StatusCode
		meta.ResponseHeaders = responseHeaderSnapshot(response)
		meta.UpstreamRequestContentBlobID = metaRequestBlobID
		completed := time.Now().UTC()
		meta.CompletedAt = &completed
		upstreamContent := meta.UpstreamContent
		if upstreamContent == nil {
			upstreamContent = meta.Content
		}
		meta.UpstreamResponseContentBlobID, contentErr = s.persistAttemptResponseContent(requestID, attemptID, meta.ResponseHeaders, upstreamContent, meta.UpstreamOriginalSize, requestSettings)
		if contentErr != nil {
			return meta, errors.Join(forwardErr, contentErr)
		}
		return meta, forwardErr
	}
	meta, forwardErr := forwardJSON(response, protocol)
	meta.HTTPStatus = response.StatusCode
	meta.ResponseHeaders = responseHeaderSnapshot(response)
	meta.UpstreamRequestContentBlobID = metaRequestBlobID
	completed := time.Now().UTC()
	meta.CompletedAt = &completed
	meta.UpstreamResponseContentBlobID, contentErr = s.persistAttemptResponseContent(requestID, attemptID, meta.ResponseHeaders, meta.Content, int64(len(meta.Content)), requestSettings)
	if contentErr != nil {
		return meta, contentErr
	}
	return meta, forwardErr
}

// rejectUpstreamRedirect 阻止携带渠道凭证的请求跳转到未配置的地址。
func rejectUpstreamRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// persistClientRequestContent 保存客户端请求头和请求体快照。
func (s *Service) persistClientRequestContent(requestID string, request *http.Request, body []byte, settings proxyRequestSettings) error {
	headerBody := headerJSON(requestHeaderSnapshot(request))
	if _, err := storage.WriteContentBlob(s.database, s.secrets, settings.DataDirectory, requestID, "request_headers", headerBody, int64(len(headerBody)), -1, settings.Logging.DiskQuotaBytes); err != nil {
		return fmt.Errorf("%w: 保存客户端请求头: %v", errLedgerWrite, err)
	}
	requestLimit := settings.Logging.MaxRequestContentBytes
	if requestLimit <= 0 {
		requestLimit = settings.Logging.MaxContentBytes
	}
	if _, err := storage.WriteContentBlob(s.database, s.secrets, settings.DataDirectory, requestID, "request", body, int64(len(body)), requestLimit, settings.Logging.DiskQuotaBytes); err != nil {
		return fmt.Errorf("%w: 保存客户端请求体: %v", errLedgerWrite, err)
	}
	return nil
}

// requestHeaderSnapshot 补充 HTTP 请求由传输层隐式携带的 Host 和 Content-Length。
func requestHeaderSnapshot(request *http.Request) http.Header {
	headers := request.Header.Clone()
	host := request.Host
	if host == "" && request.URL != nil {
		host = request.URL.Host
	}
	if host != "" && headers.Get("Host") == "" {
		headers.Set("Host", host)
	}
	if request.ContentLength >= 0 && headers.Get("Content-Length") == "" {
		headers.Set("Content-Length", strconv.FormatInt(request.ContentLength, 10))
	}
	if len(request.TransferEncoding) > 0 && headers.Get("Transfer-Encoding") == "" {
		headers.Set("Transfer-Encoding", strings.Join(request.TransferEncoding, ", "))
	}
	return headers
}

// responseHeaderSnapshot 补充 net/http 从 Header 映射中拆出的 Content-Length 和 Transfer-Encoding。
func responseHeaderSnapshot(response *http.Response) http.Header {
	headers := response.Header.Clone()
	if response.ContentLength >= 0 && headers.Get("Content-Length") == "" {
		headers.Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	}
	if len(response.TransferEncoding) > 0 && headers.Get("Transfer-Encoding") == "" {
		headers.Set("Transfer-Encoding", strings.Join(response.TransferEncoding, ", "))
	}
	return headers
}

// persistAttemptRequestContent 保存 Attempt 实际发出的上游请求头和请求体快照。
func (s *Service) persistAttemptRequestContent(requestID, attemptID string, headers http.Header, body []byte, settings proxyRequestSettings, credentialHeader string) (string, error) {
	headerBody := headerJSON(headers)
	if _, err := storage.WriteAttemptContentBlobWithRedact(s.database, s.secrets, settings.DataDirectory, requestID, attemptID, "upstream_request_headers", headerBody, int64(len(headerBody)), -1, settings.Logging.DiskQuotaBytes, credentialHeader); err != nil {
		return "", fmt.Errorf("%w: 保存上游请求头: %v", errLedgerWrite, err)
	}
	requestLimit := settings.Logging.MaxRequestContentBytes
	if requestLimit <= 0 {
		requestLimit = settings.Logging.MaxContentBytes
	}
	if blob, err := storage.WriteAttemptContentBlob(s.database, s.secrets, settings.DataDirectory, requestID, attemptID, "upstream_request_body", body, int64(len(body)), requestLimit, settings.Logging.DiskQuotaBytes); err == nil {
		return blob.ID, nil
	} else {
		return "", fmt.Errorf("%w: 保存上游请求体: %v", errLedgerWrite, err)
	}
}

// persistAttemptResponseContent 保存 Attempt 收到的上游响应头和响应体快照。
func (s *Service) persistAttemptResponseContent(requestID, attemptID string, headers http.Header, body []byte, originalSize int64, settings proxyRequestSettings) (string, error) {
	headerBody := headerJSON(headers)
	if _, err := storage.WriteAttemptContentBlob(s.database, s.secrets, settings.DataDirectory, requestID, attemptID, "upstream_response_headers", headerBody, int64(len(headerBody)), -1, settings.Logging.DiskQuotaBytes); err != nil {
		return "", fmt.Errorf("%w: 保存上游响应头: %v", errLedgerWrite, err)
	}
	if blob, err := storage.WriteAttemptContentBlob(s.database, s.secrets, settings.DataDirectory, requestID, attemptID, "upstream_response_body", body, originalSize, responseSnapshotLimit(settings.Logging), settings.Logging.DiskQuotaBytes); err != nil {
		return "", fmt.Errorf("%w: 保存上游响应体: %v", errLedgerWrite, err)
	} else {
		return blob.ID, nil
	}
}

// persistFinalResponseContent 保存最终返回客户端的响应头和响应体快照。
func (s *Service) persistFinalResponseContent(requestID string, headers http.Header, body []byte, originalSize int64, settings proxyRequestSettings) error {
	headerBody := headerJSON(headers)
	if _, err := storage.WriteContentBlob(s.database, s.secrets, settings.DataDirectory, requestID, "response_headers", headerBody, int64(len(headerBody)), -1, settings.Logging.DiskQuotaBytes); err != nil {
		return fmt.Errorf("%w: 保存最终响应头: %v", errLedgerWrite, err)
	}
	if originalSize < int64(len(body)) {
		originalSize = int64(len(body))
	}
	if _, err := storage.WriteContentBlob(s.database, s.secrets, settings.DataDirectory, requestID, "response", body, originalSize, responseSnapshotLimit(settings.Logging), settings.Logging.DiskQuotaBytes); err != nil {
		return fmt.Errorf("%w: 保存最终响应体: %v", errLedgerWrite, err)
	}
	return nil
}

// responseSnapshotLimit 返回日志响应正文上限；非正值沿用现有 1 MiB 规范化语义。
func responseSnapshotLimit(logging config.LoggingSettings) int {
	if logging.MaxResponseContentBytes > 0 {
		return logging.MaxResponseContentBytes
	}
	if logging.MaxContentBytes > 0 {
		return logging.MaxContentBytes
	}
	return 1 << 20
}

// totalProxyTokens 优先读取上游总 Token，缺失时由输入和输出 Token 计算。
func totalProxyTokens(meta proxyResponseMeta) *int64 {
	if meta.TotalTokensPresent {
		value := meta.TotalTokens
		return &value
	}
	if meta.InputTokensPresent && meta.OutputTokensPresent {
		value := meta.InputTokens + meta.OutputTokens
		return &value
	}
	return nil
}

// proxyErrorStatus 从上游 HTTP 错误中提取状态码。
func proxyErrorStatus(err error) int {
	var upstreamErr *proxyUpstreamError
	if errors.As(err, &upstreamErr) {
		return upstreamErr.status
	}
	return 0
}

// finalRequestStatus 将最终代理错误转换为请求账本终态。
func finalRequestStatus(err error) string {
	if err == nil {
		return "error"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errFirstByteTimeout) || errors.Is(err, errStreamIdleTimeout) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "error"
}

// retryLedger 对 SQLite 账本写入做少量重试，避免短暂 busy 错误造成缺失记录。
func retryLedger(operation func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = operation()
		if err == nil {
			return nil
		}
		if attempt < 2 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	return err
}

// proxyFailureMeta 构造最终返回客户端的失败响应快照元数据。
func proxyFailureMeta(err error, contextErr error) proxyResponseMeta {
	if errors.Is(err, errLedgerWrite) {
		return ledgerFailureMeta("请求日志持久化失败")
	}
	status, headers, body := http.StatusBadGateway, http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, []byte(`{"error":"上游请求失败"}`)
	if err != nil {
		var routeErr *routingError
		var upstreamErr *proxyUpstreamError
		if errors.Is(err, storage.ErrRouteStorage) {
			status = http.StatusInternalServerError
			body = []byte(`{"error":"读取路由配置失败","code":"route_storage_error"}`)
		} else if errors.As(err, &routeErr) {
			status = routeErr.status
			body, _ = json.Marshal(map[string]string{"error": routeErr.message, "code": routeErr.class})
		} else if errors.As(err, &upstreamErr) {
			status, headers, body = upstreamErr.status, upstreamErr.headers.Clone(), append([]byte(nil), upstreamErr.body...)
		} else {
			body, _ = json.Marshal(map[string]string{"error": err.Error()})
		}
	}
	return proxyResponseMeta{HTTPStatus: status, ResponseHeaders: headers, Content: body, ErrorClass: classifyProxyError(err, contextErr), ErrorMessage: errorSummary(err)}
}

// ledgerFailureMeta 构造请求日志持久化失败时返回客户端的响应元数据。
func ledgerFailureMeta(message string) proxyResponseMeta {
	body, _ := json.Marshal(map[string]string{"error": message})
	return proxyResponseMeta{HTTPStatus: http.StatusInternalServerError, ResponseHeaders: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Content: body, ErrorClass: "ledger_error", ErrorMessage: message}
}

// errorSummary 生成写入请求账本的简短错误摘要。
func errorSummary(err error) string {
	if err == nil {
		return "上游请求失败"
	}
	return err.Error()
}

// reasoningProxyTokens 在上游明确返回思考 Token 时生成可空账本值。
func reasoningProxyTokens(meta proxyResponseMeta) *int64 {
	if !meta.ReasoningTokensPresent {
		return nil
	}
	value := meta.ReasoningTokens
	return &value
}

// cacheWriteProxyTokens 在上游明确返回缓存写入 Token 时生成可空账本值。
func cacheWriteProxyTokens(meta proxyResponseMeta) *int64 {
	if !meta.CacheWriteTokensPresent {
		return nil
	}
	value := meta.CacheWriteInputTokens
	return &value
}

// headerJSON 将完整 Header 快照编码为稳定的 JSON 正文。
func headerJSON(headers http.Header) []byte {
	value, _ := json.Marshal(headers)
	return value
}

// readResponseBody 读取响应正文并记录实际收到首个字节的时间。
func readResponseBody(body io.Reader) ([]byte, *time.Time, error) {
	var content bytes.Buffer
	buffer := make([]byte, 32<<10)
	var firstByteAt *time.Time
	for {
		count, err := body.Read(buffer)
		if count > 0 {
			if firstByteAt == nil {
				now := time.Now().UTC()
				firstByteAt = &now
			}
			_, _ = content.Write(buffer[:count])
		}
		if err == io.EOF {
			return content.Bytes(), firstByteAt, nil
		}
		if err != nil {
			return content.Bytes(), firstByteAt, err
		}
	}
}

// firstByteTimerState 串行化首字节超时回调与首事件提交，避免 deadline 边界误取消。
type firstByteTimerState struct {
	mu       sync.Mutex
	timer    *time.Timer
	cancel   context.CancelCauseFunc
	timedOut bool
}

func newFirstByteTimer(timeout time.Duration, cancel context.CancelCauseFunc) *firstByteTimerState {
	state := &firstByteTimerState{cancel: cancel}
	state.timer = time.AfterFunc(timeout, state.fire)
	return state
}

func (state *firstByteTimerState) fire() {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.timer == nil {
		return
	}
	state.timer = nil
	state.timedOut = true
	state.cancel(errFirstByteTimeout)
}

func (state *firstByteTimerState) stop() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
	return !state.timedOut
}

// timedResponseBody 在不改变上游字节的前提下实施首字节和流空闲超时。
type timedResponseBody struct {
	body                io.ReadCloser
	ctx                 context.Context
	firstByteTimer      *firstByteTimerState
	cancel              context.CancelCauseFunc
	idleTimeout         time.Duration
	idleTimer           *time.Timer
	stopFirstByteOnRead bool
	idleAfterCommit     bool
	mu                  sync.Mutex
}

// newResponseBody 为上游响应添加首字节和流空闲超时，不改变读取到的字节。
func newResponseBody(body io.ReadCloser, ctx context.Context, firstByteTimer *firstByteTimerState, cancel context.CancelCauseFunc, idleTimeoutMs int, waitForEvent bool) io.ReadCloser {
	return &timedResponseBody{body: body, ctx: ctx, firstByteTimer: firstByteTimer, cancel: cancel, idleTimeout: time.Duration(idleTimeoutMs) * time.Millisecond, stopFirstByteOnRead: !waitForEvent, idleAfterCommit: !waitForEvent}
}

// Read 读取上游响应并在收到数据后停止首字节计时、刷新流空闲计时。
func (body *timedResponseBody) Read(buffer []byte) (int, error) {
	count, err := body.body.Read(buffer)
	if count > 0 {
		body.mu.Lock()
		if body.stopFirstByteOnRead {
			body.stopFirstByteTimerLocked()
		}
		if body.idleTimeout > 0 && body.idleAfterCommit {
			if body.idleTimer == nil {
				body.idleTimer = time.AfterFunc(body.idleTimeout, func() {
					body.cancel(errStreamIdleTimeout)
				})
			} else {
				body.idleTimer.Reset(body.idleTimeout)
			}
		}
		body.mu.Unlock()
	}
	if err != nil {
		body.stopTimers()
		cause := context.Cause(body.ctx)
		if errors.Is(cause, errFirstByteTimeout) || errors.Is(cause, errStreamIdleTimeout) {
			return count, cause
		}
	}
	return count, err
}

// markStreamCommitted 在首个合法 SSE 事件提交后启动流空闲计时。
func (body *timedResponseBody) markStreamCommitted() {
	body.mu.Lock()
	defer body.mu.Unlock()
	body.idleAfterCommit = true
	if body.idleTimeout > 0 {
		if body.idleTimer == nil {
			body.idleTimer = time.AfterFunc(body.idleTimeout, func() {
				body.cancel(errStreamIdleTimeout)
			})
		} else {
			body.idleTimer.Reset(body.idleTimeout)
		}
	}
}

// stopFirstByteTimer 在 SSE 首个有效事件提交时停止首字节计时。
func (body *timedResponseBody) stopFirstByteTimer() bool {
	body.mu.Lock()
	defer body.mu.Unlock()
	return body.stopFirstByteTimerLocked()
}

func (body *timedResponseBody) stopFirstByteTimerLocked() bool {
	if body.firstByteTimer != nil {
		return body.firstByteTimer.stop()
	}
	return true
}

// Close 关闭响应体并取消仍在运行的上游请求。
func (body *timedResponseBody) Close() error {
	body.stopTimers()
	body.cancel(nil)
	return body.body.Close()
}

// stopTimers 停止响应体上的全部计时器。
func (body *timedResponseBody) stopTimers() {
	body.mu.Lock()
	body.stopFirstByteTimerLocked()
	if body.idleTimer != nil {
		body.idleTimer.Stop()
		body.idleTimer = nil
	}
	body.mu.Unlock()
}
