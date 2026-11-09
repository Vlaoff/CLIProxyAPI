package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func claudeAuthResettingAt(id string, reset time.Time) *Auth {
	return &Auth{ID: id, Provider: "claude", Quota: QuotaState{
		ObservedAt: time.Now(),
		Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(reset.Unix(), 10),
		},
	}}
}

func pickSoonestReset(t *testing.T, provider string, auths ...*Auth) string {
	t.Helper()
	got, err := (&SoonestResetSelector{}).Pick(context.Background(), provider, "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	return got.ID
}

func TestSoonestResetSelectorPrefersEarliestWeeklyReset(t *testing.T) {
	now := time.Now()
	got := pickSoonestReset(t, "claude",
		claudeAuthResettingAt("a-late", now.Add(6*24*time.Hour)),
		claudeAuthResettingAt("b-soon", now.Add(20*time.Hour)),
		claudeAuthResettingAt("c-mid", now.Add(3*24*time.Hour)),
	)
	if got != "b-soon" {
		t.Fatalf("picked %q, want b-soon", got)
	}
}

func TestSoonestResetSelectorTriesUnknownResetFirst(t *testing.T) {
	now := time.Now()
	got := pickSoonestReset(t, "claude",
		claudeAuthResettingAt("a-known", now.Add(time.Hour)),
		&Auth{ID: "b-never-used", Provider: "claude"},
	)
	if got != "b-never-used" {
		t.Fatalf("picked %q, want b-never-used", got)
	}
}

func TestSoonestResetSelectorTreatsPassedResetAsUnknown(t *testing.T) {
	now := time.Now()
	got := pickSoonestReset(t, "claude",
		claudeAuthResettingAt("a-known", now.Add(time.Hour)),
		claudeAuthResettingAt("b-window-rolled-over", now.Add(-time.Minute)),
	)
	if got != "b-window-rolled-over" {
		t.Fatalf("picked %q, want b-window-rolled-over", got)
	}
}

func TestSoonestResetSelectorFallsBackToRepresentativeClaudeReset(t *testing.T) {
	now := time.Now()
	representative := &Auth{ID: "a-representative", Provider: "claude", Quota: QuotaState{Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-Reset": strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
	}}}
	got := pickSoonestReset(t, "claude", claudeAuthResettingAt("b-weekly", now.Add(2*time.Hour)), representative)
	if got != "a-representative" {
		t.Fatalf("picked %q, want a-representative", got)
	}
}

func TestSoonestResetSelectorSkipsCredentialInCooldown(t *testing.T) {
	now := time.Now()
	exhausted := claudeAuthResettingAt("a-exhausted", now.Add(time.Hour))
	exhausted.Quota.Exceeded = true
	exhausted.Quota.Reason = "credential_quota"
	exhausted.Quota.NextRecoverAt = now.Add(time.Hour)
	got := pickSoonestReset(t, "claude", exhausted, claudeAuthResettingAt("b-next", now.Add(5*24*time.Hour)))
	if got != "b-next" {
		t.Fatalf("picked %q, want b-next", got)
	}
}

func TestSoonestResetSelectorKeepsPriorityTiers(t *testing.T) {
	now := time.Now()
	preferred := claudeAuthResettingAt("a-preferred", now.Add(6*24*time.Hour))
	preferred.Attributes = map[string]string{"priority": "10"}
	got := pickSoonestReset(t, "claude", preferred, claudeAuthResettingAt("b-soon", now.Add(time.Hour)))
	if got != "a-preferred" {
		t.Fatalf("picked %q, want a-preferred", got)
	}
}

func TestSoonestResetSelectorUsesLongestCodexWindow(t *testing.T) {
	now := time.Now()
	codex := func(id string, fiveHourReset, weeklyReset time.Time) *Auth {
		return &Auth{ID: id, Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Window-Minutes":   "300",
			"X-Codex-Primary-Reset-At":         strconv.FormatInt(fiveHourReset.Unix(), 10),
			"X-Codex-Secondary-Window-Minutes": "10080",
			"X-Codex-Secondary-Reset-At":       strconv.FormatInt(weeklyReset.Unix(), 10),
		}}}
	}
	got := pickSoonestReset(t, "codex",
		codex("a-weekly-late", now.Add(10*time.Minute), now.Add(6*24*time.Hour)),
		codex("b-weekly-soon", now.Add(4*time.Hour), now.Add(24*time.Hour)),
	)
	if got != "b-weekly-soon" {
		t.Fatalf("picked %q, want b-weekly-soon", got)
	}
}

func TestSoonestResetSelectorReadsCodexResetAfterSeconds(t *testing.T) {
	now := time.Now()
	relative := &Auth{ID: "a-relative", Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
		"X-Codex-Primary-Window-Minutes":      "10080",
		"X-Codex-Primary-Reset-After-Seconds": "3600",
	}}}
	absolute := &Auth{ID: "b-absolute", Provider: "codex", Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
		"X-Codex-Primary-Window-Minutes": "10080",
		"X-Codex-Primary-Reset-At":       strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10),
	}}}
	if got := pickSoonestReset(t, "codex", absolute, relative); got != "a-relative" {
		t.Fatalf("picked %q, want a-relative", got)
	}
}

func TestSoonestResetSelectorBehindSessionAffinity(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
	manager := NewManager(nil, selector, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	for _, auth := range []*Auth{
		claudeAuthResettingAt("a-late", now.Add(6*24*time.Hour)),
		claudeAuthResettingAt("b-soon", now.Add(24*time.Hour)),
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}
	got, _, err := manager.pickNext(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatalf("pickNext() error = %v", err)
	}
	if got.ID != "b-soon" {
		t.Fatalf("picked %q, want b-soon", got.ID)
	}
}
