package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetSelector picks the credential whose usage window resets soonest, so quota that
// would expire unused at its reset is spent before quota that lasts longer. A credential with
// no known future reset (never used since startup, window already rolled over, or a provider
// without reset signals) goes first, because one response reveals its window. Priority tiers
// and cooldowns apply as for the other strategies; ties keep the ID order.
type SoonestResetSelector struct{}

// Pick selects the available auth with the earliest quota reset.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	best, bestReset := available[0], quotaResetAt(available[0], now)
	for _, candidate := range available[1:] {
		if reset := quotaResetAt(candidate, now); resetsSooner(reset, bestReset) {
			best, bestReset = candidate, reset
		}
	}
	return best, nil
}

// resetsSooner orders an unknown (zero) reset before every known one.
func resetsSooner(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() && !b.IsZero()
	}
	return a.Before(b)
}

// quotaResetAt returns when the credential's longest observed usage window resets, or the zero
// time when no future reset is known. The longest window is the one that matters: a five-hour
// window refills by itself within the day, weekly quota left at its reset is lost.
func quotaResetAt(auth *Auth, now time.Time) time.Time {
	if auth == nil {
		return time.Time{}
	}
	signals := auth.Quota.Signals
	var reset time.Time
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		reset = unixSignal(signals, "Anthropic-Ratelimit-Unified-7d-Reset")
		if reset.IsZero() {
			reset = unixSignal(signals, "Anthropic-Ratelimit-Unified-Reset")
		}
	case "codex":
		reset = codexLongestWindowReset(signals, auth.Quota.ObservedAt)
	}
	if !reset.After(now) {
		return time.Time{}
	}
	return reset
}

func codexLongestWindowReset(signals map[string]string, observedAt time.Time) time.Time {
	var reset time.Time
	longest := int64(0)
	for _, window := range []string{"Primary", "Secondary"} {
		minutes, ok := intSignal(signals, "X-Codex-"+window+"-Window-Minutes")
		if !ok || minutes <= longest {
			continue
		}
		at := unixSignal(signals, "X-Codex-"+window+"-Reset-At")
		if seconds, ok := intSignal(signals, "X-Codex-"+window+"-Reset-After-Seconds"); at.IsZero() && ok && seconds > 0 && !observedAt.IsZero() {
			at = observedAt.Add(time.Duration(seconds) * time.Second)
		}
		if !at.IsZero() {
			longest, reset = minutes, at
		}
	}
	return reset
}

func intSignal(signals map[string]string, name string) (int64, bool) {
	value, ok := signals[name]
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed, err == nil
}

func unixSignal(signals map[string]string, name string) time.Time {
	if seconds, ok := intSignal(signals, name); ok && seconds > 0 {
		return time.Unix(seconds, 0)
	}
	return time.Time{}
}
