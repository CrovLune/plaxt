-- Migration: Add queue table for Trakt offline queue system
-- Feature: 006-add-a-queueing
-- Date: 2025-10-10

-- Create queued_scrobbles table
CREATE TABLE IF NOT EXISTS queued_scrobbles (
    id           UUID PRIMARY KEY,
    user_id      VARCHAR(255) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scrobble_body JSONB NOT NULL,
    action       VARCHAR(10) NOT NULL CHECK (action IN ('start', 'pause', 'stop')),
    progress     INTEGER NOT NULL CHECK (progress >= 0 AND progress <= 100),
    created_at   TIMESTAMP NOT NULL,
    retry_count  INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0 AND retry_count <= 5),
    last_attempt TIMESTAMP,
    player_uuid  VARCHAR(255) NOT NULL,
    rating_key   VARCHAR(255) NOT NULL,
    CONSTRAINT queued_scrobbles_dedup UNIQUE (player_uuid, rating_key)
);

-- Index for efficient chronological queue queries per user
CREATE INDEX IF NOT EXISTS idx_queued_scrobbles_user_time ON queued_scrobbles(user_id, created_at);

-- Partial index for tracking events with retry failures
CREATE INDEX IF NOT EXISTS idx_queued_scrobbles_retry ON queued_scrobbles(user_id, retry_count) WHERE retry_count > 0;

-- Add comment for documentation
COMMENT ON TABLE queued_scrobbles IS 'Stores scrobble events awaiting transmission to Trakt during API outages';
COMMENT ON COLUMN queued_scrobbles.id IS 'UUID v4 generated on enqueue';
COMMENT ON COLUMN queued_scrobbles.scrobble_body IS 'JSON-serialized ScrobbleBody with media metadata';
COMMENT ON COLUMN queued_scrobbles.created_at IS 'Original webhook receipt time for chronological ordering';
COMMENT ON COLUMN queued_scrobbles.retry_count IS 'Number of send attempts (0-5), events purged after 5 failures';
COMMENT ON CONSTRAINT queued_scrobbles_dedup ON queued_scrobbles IS 'Prevents duplicate enqueue of same media item for same player';
