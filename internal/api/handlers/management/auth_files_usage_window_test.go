package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestListAuthFiles_ExposesSoonestResetUsageWindow(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	reset := time.Now().Add(26 * time.Hour).Truncate(time.Second)
	manager := coreauth.NewManager(nil, nil, nil)
	for _, record := range []*coreauth.Auth{
		{ID: "claude-known", Provider: "claude", Attributes: map[string]string{"runtime_only": "true"},
			Quota: coreauth.QuotaState{ObservedAt: time.Now(), Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(reset.Unix(), 10),
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.24",
			}}},
		{ID: "claude-fresh", Provider: "claude", Attributes: map[string]string{"runtime_only": "true"}},
	} {
		if _, err := manager.Register(context.Background(), record); err != nil {
			t.Fatalf("Register(%s) error = %v", record.ID, err)
		}
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.tokenStore = &memoryAuthStore{}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)

	h.ListAuthFiles(ginCtx)

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	byID := map[string]map[string]any{}
	for _, file := range payload.Files {
		byID[file["id"].(string)] = file
	}
	known := byID["claude-known"]
	if got, _ := time.Parse(time.RFC3339, known["quota_reset_at"].(string)); !got.Equal(reset) {
		t.Fatalf("quota_reset_at = %v, want %v", known["quota_reset_at"], reset.UTC())
	}
	if known["quota_used_percent"] != 24.0 {
		t.Fatalf("quota_used_percent = %v, want 24", known["quota_used_percent"])
	}
	if _, present := byID["claude-fresh"]["quota_reset_at"]; present {
		t.Fatal("a credential without a known window exposed quota_reset_at")
	}
}
