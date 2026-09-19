package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/storage"
)

const defaultAnthropicVersion = "2023-06-01"

// maxSSEEventBytes 限制单个 SSE 事件在内存中的解析大小。
// Codex CLI 0.148.0 与 Claude Code 2.1.220 的最小流式请求未产生超大单事件；
// 该上限用于防止无换行输入在提交前后无限增长，不属于已验证的官方协议上限。
const maxSSEEventBytes = 1 << 20

// applyProtocolRequestHeaders 补齐代理自行生成请求所需的协议头，并保留渠道显式配置。
func applyProtocolRequestHeaders(request *http.Request, protocol storage.Protocol) {
	if protocol == storage.ProtocolAnthropicMessages && len(request.Header.Values("anthropic-version")) == 0 {
		request.Header.Set("anthropic-version", defaultAnthropicVersion)
	}
}

// ensureAnthropicAPIKeyHeader 在 Anthropic 请求缺少 x-api-key 时，用凭证明文补上该头。
func ensureAnthropicAPIKeyHeader(request *http.Request, protocol storage.Protocol, credential *storedCredential) {
	if protocol != storage.ProtocolAnthropicMessages || request.Header.Get("x-api-key") != "" {
		return
	}
	if credential == nil || strings.TrimSpace(credential.Secret) == "" {
		return
	}
	request.Header.Set("x-api-key", credential.Secret)
}

// proxyResponseMeta 保存不含正文的协议响应元数据，供 Responses 亲和和后续观测使用。
type proxyResponseMeta struct {
	ResponseID                    string
	FinalChannelID                string
	FinalChannelName              string
	HTTPStatus                    int
	ErrorClass                    string
	ErrorMessage                  string
	InputTokens                   int64
	OutputTokens                  int64
	CacheReadInputTokens          int64
	InputTokensPresent            bool
	OutputTokensPresent           bool
	CacheReadTokensPresent        bool
	TotalTokens                   int64
	ReasoningTokens               int64
	CacheWriteInputTokens         int64
	UsagePresent                  bool
	TotalTokensPresent            bool
	ReasoningTokensPresent        bool
	CacheWriteTokensPresent       bool
	Content                       []byte
	UpstreamContent               []byte
	ContentOriginalSize           int64
	UpstreamOriginalSize          int64
	ResponseHeaders               http.Header
	FirstByteAt                   *time.Time
	FirstEventAt                  *time.Time
	CompletedAt                   *time.Time
	StreamEnded                   *bool
	UpstreamRequestContentBlobID  string
	UpstreamResponseContentBlobID string
}

type proxyTokenPointers struct {
	input, output, cache *int64
}

// proxyMetaTokens 将协议元数据转换为可区分未知值的 token 指针。
func proxyMetaTokens(meta proxyResponseMeta) proxyTokenPointers {
	if !meta.UsagePresent {
		return proxyTokenPointers{}
	}
	var result proxyTokenPointers
	if meta.InputTokensPresent {
		value := meta.InputTokens
		result.input = &value
	}
	if meta.OutputTokensPresent {
		value := meta.OutputTokens
		result.output = &value
	}
	if meta.CacheReadTokensPresent {
		value := meta.CacheReadInputTokens
		result.cache = &value
	}
	return result
}

