package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/storage"
)

const probeSchedulerInterval = 10 * time.Second

// runProbeScheduler 按探针策略定时执行检查，并限制同一渠道重叠运行。
func (s *Service) runProbeScheduler(ctx context.Context) {
	ticker := time.NewTicker(probeSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dataMu.RLock()
			policies, err := storage.ListProbePolicies(s.database)
			s.dataMu.RUnlock()
			if err != nil {
				s.logger.Warn("读取定时探针策略失败", "error", err)
				continue
			}
			for _, policy := range policies {
				if !policy.Enabled || !s.probeDue(policy) {
					continue
				}
				policy := policy
				go func() {
					// 每个渠道增加短暂随机抖动，避免同一时刻集中访问上游。
					time.Sleep(time.Duration(rand.Int63n(5000)) * time.Millisecond)
					if _, err := s.executeProbe(ctx, policy.ChannelID, false, ""); err != nil {
						s.logger.Warn("定时探针执行失败", "channel", policy.ChannelID, "error", err)
					}
				}()
			}
		}
	}
}

// executeProbe 执行一次连通性或最小推理探针并写入独立运行记录。
func (s *Service) executeProbe(parent context.Context, channelID string, manual bool, modelOverride string) (storage.ProbeRun, error) {
	s.migrationBarrierMu.RLock()
	defer s.migrationBarrierMu.RUnlock()
	return s.executeProbeLocked(parent, channelID, manual, modelOverride)
}

