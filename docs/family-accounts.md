# Family Accounts for Multi-User Scrobbling

## Overview

Plaxt now supports **Family Accounts**, enabling a single Plex username to scrobble to multiple Trakt accounts simultaneously. This feature is perfect for households sharing a single Plex account (like a living room TV) where different family members want to track their viewing on separate Trakt profiles.

## Key Features

- **One-to-Many Scrobbling**: Link 1 Plex username to 2-10 Trakt accounts
- **Individual Authentication**: Each family member logs into their own Trakt account with isolated sessions
- **Graceful Degradation**: If one member's scrobble fails, others continue working
- **Persistent Retry Queue**: Failed scrobbles are automatically retried up to 5 times
- **Admin Management**: Add or remove family members without changing the webhook URL
- **Backward Compatible**: Existing individual accounts continue working unchanged

## Getting Started

### Creating a Family Account

1. **Choose Family Mode**: On the Plaxt landing page, click "Family Account" to start the multi-user setup wizard.

2. **Step 1 - Configure Family Group**:
   - Enter your shared Plex username (e.g., "LivingRoomTV")
   - Add 2-10 member labels (e.g., "Dad", "Mom", "Kid1", "Kid2")
   - These labels are cosmetic and help you identify members during setup

3. **Step 2 - Authorize Each Member**:
   - For each member, click the "Authorize with Trakt" button
   - Each authorization opens in a new window with fresh login (no shared cookies)
   - Log into the appropriate Trakt account for that family member
   - The wizard shows real-time status as each member is authorized

4. **Step 3 - Configure Plex Webhook**:
   - Copy the provided webhook URL (format: `https://your-plaxt.com/api?id=FAMILY_GROUP_ID`)
   - Add this URL to your Plex webhook settings
   - The same URL handles all family member scrobbles

### Managing Family Members

Access the admin panel at `/admin` to:

- **View Family Groups**: See all configured family groups with member counts
- **Add Members**: Add new family members (up to 10 total) without changing the webhook
- **Remove Members**: Remove members who no longer need scrobbling
- **Monitor Status**: Check authorization status and token expiry for each member
- **View Notifications**: See any persistent failures or authorization issues

## Architecture

### System Components

```
┌─────────────┐     Webhook      ┌──────────────┐
│    Plex     │ ───────────────> │    Plaxt     │
│   Server    │                   │   Handler    │
└─────────────┘                   └──────────────┘
                                          │
                                    Broadcast to
                                    All Members
                                          │
                            ┌─────────────┼─────────────┐
                            │             │             │
                      ┌─────▼───┐  ┌─────▼───┐  ┌─────▼───┐
                      │  Trakt  │  │  Trakt  │  │  Trakt  │
                      │ Member1 │  │ Member2 │  │ Member3 │
                      └─────────┘  └─────────┘  └─────────┘
                            │             │             │
                            └─────────────┼─────────────┘
                                          │
                                   ┌──────▼──────┐
                                   │ Retry Queue │
                                   │ (PostgreSQL)│
                                   └─────────────┘
```

### Data Model

- **FamilyGroup**: Links one Plex username to the family account
- **GroupMember**: Individual Trakt accounts within the family
- **RetryQueueItem**: Failed scrobbles queued for retry
- **Notification**: Persistent banner notifications for issues

### Scrobbling Flow

1. Plex sends webhook event to Plaxt
2. Plaxt identifies the family group by webhook ID
3. Fetches all authorized members from database
4. Broadcasts scrobble to all members concurrently
5. Successful scrobbles complete immediately
6. Failed scrobbles (429 rate limit, network errors) enter retry queue
7. Background worker processes retry queue with exponential backoff
8. After 5 failed attempts, marks as permanent failure and notifies owner

### Retry Queue Strategy

- **Persistence**: Queue stored in PostgreSQL, survives restarts
- **Exponential Backoff**: Delays double with each retry (30s, 1m, 2m, 4m, 8m)
- **Maximum Attempts**: 5 retries before permanent failure
- **Concurrent Processing**: Multiple workers can process queue items
- **Cascade Deletion**: Removing a member clears their queued items

## Performance Characteristics

- **Latency**: ≤5 seconds for broadcasting to 10 members (SC-002)
- **Success Rate**: ≥99% eventual success with retry mechanism (SC-003)
- **Onboarding Time**: <10 minutes for complete family setup (SC-001)
- **Concurrent Webhooks**: Sequential processing per Plex ID prevents race conditions

## Telemetry & Monitoring

### Structured Logging

All family scrobbling events include:
- `timestamp`: When the event occurred
- `member_username`: Which Trakt account was affected
- `media_title`: What content was being scrobbled
- `error`: Specific error details if failed
- `event_id`: Unique identifier for correlation

