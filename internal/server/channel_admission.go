package server

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/4xian/oneai-proxy/internal/storage"
)

// channelSkipDecision 描述未创建上游 Attempt 的渠道跳过原因。
type channelSkipDecision struct {
	Reason   string
	InFlight int
	Limit    int
}

// channelLease 表示一次已占用渠道并发名额的准入结果。
type channelLease struct {
	service       *Service
	target        storage.RouteTarget
	channelID     string
	token         uint64
	healthVersion int64
	halfOpen      bool
	inFlight      int
	limit         int
	purpose       storage.AdmissionPurpose
	credential    storedCredential
	sent          atomic.Bool
	sentHook      func()
	once          sync.Once
	result        storage.HealthResult
	err           error
}

// MarkSent 标记该 lease 已经开始真实上游调用。
func (lease *channelLease) MarkSent() {
	if lease != nil && !lease.sent.Swap(true) && lease.sentHook != nil {
		lease.sentHook()
	}
}

// SetSentHook 设置真实上游调用开始时的一次性回调。
func (lease *channelLease) SetSentHook(hook func()) {
	if lease != nil {
		lease.sentHook = hook
	}
}

// WasSent 返回该 lease 是否已经开始真实上游调用。
func (lease *channelLease) WasSent() bool {
	return lease != nil && lease.sent.Load()
}

// releaseUnfinishedLease 在调用方异常退出或未完成显式结算时兜底释放 lease。
func (lease *channelLease) releaseUnfinishedLease() {
	if lease == nil {
		return
	}
	outcome := storage.HealthNotSent
	if lease.WasSent() {
		outcome = storage.HealthNeutralFailure
	}
	_, _ = lease.Finish(outcome, "", 0, time.Now())
}

// Finish 幂等结算健康状态并释放渠道并发名额。
func (lease *channelLease) Finish(outcome storage.HealthOutcome, errorClass string, retryAfter time.Duration, now time.Time) (storage.HealthResult, error) {
	return lease.finish(func() (storage.HealthResult, error) {
		return storage.SettleChannelHealth(lease.service.database, storage.HealthSettleInput{
			ChannelID: lease.channelID, HealthVersion: lease.healthVersion, Purpose: lease.purpose,
			Outcome: outcome, ErrorClass: errorClass, RetryAfter: retryAfter, OccurredAt: now,
		})
	})
}

// FinishAttempt 原子写入业务 Attempt 终态、健康结算和健康路由事件。
func (lease *channelLease) FinishAttempt(attempt storage.AttemptRecord, channelName string, outcome storage.HealthOutcome, errorClass string, retryAfter time.Duration, now time.Time) (storage.HealthResult, error) {
	return lease.finish(func() (storage.HealthResult, error) {
		return storage.SettleAttemptHealth(lease.service.database, attempt, storage.HealthSettleInput{
			ChannelID: lease.channelID, HealthVersion: lease.healthVersion, Purpose: lease.purpose,
			Outcome: outcome, ErrorClass: errorClass, RetryAfter: retryAfter, OccurredAt: now,
		}, channelName)
	})
}

func (lease *channelLease) finish(settle func() (storage.HealthResult, error)) (storage.HealthResult, error) {
	if lease == nil {
		return storage.HealthResult{}, nil
	}
	lease.once.Do(func() {
		defer lease.service.releaseChannelLease(lease)
		lease.result, lease.err = settle()
	})
	return lease.result, lease.err
}

