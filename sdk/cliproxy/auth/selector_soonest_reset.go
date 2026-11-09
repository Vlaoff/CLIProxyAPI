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
	best, bestReset := available[0], usageResetAt(available[0], now)
	for _, candidate := range available[1:] {
		if reset := usageResetAt(candidate, now); resetsSooner(reset, bestReset) {
			best, bestReset = candidate, reset
		}
	}
	return best, nil
}

func usageResetAt(auth *Auth, now time.Time) time.Time {
	window, _ := LongestUsageWindow(auth, now)
	return window.ResetAt
}

// resetsSooner orders an unknown (zero) reset before every known one.
func resetsSooner(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() && !b.IsZero()
	}
	return a.Before(b)
}

// UsageWindow is the usage window soonest-reset routing ranks a credential by.
type UsageWindow struct {
	ResetAt time.Time
	// UsedPercent is 0-100, or -1 when the provider did not report it.
	UsedPercent float64
}

// LongestUsageWindow returns the credential's longest observed usage window that resets in the
// future. The longest window is the one that matters: a five-hour window refills by itself within
// the day, weekly quota left at its reset is lost. ok is false when no future reset is known.
func LongestUsageWindow(auth *Auth, now time.Time) (window UsageWindow, ok bool) {
	if auth == nil {
		return UsageWindow{}, false
	}
	signals := auth.Quota.Signals
	window.UsedPercent = -1
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		if window.ResetAt = unixSignal(signals, "Anthropic-Ratelimit-Unified-7d-Reset"); !window.ResetAt.IsZero() {
			if used, ok := floatSignal(signals, "Anthropic-Ratelimit-Unified-7d-Utilization"); ok {
				window.UsedPercent = used * 100
			}
		} else {
			window.ResetAt = unixSignal(signals, "Anthropic-Ratelimit-Unified-Reset")
		}
	case "codex":
		window = codexLongestWindow(signals, auth.Quota.ObservedAt)
	}
	if !window.ResetAt.After(now) {
		return UsageWindow{}, false
	}
	return window, true
}

func codexLongestWindow(signals map[string]string, observedAt time.Time) UsageWindow {
	window := UsageWindow{UsedPercent: -1}
	longest := int64(0)
	for _, name := range []string{"Primary", "Secondary"} {
		minutes, ok := intSignal(signals, "X-Codex-"+name+"-Window-Minutes")
		if !ok || minutes <= longest {
			continue
		}
		at := unixSignal(signals, "X-Codex-"+name+"-Reset-At")
		if seconds, ok := intSignal(signals, "X-Codex-"+name+"-Reset-After-Seconds"); at.IsZero() && ok && seconds > 0 && !observedAt.IsZero() {
			at = observedAt.Add(time.Duration(seconds) * time.Second)
		}
		if at.IsZero() {
			continue
		}
		longest, window.ResetAt, window.UsedPercent = minutes, at, -1
		if used, ok := floatSignal(signals, "X-Codex-"+name+"-Used-Percent"); ok {
			window.UsedPercent = used
		}
	}
	return window
}

func intSignal(signals map[string]string, name string) (int64, bool) {
	value, ok := signals[name]
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed, err == nil
}

func floatSignal(signals map[string]string, name string) (float64, bool) {
	value, ok := signals[name]
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed, err == nil
}

func unixSignal(signals map[string]string, name string) time.Time {
	if seconds, ok := intSignal(signals, name); ok && seconds > 0 {
		return time.Unix(seconds, 0)
	}
	return time.Time{}
}
