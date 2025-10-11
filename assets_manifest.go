package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type assetManifest struct {
	path     string
	entries  map[string]string
	mu       sync.RWMutex
	isLoaded bool
	modTime  time.Time
}

func newAssetManifest(manifestPath string) *assetManifest {
	m := &assetManifest{
		path: manifestPath,
	}
	if err := m.reload(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Error("failed to load asset manifest", "path", manifestPath, "error", err)
	}
	return m
}

func (m *assetManifest) reload() error {
	info, err := os.Stat(m.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.mu.Lock()
			m.entries = nil
			m.isLoaded = false
			m.modTime = time.Time{}
			m.mu.Unlock()
			return err
		}
		return err
	}

	data, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}

	var entries map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}

	m.mu.Lock()
	m.entries = entries
	m.isLoaded = true
	m.modTime = info.ModTime()
	m.mu.Unlock()
	return nil
}

func (m *assetManifest) pathFor(key string) string {
	prefix := "/static/"

	m.ensureLatest()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.isLoaded {
		return prefix + normalizeAssetKey(key)
	}
	if rel, ok := m.entries[normalizeAssetKey(key)]; ok && rel != "" {
		return prefix + filepath.ToSlash(rel)
	}
	return prefix + normalizeAssetKey(key)
}

func normalizeAssetKey(key string) string {
	normalized := strings.TrimPrefix(key, "/")
	normalized = strings.TrimPrefix(normalized, "static/")
	return filepath.ToSlash(normalized)
}

func (m *assetManifest) ensureLatest() {
	if m == nil {
		return
	}
	info, err := os.Stat(m.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			m.mu.Lock()
			m.entries = nil
			m.isLoaded = false
			m.modTime = time.Time{}
			m.mu.Unlock()
		}
		return
	}

	m.mu.RLock()
	needsReload := !m.isLoaded || info.ModTime().After(m.modTime)
	m.mu.RUnlock()
	if needsReload {
		if err := m.reload(); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Error("failed to refresh asset manifest", "path", m.path, "error", err)
		}
	}
}

func assetPath(key string) string {
	if appAssets != nil {
		return appAssets.pathFor(key)
	}
	return "/static/" + normalizeAssetKey(key)
}