// mergeProxyResponseMeta 合并事件流中逐步出现的响应 ID 和 usage。
func mergeProxyResponseMeta(destination *proxyResponseMeta, source proxyResponseMeta) {
	if source.ResponseID != "" {
		destination.ResponseID = source.ResponseID
	}
	if source.UsagePresent {
		if source.InputTokensPresent {
			destination.InputTokens, destination.InputTokensPresent = source.InputTokens, true
		}
		if source.OutputTokensPresent {
			destination.OutputTokens, destination.OutputTokensPresent = source.OutputTokens, true
		}
		if source.CacheReadTokensPresent {
			destination.CacheReadInputTokens, destination.CacheReadTokensPresent = source.CacheReadInputTokens, true
		}
		destination.UsagePresent = true
	}
	if source.TotalTokensPresent {
		destination.TotalTokens, destination.TotalTokensPresent = source.TotalTokens, true
	}
	if source.ReasoningTokensPresent {
		destination.ReasoningTokens, destination.ReasoningTokensPresent = source.ReasoningTokens, true
	}
	if source.CacheWriteTokensPresent {
		destination.CacheWriteInputTokens, destination.CacheWriteTokensPresent = source.CacheWriteInputTokens, true
	}
	if len(source.Content) > 0 {
		destination.Content = append(destination.Content, source.Content...)
	}
	if source.ContentOriginalSize > destination.ContentOriginalSize {
		destination.ContentOriginalSize = source.ContentOriginalSize
	}
	if source.UpstreamOriginalSize > destination.UpstreamOriginalSize {
		destination.UpstreamOriginalSize = source.UpstreamOriginalSize
	}
	if source.HTTPStatus != 0 {
		destination.HTTPStatus = source.HTTPStatus
	}
	if source.ResponseHeaders != nil {
		destination.ResponseHeaders = source.ResponseHeaders
	}
	if source.FirstByteAt != nil && destination.FirstByteAt == nil {
		destination.FirstByteAt = source.FirstByteAt
	}
	if source.FirstEventAt != nil && destination.FirstEventAt == nil {
		destination.FirstEventAt = source.FirstEventAt
	}
	if source.CompletedAt != nil {
		destination.CompletedAt = source.CompletedAt
	}
	if source.StreamEnded != nil {
		destination.StreamEnded = source.StreamEnded
	}
}

// parseProtocolResponseMeta 提取协议定义的响应 ID 和 usage，不改变上游正文。
func parseProtocolResponseMeta(payload map[string]json.RawMessage, protocol storage.Protocol) proxyResponseMeta {
	meta := proxyResponseMeta{ResponseID: jsonString(payload["id"])}
	if rawResponse, ok := payload["response"]; ok {
		var response map[string]json.RawMessage
		if json.Unmarshal(rawResponse, &response) == nil {
			mergeProxyResponseMeta(&meta, proxyResponseMeta{ResponseID: jsonString(response["id"])})
			if usage, ok := response["usage"]; ok {
				mergeProxyResponseMeta(&meta, parseUsage(usage, protocol))
			}
		}
	}
	if usage, ok := payload["usage"]; ok {
		mergeProxyResponseMeta(&meta, parseUsage(usage, protocol))
	}
	return meta
}

// parseUsage 解析三种协议的常见 token 字段，未知或缺失字段保持为未知。
func parseUsage(raw json.RawMessage, protocol storage.Protocol) proxyResponseMeta {
	var usage map[string]json.RawMessage
	if json.Unmarshal(raw, &usage) != nil || usage == nil {
		return proxyResponseMeta{}
	}
	meta := proxyResponseMeta{UsagePresent: true}
	switch protocol {
	case storage.ProtocolOpenAIResponses, storage.ProtocolOpenAIChat:
		meta.InputTokens, meta.InputTokensPresent = usageInt64(usage, "input_tokens")
		if !meta.InputTokensPresent {
			meta.InputTokens, meta.InputTokensPresent = usageInt64(usage, "prompt_tokens")
		}
		meta.OutputTokens, meta.OutputTokensPresent = usageInt64(usage, "output_tokens")
		if !meta.OutputTokensPresent {
			meta.OutputTokens, meta.OutputTokensPresent = usageInt64(usage, "completion_tokens")
		}
		if details, ok := usage["input_tokens_details"]; ok {
			var value map[string]json.RawMessage
			if json.Unmarshal(details, &value) == nil {
				meta.CacheReadInputTokens, meta.CacheReadTokensPresent = usageInt64(value, "cached_tokens")
			}
		}
		if details, ok := usage["prompt_tokens_details"]; ok {
			var value map[string]json.RawMessage
			if json.Unmarshal(details, &value) == nil && !meta.CacheReadTokensPresent {
				meta.CacheReadInputTokens, meta.CacheReadTokensPresent = usageInt64(value, "cached_tokens")
			}
		}
		if raw, ok := usage["total_tokens"]; ok {
			meta.TotalTokens, meta.TotalTokensPresent = parseInt64(raw)
		}
		if raw, ok := usage["reasoning_tokens"]; ok {
			meta.ReasoningTokens, meta.ReasoningTokensPresent = parseInt64(raw)
		}
		if details, ok := usage["output_tokens_details"]; ok {
			var value map[string]json.RawMessage
			if json.Unmarshal(details, &value) == nil {
				if raw, exists := value["reasoning_tokens"]; exists {
					meta.ReasoningTokens, meta.ReasoningTokensPresent = parseInt64(raw)
				}
			}
		}
	case storage.ProtocolAnthropicMessages:
		meta.InputTokens, meta.InputTokensPresent = usageInt64(usage, "input_tokens")
		meta.OutputTokens, meta.OutputTokensPresent = usageInt64(usage, "output_tokens")
		meta.CacheReadInputTokens, meta.CacheReadTokensPresent = usageInt64(usage, "cache_read_input_tokens")
		if raw, ok := usage["cache_creation_input_tokens"]; ok {
			meta.CacheWriteInputTokens, meta.CacheWriteTokensPresent = parseInt64(raw)
		}
		if raw, ok := usage["total_tokens"]; ok {
			meta.TotalTokens, meta.TotalTokensPresent = parseInt64(raw)
		}
	}
	return meta
}

