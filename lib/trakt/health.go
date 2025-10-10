package trakt

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Health check intervals
const (
	ShortHealthCheckInterval = 5 * time.Minute
	LongHealthCheckInterval  = 60 * time.Minute
	ExtendedOutageThreshold  = 20 * time.Minute
)

// HealthCheckState represents the adaptive health check mechanism state.
type HealthCheckState struct {
	Mode                string        // "live" | "queue"
	DowntimeSince       time.Time     // When did Trakt first fail?
	NextCheckAt         time.Time     // Scheduled next health check
	ConsecutiveFailures int           // Failed checks since downtime started
	CheckInterval       time.Duration // Current interval (5min or 60min)
	mu                  sync.RWMutex
}

// HealthChecker manages adaptive health checks for Trakt API availability.
type HealthChecker struct {
	state     HealthCheckState
	stateChan chan string // Emits "live" or "queue" on state changes
	trakt     *Trakt      // For authenticated health checks
	mu        sync.RWMutex
}

// NewHealthChecker creates a new health checker.
func NewHealthChecker(trakt *Trakt) *HealthChecker {
	return &HealthChecker{
		state: HealthCheckState{
			Mode:          "live",
			CheckInterval: ShortHealthCheckInterval,
		},
		stateChan: make(chan string, 10), // Buffered to prevent blocking
		trakt:     trakt,
	}
}

// Start begins the health check loop.
// Returns a channel that emits state changes ("live" or "queue").
func (h *HealthChecker) Start(ctx context.Context) <-chan string {
	go h.runHealthCheckLoop(ctx)
	return h.stateChan
}

// runHealthCheckLoop performs periodic health checks with adaptive intervals.
func (h *HealthChecker) runHealthCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(h.NextInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			close(h.stateChan)
			return
		case <-ticker.C:
			isHealthy := h.CheckHealth()
			if isHealthy {
				h.RecordSuccess()
			} else {
				h.RecordFailure()
			}

			// Update ticker with new interval
			ticker.Reset(h.NextInterval())
		}
	}
}

// CheckHealth performs a health check against Trakt API.
// Returns true if Trakt is available, false otherwise.
func (h *HealthChecker) CheckHealth() bool {
	h.mu.RLock()
	trakt := h.trakt
	h.mu.RUnlock()

	if trakt == nil {
		slog.Warn("health check skipped: no Trakt client available")
		return false
	}

	// Use GET /users/settings as health check endpoint (requires auth)
	// This endpoint is lightweight and confirms API availability
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Make a simple request to check API availability
	// We'll implement this method on Trakt client later
	err := trakt.HealthCheck(ctx)
	if err != nil {
		slog.Warn("trakt health check failed",
			"error", err,
			"operation", "health_check_failure",
		)
		return false
	}

	slog.Info("trakt health check succeeded",
		"operation", "health_check_success",
	)
	return true
}

// RecordSuccess updates state after a successful health check.
func (h *HealthChecker) RecordSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()

	previousMode := h.state.Mode
	h.state.Mode = "live"
	h.state.ConsecutiveFailures = 0
	h.state.CheckInterval = ShortHealthCheckInterval
	h.state.NextCheckAt = time.Now().Add(ShortHealthCheckInterval)

	if previousMode == "queue" {
		slog.Info("trakt service restored",
			"operation", "health_check_restored",
			"downtime_duration", time.Since(h.state.DowntimeSince),
		)
		// Emit state change
		select {
		case h.stateChan <- "live":
		default:
			// Channel full, skip (non-blocking)
		}
	}
}

// RecordFailure updates state after a failed health check.
func (h *HealthChecker) RecordFailure() {
	h.mu.Lock()
	defer h.mu.Unlock()

	previousMode := h.state.Mode

	if previousMode == "live" {
		// First failure, enter queue mode
		h.state.Mode = "queue"
		h.state.DowntimeSince = time.Now()
		h.state.ConsecutiveFailures = 1

		slog.Warn("trakt service unavailable, entering queue mode",
			"operation", "health_check_queue_mode",
		)

		// Emit state change
		select {
		case h.stateChan <- "queue":
		default:
			// Channel full, skip (non-blocking)
		}
	} else {
		// Already in queue mode, increment failure count
		h.state.ConsecutiveFailures++
	}

	// Update check interval based on downtime duration
	h.state.CheckInterval = h.calculateInterval()
	h.state.NextCheckAt = time.Now().Add(h.state.CheckInterval)
}

// NextInterval calculates the next health check interval based on downtime duration.
func (h *HealthChecker) NextInterval() time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.calculateInterval()
}

// calculateInterval computes the adaptive interval (caller must hold lock).
func (h *HealthChecker) calculateInterval() time.Duration {
	if h.state.Mode == "live" {
		return ShortHealthCheckInterval
	}

	elapsed := time.Since(h.state.DowntimeSince)
	if elapsed < ExtendedOutageThreshold {
		return ShortHealthCheckInterval // 5 minutes
	}
	return LongHealthCheckInterval // 60 minutes
}

// GetState returns the current health check state.
func (h *HealthChecker) GetState() HealthCheckState {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.state
}
