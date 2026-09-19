package server

import (
	"encoding/json"
	"strings"
)

// proxyCapabilities 从协议请求体提取路由所需的固定能力集合。
func proxyCapabilities(fields map[string]json.RawMessage) []string {
	capabilities := make([]string, 0, 5)
	if raw, ok := fields["stream"]; ok {
		var enabled bool
		if json.Unmarshal(raw, &enabled) == nil && enabled {
			capabilities = append(capabilities, "streaming")
		}
	}
	if raw, ok := fields["tools"]; ok && nonEmptyJSONArray(raw) {
		capabilities = append(capabilities, "tools")
	}
	vision, audio := false, false
	for _, name := range []string{"input", "messages"} {
		var value any
		if raw, ok := fields[name]; ok && json.Unmarshal(raw, &value) == nil {
			valueVision, valueAudio := mediaCapabilities(value)
			vision = vision || valueVision
			audio = audio || valueAudio
		}
	}
	if raw, ok := fields["audio"]; ok && meaningfulJSONValue(raw) {
		audio = true
	}
	if raw, ok := fields["modalities"]; ok {
		var modalities []string
		if json.Unmarshal(raw, &modalities) == nil {
			for _, modality := range modalities {
				if modality == "audio" {
					audio = true
					break
				}
			}
		}
	}
	if vision {
		capabilities = append(capabilities, "vision")
	}
	if audio {
		capabilities = append(capabilities, "audio")
	}
	for _, name := range []string{"reasoning", "reasoning_effort", "thinking"} {
		if raw, ok := fields[name]; ok && meaningfulJSONValue(raw) {
			capabilities = append(capabilities, "reasoning")
			break
		}
	}
	return capabilities
}

// nonEmptyJSONArray 判断字段是否为至少包含一个元素的 JSON 数组。
func nonEmptyJSONArray(raw json.RawMessage) bool {
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && len(values) > 0
}

// meaningfulJSONValue 判断字段是否明确启用或携带了非空配置。
func meaningfulJSONValue(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case bool:
		return typed
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// mediaCapabilities 递归识别消息内容块中的视觉和音频输入类型。
func mediaCapabilities(value any) (bool, bool) {
	vision, audio := false, false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			itemVision, itemAudio := mediaCapabilities(item)
			vision = vision || itemVision
			audio = audio || itemAudio
		}
	case map[string]any:
		if contentType, ok := typed["type"].(string); ok {
			switch contentType {
			case "input_image", "image_url", "image":
				vision = true
			case "input_audio", "audio", "audio_url":
				audio = true
			}
		}
		for _, item := range typed {
			itemVision, itemAudio := mediaCapabilities(item)
			vision = vision || itemVision
			audio = audio || itemAudio
		}
	}
	return vision, audio
}