// responsesLifecycleEvents 列出 OpenAI Responses 流式响应的合法非终止生命周期事件。
var responsesLifecycleEvents = map[string]bool{
	"response.audio.delta":                         true,
	"response.audio.done":                          true,
	"response.audio.transcript.delta":              true,
	"response.audio.transcript.done":               true,
	"response.code_interpreter_call.completed":     true,
	"response.code_interpreter_call.in_progress":   true,
	"response.code_interpreter_call.interpreting":  true,
	"response.code_interpreter_call_code.delta":    true,
	"response.code_interpreter_call_code.done":     true,
	"response.content_part.added":                  true,
	"response.content_part.done":                   true,
	"response.created":                             true,
	"response.custom_tool_call_input.delta":        true,
	"response.custom_tool_call_input.done":         true,
	"response.file_search_call.completed":          true,
	"response.file_search_call.in_progress":        true,
	"response.file_search_call.searching":          true,
	"response.function_call_arguments.delta":       true,
	"response.function_call_arguments.done":        true,
	"response.image_generation_call.completed":     true,
	"response.image_generation_call.generating":    true,
	"response.image_generation_call.in_progress":   true,
	"response.image_generation_call.partial_image": true,
	"response.in_progress":                         true,
	"response.mcp_call.completed":                  true,
	"response.mcp_call.failed":                     true,
	"response.mcp_call.in_progress":                true,
	"response.mcp_call_arguments.delta":            true,
	"response.mcp_call_arguments.done":             true,
	"response.mcp_list_tools.completed":            true,
	"response.mcp_list_tools.failed":               true,
	"response.mcp_list_tools.in_progress":          true,
	"response.output_item.added":                   true,
	"response.output_item.done":                    true,
	"response.output_text.annotation.added":        true,
	"response.output_text.delta":                   true,
	"response.output_text.done":                    true,
	"response.queued":                              true,
	"response.reasoning_summary_part.added":        true,
	"response.reasoning_summary_part.done":         true,
	"response.reasoning_summary_text.delta":        true,
	"response.reasoning_summary_text.done":         true,
	"response.reasoning_text.delta":                true,
	"response.reasoning_text.done":                 true,
	"response.refusal.delta":                       true,
	"response.refusal.done":                        true,
	"response.shell_call_command.added":            true,
	"response.shell_call_command.delta":            true,
	"response.shell_call_command.done":             true,
	"response.shell_call_output_content.delta":     true,
	"response.shell_call_output_content.done":      true,
	"response.web_search_call.completed":           true,
	"response.web_search_call.in_progress":         true,
	"response.web_search_call.searching":           true,
}