// tryAcquireChannel 非阻塞检查最新渠道状态并领取健康和并发名额。
func (s *Service) tryAcquireChannel(candidate storage.RouteTarget, purpose storage.AdmissionPurpose, now time.Time) (*channelLease, *channelSkipDecision, error) {
	channelID := candidate.ChannelID
	if now.IsZero() {
		now = time.Now()
	}
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	state, err := storage.GetChannelAdmissionState(s.database, channelID)
	if err != nil {
		return nil, nil, err
	}
	if state.AdminState == "disabled" {
		return nil, &channelSkipDecision{Reason: "admin_disabled", InFlight: s.channelInFlight[channelID], Limit: state.ConcurrencyLimit}, nil
	}
	if state.HealthState == "auto_disabled" && purpose == storage.AdmissionBusiness {
		return nil, &channelSkipDecision{Reason: "auto_disabled", InFlight: s.channelInFlight[channelID], Limit: state.ConcurrencyLimit}, nil
	}
	if state.HealthState == "auto_disabled" && purpose == storage.AdmissionAutomaticProbe && (!state.ProbeEnabled || !state.ProbeAutoRecover) {
		return nil, &channelSkipDecision{Reason: "auto_disabled", InFlight: s.channelInFlight[channelID], Limit: state.ConcurrencyLimit}, nil
	}
	if (state.HealthState == "cooldown" || state.HealthState == "half_open") && purpose == storage.AdmissionAutomaticProbe && (!state.ProbeEnabled || !state.ProbeAutoRecover) {
		return nil, &channelSkipDecision{Reason: "cooldown", InFlight: s.channelInFlight[channelID], Limit: state.ConcurrencyLimit}, nil
	}
	needsHalfOpenClaim := state.HealthState == "half_open"
	if state.HealthState == "cooldown" {
		expires, parseErr := time.Parse(time.RFC3339Nano, state.CooldownUntil)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("渠道 %s 冷却截止时间无效: %w", channelID, parseErr)
		}
		if now.Before(expires) {
			return nil, &channelSkipDecision{Reason: "cooldown", InFlight: s.channelInFlight[channelID], Limit: state.ConcurrencyLimit}, nil
		}
		needsHalfOpenClaim = true
	}
	if needsHalfOpenClaim && state.HalfOpenClaimed {
		return nil, &channelSkipDecision{Reason: "half_open_claimed", InFlight: s.channelInFlight[channelID], Limit: state.ConcurrencyLimit}, nil
	}
	inFlight := s.channelInFlight[channelID]
	if inFlight >= state.ConcurrencyLimit {
		return nil, &channelSkipDecision{Reason: "concurrency_full", InFlight: inFlight, Limit: state.ConcurrencyLimit}, nil
	}
	credential, err := s.loadStoredCredential(candidate.SecretRef)
	if err != nil {
		return nil, nil, fmt.Errorf("渠道 %s 凭证不可用: %w", channelID, err)
	}
	healthVersion := state.HealthVersion
	if needsHalfOpenClaim {
		claimedVersion, claimed, claimErr := storage.ClaimChannelHalfOpen(s.database, channelID, state.HealthVersion, now)
		if claimErr != nil {
			return nil, nil, claimErr
		}
		if !claimed {
			return nil, &channelSkipDecision{Reason: "half_open_claimed", InFlight: inFlight, Limit: state.ConcurrencyLimit}, nil
		}
		healthVersion = claimedVersion
	}
	s.channelNext++
	inFlight++
	s.channelInFlight[channelID] = inFlight
	return &channelLease{
		service: s, target: candidate, channelID: channelID, token: s.channelNext, healthVersion: healthVersion,
		halfOpen: needsHalfOpenClaim, inFlight: inFlight, limit: state.ConcurrencyLimit, purpose: purpose, credential: credential,
	}, nil, nil
}

// releaseChannelLease 释放指定 lease 的进程内并发名额。
func (s *Service) releaseChannelLease(lease *channelLease) {
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	current := s.channelInFlight[lease.channelID]
	if current <= 1 {
		delete(s.channelInFlight, lease.channelID)
		return
	}
	s.channelInFlight[lease.channelID] = current - 1
}

// channelConcurrencySnapshot 返回渠道当前在途数和最新并发上限。
func (s *Service) channelConcurrencySnapshot(channelID string) (int, int, error) {
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	state, err := storage.GetChannelAdmissionState(s.database, channelID)
	if err != nil {
		return 0, 0, err
	}
	return s.channelInFlight[channelID], state.ConcurrencyLimit, nil
}
