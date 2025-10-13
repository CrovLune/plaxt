-- Migration: 008_family_accounts.sql
-- Purpose: Add family account tables and persistent retry queue (FR-010, FR-016)

BEGIN;

-- ============================================================================
-- Family Groups Table
-- ============================================================================
CREATE TABLE IF NOT EXISTS family_groups (
    id VARCHAR(255) PRIMARY KEY,
    plex_username VARCHAR(255) UNIQUE NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_family_groups_plex_username
    ON family_groups(plex_username);

ALTER TABLE family_groups
    ADD CONSTRAINT IF NOT EXISTS check_plex_username_not_empty
    CHECK (LENGTH(TRIM(plex_username)) > 0);

-- Trigger to maintain updated_at column
CREATE OR REPLACE FUNCTION update_family_group_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trigger_update_family_group_timestamp ON family_groups;
CREATE TRIGGER trigger_update_family_group_timestamp
    BEFORE UPDATE ON family_groups
    FOR EACH ROW
    EXECUTE FUNCTION update_family_group_timestamp();

-- ============================================================================
-- Group Members Table
-- ============================================================================
CREATE TABLE IF NOT EXISTS group_members (
    id VARCHAR(255) PRIMARY KEY,
    family_group_id VARCHAR(255) NOT NULL REFERENCES family_groups(id) ON DELETE CASCADE,
    temp_label VARCHAR(100) NOT NULL,
    trakt_username VARCHAR(255),
    access_token TEXT,
    refresh_token TEXT,
    token_expiry TIMESTAMP,
    authorization_status VARCHAR(50) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_group_members_family_group_id
    ON group_members(family_group_id);

ALTER TABLE group_members
    ADD CONSTRAINT IF NOT EXISTS unique_trakt_per_group
    UNIQUE (family_group_id, trakt_username);

ALTER TABLE group_members
    ADD CONSTRAINT IF NOT EXISTS check_temp_label_not_empty
    CHECK (LENGTH(TRIM(temp_label)) > 0);

ALTER TABLE group_members
    ADD CONSTRAINT IF NOT EXISTS check_authorization_status
    CHECK (authorization_status IN ('pending', 'authorized', 'expired', 'failed'));

-- ============================================================================
-- Retry Queue Table (Persistent PostgreSQL Queue)
-- ============================================================================
CREATE TABLE IF NOT EXISTS retry_queue_items (
    id VARCHAR(255) PRIMARY KEY,
    family_group_id VARCHAR(255) NOT NULL REFERENCES family_groups(id) ON DELETE CASCADE,
    group_member_id VARCHAR(255) NOT NULL REFERENCES group_members(id) ON DELETE CASCADE,
    payload JSONB NOT NULL,
    attempt_count SMALLINT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMP NOT NULL,
    last_error TEXT,
    status VARCHAR(50) NOT NULL DEFAULT 'queued',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_retry_queue_due_items
    ON retry_queue_items(status, next_attempt_at);

ALTER TABLE retry_queue_items
    ADD CONSTRAINT IF NOT EXISTS check_attempt_count_range
    CHECK (attempt_count >= 0 AND attempt_count <= 5);

ALTER TABLE retry_queue_items
    ADD CONSTRAINT IF NOT EXISTS check_retry_status
    CHECK (status IN ('queued', 'retrying', 'permanent_failure'));

-- Trigger to maintain updated_at column on retry queue items
CREATE OR REPLACE FUNCTION update_retry_queue_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trigger_update_retry_queue_timestamp ON retry_queue_items;
CREATE TRIGGER trigger_update_retry_queue_timestamp
    BEFORE UPDATE ON retry_queue_items
    FOR EACH ROW
    EXECUTE FUNCTION update_retry_queue_timestamp();

-- ============================================================================
-- Notifications Table (Persistent Banner Notifications)
-- ============================================================================
CREATE TABLE IF NOT EXISTS notifications (
    id VARCHAR(255) PRIMARY KEY,
    family_group_id VARCHAR(255) NOT NULL REFERENCES family_groups(id) ON DELETE CASCADE,
    group_member_id VARCHAR(255) REFERENCES group_members(id) ON DELETE CASCADE,
    notification_type VARCHAR(50) NOT NULL,
    message TEXT NOT NULL,
    metadata JSONB,
    dismissed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notifications_family_group
    ON notifications(family_group_id, dismissed, created_at DESC);

ALTER TABLE notifications
    ADD CONSTRAINT IF NOT EXISTS check_notification_type
    CHECK (notification_type IN ('permanent_failure', 'authorization_expired', 'member_added', 'member_removed'));

COMMIT;

-- Rollback instructions
-- BEGIN;
-- DROP TABLE IF EXISTS notifications;
-- DROP TRIGGER IF EXISTS trigger_update_retry_queue_timestamp ON retry_queue_items;
-- DROP FUNCTION IF EXISTS update_retry_queue_timestamp();
-- DROP TABLE IF EXISTS retry_queue_items;
-- DROP TRIGGER IF EXISTS trigger_update_family_group_timestamp ON family_groups;
-- DROP FUNCTION IF EXISTS update_family_group_timestamp();
-- DROP TABLE IF EXISTS group_members;
-- DROP TABLE IF EXISTS family_groups;
-- COMMIT;
