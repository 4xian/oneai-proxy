package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/storage"
)

var runtimeSecretPattern = regexp.MustCompile(`(?i)(bearer\s+)[^\s,;]+|((?:authorization|api[-_ ]?key|apikey|token|secret|password|cookie)\s*[:=]\s*(?:bearer\s+)?)[^\s,;]+`)

// emitRuntime 写入 SQLite、轮转文件并向 SSE 订阅者广播；失败只影响运行日志，不阻塞业务。
// 某个订阅缓冲满时关闭该通道，由 runtimeStream 发送 reset，避免静默丢事件。
func (s *Service) emitRuntime(level, event, message string, attrs map[string]any) {
	id, err := storage.NewRuntimeLogID()
	if err != nil {
		return
	}
	sanitizeRuntimeContext(attrs)
	contextJSON, _ := json.Marshal(attrs)
	entry := storage.RuntimeLog{ID: id, OccurredAt: storage.FormatSQLiteTime(time.Now()), Level: level, Event: event, Message: sanitizeRuntimeText(message), Context: contextJSON}
	if attrs != nil {
		if value, ok := attrs["requestID"].(string); ok {
			entry.RequestID = value
		}
		if value, ok := attrs["attemptID"].(string); ok {
			entry.AttemptID = value
		}
		if value, ok := attrs["channelID"].(string); ok {
			entry.ChannelID = value
		}
	}
	if err := storage.RecordRuntimeLog(s.database, entry); err != nil {
		return
	}
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.writeRuntimeFileLocked(entry)
	for id, subscriber := range s.runtimeSubs {
		select {
		case subscriber <- entry:
		default:
			// 缓冲满则关闭订阅，stream 据此发 reset，避免静默丢事件。
			delete(s.runtimeSubs, id)
			close(subscriber)
		}
	}
}

func sanitizeRuntimeContext(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			name := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", "-"), " ", "-"))
			if strings.Contains(name, "authorization") || strings.Contains(name, "api-key") || strings.Contains(name, "apikey") || name == "token" || strings.HasSuffix(name, "-token") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "cookie") || strings.Contains(name, "header") || strings.Contains(name, "body") {
				current[key] = "***"
				continue
			}
			if text, ok := child.(string); ok {
				current[key] = sanitizeRuntimeText(text)
				continue
			}
			sanitizeRuntimeContext(child)
		}
	case []any:
		for index, child := range current {
			if text, ok := child.(string); ok {
				current[index] = sanitizeRuntimeText(text)
				continue
			}
			sanitizeRuntimeContext(child)
		}
	}
}

func sanitizeRuntimeText(value string) string {
	value = strings.TrimSpace(value)
	return runtimeSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		if strings.HasPrefix(strings.ToLower(match), "bearer") {
			return "Bearer ***"
		}
		for _, separator := range []string{"=", ":"} {
			if index := strings.Index(match, separator); index >= 0 {
				value := strings.TrimSpace(match[index+1:])
				if strings.HasPrefix(strings.ToLower(value), "bearer ") {
					return match[:index+1] + " Bearer ***"
				}
				return match[:index+1] + "***"
			}
		}
		return "***"
	})
}

