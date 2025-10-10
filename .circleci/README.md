# CircleCI Configuration Guide

This document explains the optimized CircleCI setup for the Plaxt project.

## Overview

The CircleCI configuration provides:
- **Automated testing** on every commit
- **Nightly Docker builds** with automatic versioning
- **Manual release workflow** for production deployments
- **Multi-architecture support** (amd64, arm64)
- **Optimized caching** for fast builds

## Workflows

### 1. Build & Test (Automatic)

Runs on **every commit** to any branch.

**Steps:**
1. Checkout code
2. Restore Go module cache
3. Restore Go build cache
4. Download and verify Go modules
5. Run tests with race detection and coverage
6. Save caches
7. Store test results and coverage reports

**Performance optimizations:**
- Dual caching (modules + build artifacts)
- Race detection for concurrency bugs
- Coverage reports stored as artifacts

### 2. Nightly Builds (Scheduled)

Runs **daily at 2 AM UTC** on `main`/`master` branch.

**Steps:**
1. Run full test suite
2. Build multi-arch Docker images
3. Push to Docker Hub with tags:
   - `crovlune/plaxt:nightly`
   - `crovlune/plaxt:nightly-YYYYMMDD-<commit>`

**Version format:**
```
nightly-20251010-a1b2c3d
```

**When to use nightly builds:**
- Testing latest unreleased features
- Pre-production validation
- Continuous deployment environments

### 3. Release (Manual)

Triggered by **git tags** matching `v*` pattern (e.g., `v1.2.15`).

**Steps:**
1. Run full test suite
2. Wait for manual approval (hold job)
3. Build multi-arch Docker images
4. Push to Docker Hub with tags:
   - `crovlune/plaxt:latest`
   - `crovlune/plaxt:1.2.15` (full version)
   - `crovlune/plaxt:1.2` (minor version)

**How to trigger a release:**

```bash
# Create and push a git tag
git tag v1.2.16
git push origin v1.2.16

# Or create tag with annotation
git tag -a v1.2.16 -m "Release version 1.2.16"
git push origin v1.2.16
```

After pushing the tag:
1. CircleCI will run tests automatically
2. A "hold-for-approval" job will appear in the UI
3. Click "Approve" to proceed with the release
4. Images will be built and pushed to Docker Hub

## Configuration Requirements

### CircleCI Context

Create a context named `docker-hub` with these environment variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `DOCKER_USERNAME` | Docker Hub username | `crovlune` |
| `DOCKER_PASSWORD` | Docker Hub access token | (create at hub.docker.com) |

**How to set up:**
1. Go to CircleCI → Organization Settings → Contexts
2. Create new context: `docker-hub`
3. Add environment variables
4. Use a Docker Hub **access token**, not your password

### Docker Hub Access Token

1. Visit https://hub.docker.com/settings/security
2. Click "New Access Token"
3. Name: `circleci-plaxt`
4. Permissions: Read, Write, Delete
5. Copy the token and add to CircleCI context

## Versioning Strategy

### Semantic Versioning