// executeProbeLocked 在领取渠道 lease 前读取一致配置快照，再执行可能耗时的上游请求。
func (s *Service) executeProbeLocked(parent context.Context, channelID string, manual bool, modelOverride string) (storage.ProbeRun, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return storage.ProbeRun{}, fmt.Errorf("渠道 ID 不能为空")
	}
	if !s.claimProbe(channelID) {
		return storage.ProbeRun{}, fmt.Errorf("渠道探针正在执行: %s", channelID)
	}
	defer s.releaseProbe(channelID)
	select {
	case s.probeSlots <- struct{}{}:
		defer func() { <-s.probeSlots }()
	case <-parent.Done():
		return storage.ProbeRun{}, parent.Err()
	}
	// 在最终领取渠道 lease 前重新读取配置，确保探针请求体与当前健康版本一致。
	s.dataMu.RLock()
	policy, err := storage.GetProbePolicy(s.database, channelID)
	if err != nil {
		s.dataMu.RUnlock()
		return storage.ProbeRun{}, err
	}
	target, targetErr := storage.GetProbeTarget(s.database, channelID)
	if targetErr != nil {
		s.dataMu.RUnlock()
		return storage.ProbeRun{}, targetErr
	}
	if !manual && !policy.Enabled {
		s.dataMu.RUnlock()
		return storage.ProbeRun{}, fmt.Errorf("渠道探针未启用: %s", channelID)
	}
	if !manual && policy.AutoRecover && !policy.Enabled {
		s.dataMu.RUnlock()
		return storage.ProbeRun{}, fmt.Errorf("自动恢复探针未启用: %s", channelID)
	}
	if strings.TrimSpace(modelOverride) != "" {
		target.UpstreamModel = strings.TrimSpace(modelOverride)
	} else if strings.TrimSpace(policy.Model) != "" {
		target.UpstreamModel = strings.TrimSpace(policy.Model)
	}
	purpose := storage.AdmissionAutomaticProbe
	if manual {
		purpose = storage.AdmissionManualProbe
	}
	lease, skip, err := s.tryAcquireChannel(target, purpose, time.Now())
	s.dataMu.RUnlock()
	s.emitRuntime("info", "probe.started", "渠道探针开始", map[string]any{"channelID": channelID, "manual": manual, "mode": policy.Mode})
	if lease != nil {
		defer lease.releaseUnfinishedLease()
	}
	if err != nil {
		return storage.ProbeRun{}, err
	}
	if skip != nil {
		if skip.Reason == "concurrency_full" {
			s.emitRuntime("info", "probe.skipped_busy", "渠道探针因并发已满跳过", map[string]any{"channelID": channelID, "manual": manual, "inFlight": skip.InFlight, "concurrencyLimit": skip.Limit})
			runID, idErr := storage.NewProbeRunID()
			if idErr != nil {
				return storage.ProbeRun{}, idErr
			}
			run := storage.ProbeRun{ID: runID, ChannelID: channelID, Mode: policy.Mode, Status: "skipped_busy", ErrorClass: "concurrency_full", ErrorMessage: "渠道并发已满，探针跳过", OccurredAt: storage.FormatSQLiteTime(time.Now())}
			if recordErr := storage.RecordProbeRun(s.database, run); recordErr != nil {
				s.emitRuntime("error", "database.probe_record_failed", "探针跳过记录写入失败", map[string]any{"channelID": channelID, "error": recordErr.Error()})
				return storage.ProbeRun{}, recordErr
			}
			return run, nil
		}
		return storage.ProbeRun{}, fmt.Errorf("渠道当前不可执行探针: %s (%s)", channelID, skip.Reason)
	}
	// 定时探针准入成功后再刷新周期，避免跳过或领取失败空等整个 interval。
	if !manual {
		s.markProbeLast(channelID)
	}
	target = lease.target
	started := time.Now()
	probeErr := s.sendProbe(parent, target, policy, lease)
	outcome := storage.HealthSuccess
	if !lease.WasSent() {
		outcome = storage.HealthNotSent
	} else if probeErr != nil {
		outcome = storage.HealthNeutralFailure
		if shouldCountHealthFailure(probeErr) {
			outcome = storage.HealthFailure
		}
	}
	errorClass := classifyProxyError(probeErr, parent.Err())
	healthResult, healthErr := lease.Finish(outcome, errorClass, retryAfterDuration(probeErr, time.Now()), time.Now())
	if healthErr != nil {
		s.emitRuntime("error", "database.probe_health_update_failed", "探针健康状态结算失败", map[string]any{"channelID": channelID, "error": healthErr.Error()})
		return storage.ProbeRun{}, healthErr
	}
	if healthResult.Applied {
		s.emitRuntime("info", "health.changed", "探针已更新渠道健康状态", map[string]any{"channelID": channelID, "fromState": healthResult.FromState, "toState": healthResult.ToState, "failureCountBefore": healthResult.FailureCountBefore, "failureCountAfter": healthResult.FailureCountAfter, "probeFailureCountBefore": healthResult.ProbeFailureCountBefore, "probeFailureCountAfter": healthResult.ProbeFailureCountAfter, "probeSuccessCountBefore": healthResult.ProbeSuccessCountBefore, "probeSuccessCountAfter": healthResult.ProbeSuccessCountAfter, "healthVersion": healthResult.HealthVersion, "cooldownUntil": healthResult.CooldownUntil})
	}
	runID, idErr := storage.NewProbeRunID()
	if idErr != nil {
		return storage.ProbeRun{}, idErr
	}
	run := storage.ProbeRun{ID: runID, ChannelID: channelID, Mode: policy.Mode, Status: "success", Latency: time.Since(started), OccurredAt: storage.FormatSQLiteTime(time.Now())}
	if probeErr != nil {
		run.Status = "failed"
		run.ErrorClass = errorClass
		run.ErrorMessage = probeErr.Error()
		s.emitRuntime("warn", "probe.failed", "渠道探针失败", map[string]any{"channelID": channelID, "errorClass": run.ErrorClass, "error": run.ErrorMessage, "status": run.Status})
	} else {
		s.emitRuntime("info", "probe.succeeded", "渠道探针成功", map[string]any{"channelID": channelID, "status": run.Status})
	}
	if recordErr := storage.RecordProbeRun(s.database, run); recordErr != nil {
		s.emitRuntime("error", "database.probe_record_failed", "探针记录写入失败", map[string]any{"channelID": channelID, "error": recordErr.Error()})
		return run, recordErr
	}
	s.emitRuntime("info", "probe.completed", "渠道探针完成", map[string]any{"channelID": channelID, "status": run.Status, "latencyMs": run.Latency.Milliseconds()})
	return run, nil
}

// claimProbe 领取渠道级探针执行锁，防止定时探针和手动探针重叠。
func (s *Service) claimProbe(channelID string) bool {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probeRun == nil {
		s.probeRun = make(map[string]bool)
	}
	if s.probeRun[channelID] {
		return false
	}
	s.probeRun[channelID] = true
	return true
}

// releaseProbe 释放渠道级探针执行锁。
func (s *Service) releaseProbe(channelID string) {
	s.probeMu.Lock()
	delete(s.probeRun, channelID)
	s.probeMu.Unlock()
}