func (s *Service) writeRuntimeFileLocked(entry storage.RuntimeLog) {
	settings := s.snapshotSettings()
	if strings.TrimSpace(settings.DataDirectory) == "" {
		return
	}
	directory := filepath.Join(settings.DataDirectory, "logs")
	if os.MkdirAll(directory, 0o700) != nil {
		return
	}
	day := time.Now().UTC().Format("2006-01-02")
	maxBytes := settings.Logging.RuntimeLogMaxBytes
	if maxBytes <= 0 {
		maxBytes = 100 << 20
	}
	if s.runtimeFile == nil || s.runtimeDay != day {
		if s.runtimeFile != nil {
			_ = s.runtimeFile.Close()
		}
		path := filepath.Join(directory, "runtime.log")
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			fileDay := info.ModTime().UTC().Format("2006-01-02")
			if fileDay != day || s.runtimeDay != "" && s.runtimeDay != day {
				rotated := filepath.Join(directory, "runtime-"+fileDay+".log")
				if _, existsErr := os.Stat(rotated); existsErr == nil {
					rotated = filepath.Join(directory, fmt.Sprintf("runtime-%s-%d.log", fileDay, time.Now().UnixNano()))
				}
				_ = os.Rename(path, rotated)
			}
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		s.runtimeFile, s.runtimeDay = file, day
	}
	line, _ := json.Marshal(entry)
	line = append(line, '\n')
	if info, err := s.runtimeFile.Stat(); err == nil && info.Size()+int64(len(line)) > maxBytes {
		_ = s.runtimeFile.Close()
		rotated := filepath.Join(directory, fmt.Sprintf("runtime-%s-%d.log", day, time.Now().UnixNano()))
		_ = os.Rename(filepath.Join(directory, "runtime.log"), rotated)
		file, err := os.OpenFile(filepath.Join(directory, "runtime.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			s.runtimeFile = nil
			return
		}
		s.runtimeFile = file
	}
	_, _ = s.runtimeFile.Write(line)
	_ = s.runtimeFile.Sync()
}

func (s *Service) cleanupRuntimeFiles(cutoff time.Time) int64 {
	directory := filepath.Join(s.snapshotSettings().DataDirectory, "logs")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0
	}
	var removed int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "runtime-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(directory, entry.Name())) == nil {
			removed++
		}
	}
	return removed
}

// subscribeRuntime 注册容量为 32 的 live 订阅；缓冲满时由 emitRuntime 关闭该通道。
func (s *Service) subscribeRuntime() (int, <-chan storage.RuntimeLog) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runtimeNext++
	channel := make(chan storage.RuntimeLog, 32)
	s.runtimeSubs[s.runtimeNext] = channel
	return s.runtimeNext, channel
}

// unsubscribeRuntime 移除订阅并关闭通道；通道已被溢出关闭时为 no-op。
func (s *Service) unsubscribeRuntime(id int) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if channel, ok := s.runtimeSubs[id]; ok {
		delete(s.runtimeSubs, id)
		close(channel)
	}
}

func writeRuntimeSSE(writer http.ResponseWriter, entry storage.RuntimeLog) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "id: %s\nevent: runtime\ndata: %s\n\n", entry.ID, data); err != nil {
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// writeRuntimeResetSSE 通知客户端丢失游标，需要重新读取最近日志。
func writeRuntimeResetSSE(writer http.ResponseWriter) error {
	if _, err := fmt.Fprint(writer, "event: reset\ndata: {\"reason\":\"cursor_expired\"}\n\n"); err != nil {
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// runtimeStream 推送运行日志 SSE。游标失效或积压溢出时发 reset，清掉 afterID 后补最近 100 条并继续 live，不断开连接。
func (s *Service) runtimeStream(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	afterID := request.Header.Get("Last-Event-ID")
	if afterID == "" {
		afterID = request.URL.Query().Get("after")
	}
	id, channel := s.subscribeRuntime()
	defer s.unsubscribeRuntime(id)
	if afterID != "" {
		exists, existErr := storage.RuntimeLogExists(s.database, afterID)
		if existErr != nil || !exists {
			if writeRuntimeResetSSE(writer) != nil {
				return
			}
			afterID = ""
		}
	}
	var entries []storage.RuntimeLog
	var err error
	if afterID != "" {
		var overflow bool
		entries, overflow, err = storage.ListRuntimeLogsForStream(s.database, afterID)
		if overflow {
			if writeRuntimeResetSSE(writer) != nil {
				return
			}
			afterID = ""
			entries, _, err = storage.ListRuntimeLogs(s.database, 100, "")
		}
	} else {
		entries, _, err = storage.ListRuntimeLogs(s.database, 100, "")
	}
	if err != nil {
		http.Error(writer, "读取运行日志失败", http.StatusInternalServerError)
		return
	}
	historyIDs := make(map[string]struct{}, len(entries))
	for index := len(entries) - 1; index >= 0; index-- {
		historyIDs[entries[index].ID] = struct{}{}
		if writeRuntimeSSE(writer, entries[index]) != nil {
			return
		}
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	for {
		select {
		case <-request.Context().Done():
			return
		case entry, ok := <-channel:
			if !ok {
				_ = writeRuntimeResetSSE(writer)
				return
			}
			if _, exists := historyIDs[entry.ID]; exists {
				continue
			}
			if err := writeRuntimeSSE(writer, entry); err != nil {
				return
			}
		}
	}
}