// inspectSSEFrame 校验单个 SSE 帧并识别提交点、终止事件和响应元数据。
func inspectSSEFrame(frame []byte, protocol storage.Protocol) (bool, bool, proxyResponseMeta, error) {
	var eventName string
	var dataLines []string
	for _, line := range bytes.Split(frame, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			eventName = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
		case bytes.HasPrefix(line, []byte("data:")):
			dataLines = append(dataLines, strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("data:")))))
		case bytes.HasPrefix(line, []byte("id:")), bytes.HasPrefix(line, []byte("retry:")):
			// SSE 标准字段，透传但不参与协议提交判断。
		case len(bytes.TrimSpace(line)) == 0 || bytes.HasPrefix(line, []byte(":")):
		default:
			return false, false, proxyResponseMeta{}, errors.New("上游返回非法 SSE 行")
		}
	}
	if len(dataLines) == 0 {
		return false, false, proxyResponseMeta{}, nil
	}
	data := strings.Join(dataLines, "\n")
	if protocol == storage.ProtocolOpenAIChat && data == "[DONE]" {
		return false, true, proxyResponseMeta{StreamEnded: boolPointer(true)}, nil
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(data), &payload) != nil || payload == nil {
		return false, false, proxyResponseMeta{}, errors.New("上游返回非法 SSE JSON")
	}
	meta := parseProtocolResponseMeta(payload, protocol)
	now := time.Now().UTC()
	meta.FirstByteAt = &now
	payloadType := jsonString(payload["type"])
	if eventName == "" {
		eventName = payloadType
	} else if payloadType != "" && payloadType != eventName {
		return false, false, meta, errors.New("上游 SSE 事件名与 JSON type 不一致")
	}
	switch protocol {
	case storage.ProtocolOpenAIChat:
		rawChoices, ok := payload["choices"]
		if !ok {
			return false, false, meta, errors.New("上游返回的 OpenAI Chat SSE 缺少 choices")
		}
		var choices []json.RawMessage
		if json.Unmarshal(rawChoices, &choices) != nil {
			return false, false, meta, errors.New("上游返回的 OpenAI Chat SSE choices 无效")
		}
		if len(choices) == 0 {
			return false, false, meta, nil
		}
		meta.FirstEventAt = &now
		return true, false, meta, nil
	case storage.ProtocolOpenAIResponses:
		switch eventName {
		case "response.failed", "response.incomplete", "error":
			return false, false, meta, errors.New("上游返回 OpenAI Responses 错误事件")
		case "response.completed":
			if err := validateResponsesLifecyclePayload(payload, eventName); err != nil {
				return false, false, meta, err
			}
			meta.FirstEventAt = &now
			meta.StreamEnded = boolPointer(true)
			return true, true, meta, nil
		}
		if responsesLifecycleEvents[eventName] {
			if err := validateResponsesLifecyclePayload(payload, eventName); err != nil {
				return false, false, meta, err
			}
			if err := validateResponsesCommitPayload(payload, eventName); err != nil {
				return false, false, meta, err
			}
			meta.FirstEventAt = &now
			return true, false, meta, nil
		}
		return false, false, meta, nil
	case storage.ProtocolAnthropicMessages:
		switch eventName {
		case "error":
			return false, false, meta, errors.New("上游返回 Anthropic SSE 错误事件")
		case "message_start":
			if err := validateMessagesStartPayload(payload); err != nil {
				return false, false, meta, err
			}
			meta.FirstEventAt = &now
			return true, false, meta, nil
		case "message_stop":
			meta.StreamEnded = boolPointer(true)
			return false, true, meta, nil
		default:
			return false, false, meta, nil
		}
	default:
		return false, false, meta, errors.New("不支持的协议")
	}
}

func responsesLifecycleNeedsObject(eventName string) bool {
	switch eventName {
	case "response.created", "response.in_progress", "response.completed", "response.queued":
		return true
	default:
		return false
	}
}