### Onboarding Telemetry

The system tracks:
- `onboarding_start`: When user begins family setup
- `onboarding_complete`: Successful completion with duration
- `onboarding_abandon`: If user doesn't complete within 15 minutes

### Notification System

Banner notifications appear for:
- **Permanent Failures**: When a scrobble fails 5 times
- **Token Expiry**: When a member's Trakt authorization expires
- **Member Changes**: When members are added or removed

## API Endpoints

### Family Onboarding

- `POST /oauth/family/state` - Create family group and start onboarding
- `GET /authorize/family/member` - OAuth callback for member authorization

### Admin Management

- `GET /admin/api/family-groups` - List all family groups
- `GET /admin/api/family-groups/{id}` - Get family group details
- `POST /admin/api/family-groups/{id}/members` - Add new member
- `DELETE /admin/api/family-groups/{id}/members/{member_id}` - Remove member
- `DELETE /admin/api/family-groups/{id}` - Delete entire family group

### Telemetry

- `POST /api/telemetry` - Submit telemetry events

## Database Schema

### Core Tables

```sql
-- Family Groups (one Plex account)
family_groups
├── id (UUID, primary key)
├── plex_username (unique)
├── created_at
└── updated_at

-- Group Members (multiple Trakt accounts)
group_members
├── id (UUID, primary key)
├── family_group_id (foreign key)
├── temp_label (cosmetic name)
├── trakt_username
├── access_token
├── refresh_token
├── token_expiry
├── authorization_status (pending|authorized|expired|failed)
└── created_at

-- Retry Queue (failed scrobbles)
retry_queue_items
├── id (UUID, primary key)
├── family_group_id (foreign key)
├── group_member_id (foreign key)
├── payload (JSONB)
├── attempt_count (0-5)
├── next_attempt_at
├── last_error
├── status (queued|retrying|permanent_failure)
├── created_at
└── updated_at

-- Notifications (persistent banners)
notifications
├── id (UUID, primary key)
├── family_group_id (foreign key)
├── group_member_id (foreign key, nullable)
├── notification_type
├── message
├── metadata (JSONB)
├── dismissed
└── created_at
```

## Troubleshooting

### Common Issues

1. **"Plex username already exists"**: Each Plex username can only have one family group. Delete the existing group or use individual account mode.

2. **"Maximum 10 members reached"**: Family groups are limited to 10 members. Remove inactive members to add new ones.

3. **"Duplicate Trakt account"**: Each Trakt account can only appear once per family group. Use different Trakt accounts for each member.

4. **Scrobbles not working for specific member**:
   - Check authorization status in admin panel
   - Verify token hasn't expired
   - Look for permanent failure notifications
   - Re-authorize the member if needed

5. **High latency on scrobbles**: 
   - Normal for large families (up to 5 seconds for 10 members)
   - Check for members with expired tokens
   - Monitor retry queue depth

### Logs to Check

```bash
# Check for broadcast failures
grep "scrobble broadcast failure" /var/log/plaxt.log

# Monitor retry queue processing
grep "retry_queue" /var/log/plaxt.log

# Track onboarding success
grep "onboarding_complete" /var/log/plaxt.log | grep "mode=family"

# Find permanent failures
grep "permanent_failure" /var/log/plaxt.log
```

## Migration from Individual Accounts

If you're currently using workarounds (like multiple Plaxt instances):

1. **Existing webhooks continue working** - No need to update Plex immediately
2. **Create a family group** with all member Trakt accounts
3. **Update Plex webhook** to the new family group URL
4. **Decommission old instances** once family account is verified working

## Security Considerations

- **Token Isolation**: Each member's Trakt tokens are stored separately
- **No Shared Sessions**: Authorization uses `prompt=login` to prevent cookie sharing
- **Cascade Deletion**: Removing a member deletes their tokens and queued items
- **Admin Access**: Only admin panel can manage family groups

## Best Practices

1. **Use Descriptive Labels**: Choose member labels that clearly identify each person
2. **Monitor Notifications**: Check admin panel regularly for issues
3. **Rotate Tokens**: Re-authorize members if tokens are compromised
4. **Limit Membership**: Only add active users to minimize broadcast overhead
5. **Test with One Member**: Verify scrobbling works before adding all members

## Future Enhancements

Planned improvements:
- Email notifications for permanent failures
- Bulk member management
- Usage statistics per member
- Automatic token refresh before expiry
- Member-specific scrobbling rules/filters

## Support

For issues or questions:
1. Check the troubleshooting section above
2. Review logs for error messages
3. Open an issue on GitHub with:
   - Plaxt version
   - Number of family members
   - Error messages from logs
   - Steps to reproduce