We follow [Semantic Versioning 2.0.0](https://semver.org/):

```
MAJOR.MINOR.PATCH
  1  .  2  . 15
```

- **MAJOR**: Incompatible API changes
- **MINOR**: New features (backward compatible)
- **PATCH**: Bug fixes (backward compatible)

### Version Bumping Guidelines

**When to bump MAJOR (1.x.x → 2.0.0):**
- Breaking changes to environment variables
- Removed storage backend support
- Changed webhook URL format
- Database schema changes requiring migration

**When to bump MINOR (x.2.x → x.3.0):**
- New features (manual renewal flow, new storage backend)
- New API endpoints
- UI overhaul with new capabilities

**When to bump PATCH (x.x.15 → x.x.16):**
- Bug fixes
- Performance improvements
- Documentation updates
- Security patches

### Current Version

The current version is defined by the latest git tag. To check:

```bash
git describe --tags --abbrev=0
```

### Docker Image Tags

Each release creates multiple tags for flexibility:

| Tag | Example | Description | When to use |
|-----|---------|-------------|-------------|
| `latest` | `crovlune/plaxt:latest` | Latest stable release | Production (auto-update) |
| Full version | `crovlune/plaxt:1.2.15` | Specific release | Production (pinned) |
| Minor version | `crovlune/plaxt:1.2` | Latest patch in minor | Production (patch updates) |
| Nightly | `crovlune/plaxt:nightly` | Latest nightly build | Testing/staging |
| Dated nightly | `crovlune/plaxt:nightly-20251010-a1b2c3d` | Specific nightly | Reproducible testing |

## Build Arguments

The Dockerfile accepts these build arguments:

| Argument | Purpose | Set by |
|----------|---------|--------|
| `VERSION` | Application version | CircleCI (from tag) |
| `COMMIT` | Git commit SHA | CircleCI (`$CIRCLE_SHA1`) |
| `DATE` | Build timestamp | CircleCI (UTC timestamp) |

These are injected into the binary using Go linker flags:

```go
var (
    version string
    commit  string
    date    string
)
```

View in running container:
```bash
docker logs <container-id> | head -1
# Output: Started version="1.2.15 (a1b2c3d@2025-10-10T02:00:00Z)"
```

## Cache Strategy

### Go Module Cache
- **Key**: `go-mod-v2-{{ checksum "go.sum" }}`
- **Path**: `/go/pkg/mod`
- **Invalidation**: Changes to `go.sum`
- **Size**: ~50-100 MB

### Go Build Cache
- **Key**: `go-build-v2-{{ .Branch }}-{{ .Revision }}`
- **Path**: `/tmp/go-cache`
- **Invalidation**: Per commit (with branch fallback)
- **Size**: ~100-200 MB

### Docker Layer Cache
- Handled automatically by Docker Buildx
- BuildKit cache mount for Go build artifacts
- Significantly speeds up multi-arch builds

**Cache hit rates:**
- First build: ~3-5 minutes
- Cached build (no changes): ~30 seconds
- Cached build (code changes): ~1-2 minutes

## Performance Benchmarks

Typical build times on CircleCI:

| Job | First Run | Cached | Notes |
|-----|-----------|--------|-------|
| Test | 2-3 min | 30-45 sec | With race detection |
| Docker (single arch) | 4-5 min | 1-2 min | With layer cache |
| Docker (multi-arch) | 8-10 min | 2-3 min | Parallel builds |

**Total nightly build time**: ~5-7 minutes (with cache)

## Troubleshooting

### Tests Failing

Check test results in CircleCI artifacts:
1. Click on failed job
2. Go to "Artifacts" tab
3. Download `test-results/coverage.html`

### Docker Build Failing

**BuildKit issues:**
```bash
# In CircleCI job, check Docker version
docker version
docker buildx version
```

**Authentication issues:**
```bash
# Verify credentials in context
echo $DOCKER_USERNAME
# Should not echo password in logs!
```

**Platform issues:**
```bash
# Check available platforms
docker buildx inspect --bootstrap
```

### Cache Not Working

**Force cache clear:**
1. Go to CircleCI project settings
2. Click "Clear cache"
3. Or bump cache version in config (`go-mod-v2` → `go-mod-v3`)

### Release Tag Not Building

**Check tag format:**
```bash
git tag -l
# Must match: v1.2.15, v2.0.0, etc.
# Won't match: 1.2.15, release-1.2.15
```

**Verify tag push:**
```bash
git ls-remote --tags origin
```

## Local Testing

### Test Docker Build Locally

```bash
# Build with version args
docker buildx build \
  --platform linux/amd64 \
  --build-arg VERSION="1.2.16-dev" \
  --build-arg COMMIT="$(git rev-parse HEAD)" \
  --build-arg DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t crovlune/plaxt:dev \
  --load \
  .

# Run and check version
docker run --rm crovlune/plaxt:dev
```

### Test Multi-Arch Build

```bash
# Create builder
docker buildx create --use --name test-builder

# Build for multiple architectures (don't push)
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION="test" \
  -t crovlune/plaxt:test \
  .
```

### Validate CircleCI Config

```bash
# Install CircleCI CLI
brew install circleci

# Validate config
circleci config validate .circleci/config.yml

# Process config (see final YAML)
circleci config process .circleci/config.yml
```

## Best Practices

### Before Creating a Release

1. **Update CHANGELOG.md**
   - Document all changes since last release
   - Follow Keep a Changelog format
   
2. **Test locally**
   - Run full test suite: `go test ./...`
   - Build Docker image and test manually
   - Check version output
   
3. **Update version references**
   - README.md examples
   - docker-compose.example.yml
   - Documentation

4. **Create git tag**
   - Use annotated tags: `git tag -a v1.2.16 -m "Release 1.2.16"`
   - Push tag: `git push origin v1.2.16`

5. **Monitor CI/CD**
   - Watch CircleCI build
   - Approve hold job
   - Verify images on Docker Hub
   - Test deployed image

### Security Considerations

- ✅ Use Docker Hub access tokens (not passwords)
- ✅ Store credentials in CircleCI context
- ✅ Use minimal base image (scratch)
- ✅ No secrets in build logs
- ✅ Version pinning for dependencies

## Additional Resources

- [CircleCI Documentation](https://circleci.com/docs/)
- [Docker Buildx Documentation](https://docs.docker.com/buildx/working-with-buildx/)
- [Semantic Versioning](https://semver.org/)
- [Keep a Changelog](https://keepachangelog.com/)

---

**Questions or issues?** Open an issue on GitHub or check CircleCI job logs for detailed error messages.