func validateResponsesLifecyclePayload(payload map[string]json.RawMessage, eventName string) error {
	if !responsesLifecycleNeedsObject(eventName) {
		return nil
	}
	rawResponse, ok := payload["response"]
	if !ok {
		return errors.New("上游 Responses 生命周期事件缺少 response 对象")
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(rawResponse, &response) != nil || response == nil {
		return errors.New("上游 Responses 生命周期事件的 response 无效")
	}
	if jsonString(response["id"]) == "" {
		return errors.New("上游 Responses 生命周期事件缺少响应 ID")
	}
	status := jsonString(response["status"])
	expected := expectedResponsesStatus(eventName)
	if expected != "" && status != expected {
		return errors.New("上游 Responses 生命周期事件状态与事件名不一致")
	}
	return nil
}

// validateResponsesCommitPayload 校验可作为提交点的非对象生命周期事件的最小字段和类型。
func validateResponsesCommitPayload(payload map[string]json.RawMessage, eventName string) error {
	if responsesLifecycleNeedsObject(eventName) {
		return nil
	}
	switch {
	case strings.HasSuffix(eventName, ".delta"):
		if responsesDeltaMustBeString(eventName) {
			if !isJSONString(payload["delta"], true) {
				return errors.New("上游 Responses delta 事件的 delta 无效")
			}
		} else if _, ok := payload["delta"]; !ok || isJSONNull(payload["delta"]) {
			return errors.New("上游 Responses delta 事件缺少 delta")
		}
	case strings.Contains(eventName, "output_item"):
		if !isJSONObject(payload["item"]) {
			return errors.New("上游 Responses item 事件的 item 无效")
		}
	case strings.Contains(eventName, "content_part"):
		if !isJSONObject(payload["part"]) {
			return errors.New("上游 Responses content_part 事件的 part 无效")
		}
	default:
		if !hasResponsesEventIdentity(payload) {
			return errors.New("上游 Responses 事件缺少必要结构")
		}
	}
	if raw, ok := payload["item_id"]; ok && !isJSONString(raw, false) {
		return errors.New("上游 Responses 事件的 item_id 无效")
	}
	if raw, ok := payload["output_index"]; ok && !isJSONNumber(raw) {
		return errors.New("上游 Responses 事件的 output_index 无效")
	}
	if raw, ok := payload["content_index"]; ok && !isJSONNumber(raw) {
		return errors.New("上游 Responses 事件的 content_index 无效")
	}
	return nil
}

// hasResponsesEventIdentity 判断非对象生命周期事件是否带有类型正确的业务字段。
func hasResponsesEventIdentity(payload map[string]json.RawMessage) bool {
	if isJSONString(payload["item_id"], false) {
		return true
	}
	if isJSONObject(payload["item"]) || isJSONObject(payload["part"]) || isJSONObject(payload["annotation"]) {
		return true
	}
	if isJSONString(payload["delta"], true) {
		return true
	}
	return isJSONNumber(payload["output_index"]) || isJSONNumber(payload["content_index"])
}

func isJSONString(raw json.RawMessage, allowEmpty bool) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var value string
	if json.Unmarshal(trimmed, &value) != nil {
		return false
	}
	return allowEmpty || strings.TrimSpace(value) != ""
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(trimmed, &object) == nil && object != nil
}

func isJSONNumber(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var value float64
	return json.Unmarshal(trimmed, &value) == nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func responsesDeltaMustBeString(eventName string) bool {
	return strings.Contains(eventName, "text") ||
		strings.Contains(eventName, "refusal") ||
		strings.Contains(eventName, "arguments") ||
		strings.Contains(eventName, "transcript") ||
		strings.Contains(eventName, "code") ||
		strings.Contains(eventName, "input")
}

func expectedResponsesStatus(eventName string) string {
	switch eventName {
	case "response.created":
		return "in_progress"
	case "response.in_progress":
		return "in_progress"
	case "response.completed":
		return "completed"
	case "response.queued":
		return "queued"
	default:
		return ""
	}
}

func validateMessagesStartPayload(payload map[string]json.RawMessage) error {
	rawMessage, ok := payload["message"]
	if !ok {
		return errors.New("上游 Messages message_start 缺少 message 对象")
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(rawMessage, &message) != nil || message == nil {
		return errors.New("上游 Messages message_start 的 message 无效")
	}
	if jsonString(message["type"]) != "message" {
		return errors.New("上游 Messages message_start 缺少合法消息类型")
	}
	if jsonString(message["id"]) == "" {
		return errors.New("上游 Messages message_start 缺少消息 ID")
	}
	return nil
}

func boolPointer(value bool) *bool { return &value }

// jsonString 从 JSON 字段安全读取非空字符串。
func jsonString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// jsonInt64 从 JSON 字段读取非负整数，异常值视为未知。
func jsonInt64(raw json.RawMessage) int64 {
	var value int64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value < 0 {
		return 0
	}
	return value
}

func usageInt64(values map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := values[key]
	if !ok || len(raw) == 0 {
		return 0, false
	}
	return parseInt64(raw)
}

func parseInt64(raw json.RawMessage) (int64, bool) {
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < 0 {
		return 0, false
	}
	return value, true
}