// probeDue 只判断渠道定时探针是否到期，不刷新最近执行时间。
func (s *Service) probeDue(policy storage.ProbePolicy) bool {
	now := time.Now()
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	last, exists := s.probeLast[policy.ChannelID]
	interval := time.Duration(policy.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	if exists && now.Sub(last) < interval {
		return false
	}
	return true
}

// markProbeLast 在探针真正开始执行时记录时间，claim 失败或准入跳过不得调用。
func (s *Service) markProbeLast(channelID string) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.probeLast == nil {
		s.probeLast = make(map[string]time.Time)
	}
	s.probeLast[channelID] = time.Now()
}

// sendProbe 按渠道协议发起低成本探针请求。
func (s *Service) sendProbe(parent context.Context, target storage.RouteTarget, policy storage.ProbePolicy, lease *channelLease) error {
	return s.sendProbeWithCredential(parent, target, policy, &lease.credential, lease)
}

// sendProbeWithCredential 按可选的内存凭证执行探针，供未保存渠道即时测试使用。
func (s *Service) sendProbeWithCredential(parent context.Context, target storage.RouteTarget, policy storage.ProbePolicy, credential *storedCredential, leases ...*channelLease) error {
	mode := policy.Mode
	timeout := time.Duration(policy.RequestTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	method := http.MethodGet
	endpoint := ""
	var body []byte
	customPath := strings.TrimSpace(policy.Path)
	if customPath == "" {
		customPath = "/v1/models"
	}
	if mode == "minimal_inference" {
		method = http.MethodPost
		var err error
		if customPath == "/v1/models" {
			switch target.Protocol {
			case storage.ProtocolOpenAIChat:
				endpoint, err = joinUpstreamURL(target.BaseURL, "/v1/chat/completions", "")
			case storage.ProtocolOpenAIResponses:
				endpoint, err = joinUpstreamURL(target.BaseURL, "/v1/responses", "")
			case storage.ProtocolAnthropicMessages:
				endpoint, err = joinUpstreamURL(target.BaseURL, "/v1/messages", "")
			default:
				return fmt.Errorf("不支持的探针协议: %s", target.Protocol)
			}
		} else {
			endpoint, err = joinUpstreamURL(target.BaseURL, customPath, "")
		}
		if err != nil {
			return err
		}
		switch target.Protocol {
		case storage.ProtocolOpenAIChat:
			body, _ = json.Marshal(map[string]any{"model": target.UpstreamModel, "messages": []map[string]string{{"role": "user", "content": "ping"}}, "max_tokens": 1})
		case storage.ProtocolOpenAIResponses:
			body, _ = json.Marshal(map[string]any{"model": target.UpstreamModel, "input": "ping", "max_output_tokens": 1})
		case storage.ProtocolAnthropicMessages:
			body, _ = json.Marshal(map[string]any{"model": target.UpstreamModel, "max_tokens": 1, "messages": []map[string]string{{"role": "user", "content": "ping"}}})
		}
	} else {
		var err error
		endpoint, err = joinUpstreamURL(target.BaseURL, customPath, "")
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range target.CustomHeaders {
		if strings.TrimSpace(name) != "" && !strings.ContainsAny(name+value, "\r\n") {
			request.Header.Set(name, value)
		}
	}
	applyProtocolRequestHeaders(request, target.Protocol)
	appliedCredential := credential
	if appliedCredential != nil {
		if err := applyCredentialValue(request, *appliedCredential); err != nil {
			return err
		}
	} else {
		stored, err := s.loadStoredCredential(target.SecretRef)
		if err != nil {
			return err
		}
		if err := applyCredentialValue(request, stored); err != nil {
			return err
		}
		appliedCredential = &stored
	}
	ensureAnthropicAPIKeyHeader(request, target.Protocol, appliedCredential)
	var lease *channelLease
	if len(leases) > 0 {
		lease = leases[0]
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease != nil {
		// 本地准备完成后、client.Do 前同步标记传输开始；DNS/TCP/TLS 失败也计为探针 Attempt。
		lease.MarkSent()
	}
	response, err := (&http.Client{Timeout: timeout, CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	const probeBodyLimit int64 = 1 << 20
	body, err = io.ReadAll(io.LimitReader(response.Body, probeBodyLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("读取探针响应失败: %w", err)
	}
	if int64(len(body)) > probeBodyLimit {
		return errors.New("探针响应超过大小上限")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newProxyUpstreamError(target.Protocol, response.StatusCode, response.Header.Clone(), body)
	}
	if mode == "minimal_inference" && !validJSONResponse(body, target.Protocol) {
		return errors.New("最小推理探针响应不符合目标协议")
	}
	return nil
}
