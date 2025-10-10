# Changelog

All notable changes to this project will be documented in this file.

This fork continues the work of [XanderStrike's goplaxt](https://github.com/XanderStrike/goplaxt) with significant enhancements to the user experience, storage backends, and operational capabilities.

## [Unreleased]

### Added (2025-10-10)
- **Admin Dashboard** - Comprehensive user management interface:
  - Full-featured admin dashboard at `/admin` with real-time user monitoring
  - Dashboard statistics: Total users, Healthy, Warning, and Expired token counts
  - User management table with sortable columns and hover effects
  - Color-coded token status indicators (green/orange/red based on token age)
  - Edit user modal: Update Plex username and Trakt display name
  - Delete user modal: Confirmation dialog with safety warnings
  - Auto-refresh every 30 seconds for real-time monitoring
  - Responsive design optimized for desktop and mobile devices
  - Empty state UI when no users exist
  - Success/error notification system
- **Admin API endpoints** for user management:
  - `GET /admin` - Serves admin dashboard HTML
  - `GET /admin/api/users` - List all users with token status and metadata
  - `GET /admin/api/users/{id}` - Get detailed user information
  - `PUT /admin/api/users/{id}` - Update user (username, display name)
  - `DELETE /admin/api/users/{id}` - Delete user from storage
- **Admin link in main UI**:
  - Elegant admin access button in hero section (top-right corner)
  - Glassmorphic design matching existing UI aesthetic
  - Users icon with "Admin" label (icon-only on mobile)
  - Smooth hover animations and transitions
- **Token status monitoring system**:
  - Automatic calculation of token age in hours
  - Status classification: Healthy (<20hrs), Warning (20-24hrs), Expired (≥24hrs)
  - Real-time status updates with color-coded visual indicators
  - Token age display in human-readable format (hours/days)
- **User model enhancements**:
  - Added `UpdateUsername()` method for safe username updates
  - Username normalization to lowercase for consistency
  - Validation and sanitization for all user inputs
- **Optimized CircleCI CI/CD pipeline** with three workflows:
  - Automated build and test on every commit with race detection and coverage reports
  - Nightly builds (scheduled daily at 2 AM UTC) with automatic versioning (`nightly-YYYYMMDD-commit`)
  - Manual release workflow with approval gate for production deployments
- **Dual caching strategy** for CircleCI:
  - Go module cache (keyed by `go.sum`)
  - Go build cache (keyed by branch and commit)
  - Results in 75% faster cached builds (30-45s vs 2-3min)
- **Version injection system** using Docker build arguments:
  - `VERSION`, `COMMIT`, and `DATE` injected into binary via Go linker flags
  - Version info displayed in application logs on startup
- **Multi-architecture Docker builds** via CircleCI:
  - Automated builds for linux/amd64 and linux/arm64
  - Docker Buildx integration with layer caching
- **Semantic versioning support**:
  - Tag-based releases (e.g., `v1.2.16`)
  - Automatic creation of multiple Docker tags: `latest`, `1.2.16`, `1.2`
  - Nightly tags for testing: `nightly` and `nightly-YYYYMMDD-commit`
- **Comprehensive CI/CD documentation**:
  - `.circleci/README.md` - Complete guide with troubleshooting and performance benchmarks
  - `RELEASING.md` - Quick reference for creating releases with checklists
  - Documentation for versioning strategy and Docker Hub integration

### Fixed (2025-10-10)
- Fixed manual renewal flow UI issue where incident ID and "Start Over" button incorrectly appeared at step 1 after canceling Trakt authorization
- Banner actions (incident ID, Start Over button) now only display when on the result step (step 3)

### Changed (2025-10-10)
- **CircleCI configuration completely rewritten** for performance and reliability:
  - Updated from basic test-only workflow to full CI/CD pipeline
  - Migrated to machine executor for Docker builds (better performance)
  - Added test result and coverage artifact storage
  - Implemented parallel job execution where possible
  - Added manual approval gates for production releases
- **Dockerfile enhanced** with build argument support:
  - Accepts `VERSION`, `COMMIT`, and `DATE` build args
  - Updated build command to inject version info via ldflags
  - Maintains existing multi-stage build optimization
- **Docker build context optimized**:
  - Enhanced `.dockerignore` to exclude unnecessary files
  - Reduced build context size to ~2.35 MB
  - Improved layer caching efficiency
- Enhanced `.gitignore` to comprehensively exclude:
  - AI assistant files (`.claude/`, `.codex/`, `.cursor/`, `.specify/`, `specs/`, `AGENTS.md`, `CLAUDE.md`)
  - Build artifacts and test binaries (`plaxt-test`, `*.test`, `*.out`)
  - Go build caches (`.gocache/`, `.gomodcache/`)
  - IDE files (`.idea/`, `.vscode/`, editor swap files)
  - System files (`.DS_Store`)
  - Logs and environment files

### Removed (2025-10-10)
- Cleaned up AI assistant files and development artifacts from repository
- Removed redundant files from `plexhooks/` subdirectory:
  - `.circleci/` (CI handled at project root)
  - `.gitignore` (covered by main .gitignore)
  - `README.md` (internal package documentation)
- Removed all `.DS_Store` files (macOS system files)

## [1.2.15] - 2025-10-10

### Fixed
- Manual renewal banner now correctly displays incident ID and "Start Over" button only on step 3 (result screen)
- Prevented UI clutter when canceling authorization and returning to step 1

## [1.2.14] - 2025-10-10

### Added
- Multi-architecture Docker image support (linux/amd64, linux/arm64)
- Published to Docker Hub as `crovlune/plaxt:latest`

---

## Major Differences from Original Plaxt (XanderStrike/goplaxt)

This fork represents a significant evolution of the original Plaxt project. Below are the key differentiators:

### 🎨 Complete UI Overhaul

**Modern Guided Onboarding Flow**
- Redesigned with 2025 aesthetics featuring glassmorphism styling
- Three-step wizard interface with clear visual progress indicators
- Responsive layout optimized for desktop and mobile devices
- Real-time validation and user-friendly error messages
- Accessible design with proper ARIA labels and keyboard navigation

**Manual Token Renewal Interface**
- NEW: Dedicated manual renewal workflow that doesn't require re-entering usernames
- User selection dropdown showing all registered Plaxt users with last refresh timestamps
- Automatic display name fetching from Trakt API after authorization
- Optional manual display name entry for disambiguation
- Preserves existing webhook URLs—no need to reconfigure Plex
- State persistence across page refreshes using localStorage
- Correlation IDs for tracking renewal attempts in logs

**Enhanced Visual Design**
- Custom glassmorphic components with backdrop blur effects
- Color-coded status indicators (success, error, warning, info)
- Smooth animations and transitions
- Professional typography and spacing
- Custom SVG icons for actions
- Improved readability with better contrast ratios

**Admin Dashboard**
- NEW: Comprehensive user management interface at `/admin`
- Real-time dashboard with token status monitoring
- User statistics: Total, Healthy, Warning, Expired counts
- Full CRUD operations: View, Edit, Delete users
- Color-coded token status (green/orange/red based on age)
- Modal-based editing with validation
- Auto-refresh every 30 seconds
- Accessible via admin link in main UI
- Designed for use behind authentication middleware (e.g., Authentik)

### 🗄️ Multiple Storage Backend Support

**Flexible Data Persistence**
- **Disk storage** (default): JSON files in `/app/keystore` directory
- **Redis**: Full support with connection pooling and automatic reconnection
- **PostgreSQL**: Production-grade SQL storage with automatic schema migrations
- Automatic column addition for `trakt_display_name` field
- Consistent API across all storage backends
- Thread-safe operations with proper locking mechanisms

### 🔐 Enhanced Token Management

**Display Name Tracking**
- Stores Trakt display names alongside Plex usernames
- Automatic fetching via Trakt API after authorization
- Manual entry option when API fetch fails
- 50-character limit with truncation warnings
- Helps disambiguate duplicate Plex usernames across different Trakt accounts
- Display names shown in dropdown labels (e.g., "john_doe (John Smith) • refreshed 2 hours ago")

**Automatic Token Refresh**
- Proactive refresh for tokens older than 23 hours during webhook handling
- Prevents token expiration interrupting scrobble operations
- Maintains seamless sync even during long periods of inactivity

### 📊 Operational Improvements

**Structured Logging**
- Comprehensive logging with correlation IDs for tracking flows
- Structured log format for manual renewal operations: `[MANUAL_RENEWAL] result=... correlation_id=... ...`
- Detailed error context including HTTP status codes and Trakt error types
- Separate logging for onboarding vs. manual renewal flows
- Easier troubleshooting with incident ID display in UI

**Better Error Handling**
- User-friendly error messages with actionable guidance
- Detailed server-side error logging with context
- Graceful degradation when services unavailable
- Proper error state management in multi-step flows
- Authorization cancellation handling with clear messaging

**Environment Configuration**
- Support for multiple hostname formats in `ALLOWED_HOSTNAMES`
- Flexible Redis URL configuration (`REDIS_URL` or `REDIS_URI` + `REDIS_PASSWORD`)
- PostgreSQL connection string support with SSL options
- Backward-compatible with existing deployments

### 🏗️ Code Architecture

**Modular Package Structure**
- `lib/store/`: Abstract storage interface with multiple implementations
- `lib/trakt/`: Trakt API client with display name fetching
- `lib/common/`: Shared utilities and data structures
- `lib/config/`: Configuration management
- `plexhooks/`: In-tree webhook parser (imported from XanderStrike/plexhooks)

**Improved Testing**
- Comprehensive test coverage for storage backends
- Test fixtures for Plex webhook parsing (movie, TV, music)
- Unit tests for user management and display name handling
- Mock interfaces for external dependencies

**Container Optimizations**
- Multi-stage Docker builds for minimal image size
- Support for multiple architectures (amd64, arm64, arm/v7)
- Optimized layer caching
- Comprehensive `.dockerignore` for lean build contexts (~2.35 MB)

### 🔄 OAuth Flow Enhancements

**Dual Authorization Endpoints**
- `/authorize` - Onboarding flow for new users
- `/manual/authorize` - Token renewal flow for existing users
- State management with mode detection
- Correlation ID tracking through entire OAuth flow
- Automatic redirection to appropriate step after callback

**Cancellation Handling**
- Graceful handling of denied authorizations
- Informative messages about unchanged state
- Return to appropriate step in flow
- No data corruption on cancellation

### 📦 Deployment Features

**Docker First**
- Official multi-architecture images on Docker Hub
- Volume mounting for persistent storage
- Environment-based configuration
- Health check support
- Minimal base image (scratch) for security

**Example Configurations**
- `.env.example` with all required variables documented
- `docker-compose.example.yml` for quick deployment
- Support for various hosting environments
- Clear documentation for Kubernetes/Nomad adaptation

### 🎯 User Experience Improvements

**Onboarding Flow**
1. Enter Plex username once
2. Review and authorize with Trakt
3. Copy webhook URL to Plex settings

**Manual Renewal Flow**
1. Select user from dropdown (no typing needed)
2. Confirm webhook details
3. Authorize with Trakt to refresh tokens

**Key UX Wins**
- No username re-entry required for renewals
- Webhook URLs remain stable—no Plex reconfiguration
- Visual feedback at every step
- Clear success/error states
- Contextual help and documentation links
- Copy-to-clipboard functionality for webhook URLs

### 🔒 Privacy & Security

**Minimal Data Collection**
- Only stores OAuth tokens and display names
- No telemetry or analytics
- No external service calls except Trakt API
- Local-first architecture
- Optional display names (can be left empty)

### 📝 Documentation

**Comprehensive README**
- Quick start guide with Docker examples
- Environment variable reference
- Storage backend configuration
- Developer guide for contributing
- Multi-architecture build instructions
- Operational notes for token management

### 🙏 Attribution

This fork maintains proper attribution to:
- Original Plaxt creator: [XanderStrike](https://github.com/XanderStrike)
- Original plexhooks parser: [XanderStrike/plexhooks](https://github.com/XanderStrike/plexhooks)

---

## Version History

The version numbering continues from the original project with this fork maintaining semantic versioning:

- `1.2.x` - Current fork with UI overhaul and storage backends
- `1.1.x` - Original goplaxt releases (XanderStrike)
- `1.0.x` - Initial goplaxt releases (XanderStrike)

## Contributing

We welcome contributions! See the README for guidelines on:
- Code formatting (gofmt)
- Running tests (`go test ./...`)
- Building multi-platform Docker images
- Submitting pull requests

---

**Happy syncing!** 🎬→📚
