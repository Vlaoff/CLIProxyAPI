package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// A credential's usage window is only learnt from a response, and signals live in memory. Without
// this file every restart (a nightly update) made all credentials "unknown", so soonest-reset sent
// traffic to whichever answered first, and session affinity then kept whole conversations there.
// The last known window of each credential is kept on disk and used until a response replaces it.

const resetMemoryFile = ".soonest-reset-windows.json"

var resetMemory struct {
	sync.Mutex
	path    string
	loaded  bool
	windows map[string]UsageWindow
	savedAt time.Time
}

// SetSoonestResetStateDir keeps the last known usage window of each credential in dir, so a
// restart does not forget them. An empty dir turns the memory off (the default, and in tests).
func SetSoonestResetStateDir(dir string) {
	path := ""
	if dir = strings.TrimSpace(dir); dir != "" {
		path = filepath.Join(dir, resetMemoryFile)
	}
	resetMemory.Lock()
	defer resetMemory.Unlock()
	if path != resetMemory.path {
		resetMemory.path, resetMemory.loaded, resetMemory.windows = path, false, nil
	}
}

func rememberUsageWindow(id string, window UsageWindow, now time.Time) {
	resetMemory.Lock()
	defer resetMemory.Unlock()
	if id == "" || !loadResetMemoryLocked() {
		return
	}
	previous, known := resetMemory.windows[id]
	resetMemory.windows[id] = window
	// Utilization moves with nearly every response: write when the reset itself changes, else at
	// most once a minute.
	if known && previous.ResetAt.Equal(window.ResetAt) && now.Sub(resetMemory.savedAt) < time.Minute {
		return
	}
	saveResetMemoryLocked(now)
}

func recalledUsageWindow(id string, now time.Time) (UsageWindow, bool) {
	resetMemory.Lock()
	defer resetMemory.Unlock()
	if id == "" || !loadResetMemoryLocked() {
		return UsageWindow{}, false
	}
	window, ok := resetMemory.windows[id]
	if !ok || !window.ResetAt.After(now) {
		return UsageWindow{}, false
	}
	return window, true
}

// loadResetMemoryLocked reports whether the memory is on; an unreadable file starts it empty.
func loadResetMemoryLocked() bool {
	if resetMemory.path == "" {
		return false
	}
	if !resetMemory.loaded {
		resetMemory.loaded, resetMemory.windows = true, map[string]UsageWindow{}
		if data, err := os.ReadFile(resetMemory.path); err == nil {
			if errDecode := json.Unmarshal(data, &resetMemory.windows); errDecode != nil {
				log.Warnf("soonest-reset: ignoring %s: %v", resetMemory.path, errDecode)
				resetMemory.windows = map[string]UsageWindow{}
			}
		}
	}
	return true
}

func saveResetMemoryLocked(now time.Time) {
	resetMemory.savedAt = now
	for id, window := range resetMemory.windows {
		if !window.ResetAt.After(now) {
			delete(resetMemory.windows, id)
		}
	}
	data, err := json.Marshal(resetMemory.windows)
	if err == nil {
		temporary := resetMemory.path + ".tmp"
		if err = os.WriteFile(temporary, data, 0o600); err == nil {
			err = os.Rename(temporary, resetMemory.path)
		}
	}
	if err != nil {
		log.Warnf("soonest-reset: could not save %s: %v", resetMemory.path, err)
	}
}
