# Release Process Quick Reference

This is a quick guide for creating new releases of Plaxt.

## TL;DR

```bash
# 1. Update version and changelog
vim CHANGELOG.md

# 2. Test locally
go test ./...
docker buildx build --platform linux/amd64 --build-arg VERSION="1.2.16" -t test --load .

# 3. Create and push tag
git tag -a v1.2.16 -m "Release 1.2.16"
git push origin v1.2.16

# 4. Go to CircleCI and approve the hold job
# 5. Verify images on Docker Hub
```

## Detailed Steps

### 1. Prepare the Release

#### Check Current Version
```bash
git describe --tags --abbrev=0
# Output: v1.2.15
```

#### Decide New Version

Follow [Semantic Versioning](https://semver.org/):

- **Patch** (1.2.15 → 1.2.16): Bug fixes, minor improvements
- **Minor** (1.2.15 → 1.3.0): New features, backward compatible
- **Major** (1.2.15 → 2.0.0): Breaking changes

### 2. Update CHANGELOG.md

Add a new section at the top:

```markdown
## [1.2.16] - 2025-10-10

### Added
- New feature description

### Fixed
- Bug fix description

### Changed
- Change description
```

Move items from `[Unreleased]` to the new version section.

### 3. Test Everything

```bash
# Run all tests
go test -v ./...

# Build and test Docker image locally
docker buildx build \
  --platform linux/amd64 \
  --build-arg VERSION="1.2.16" \
  --build-arg COMMIT="$(git rev-parse HEAD)" \
  --build-arg DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t crovlune/plaxt:1.2.16-test \
  --load \
  .

# Run the image
docker run --rm crovlune/plaxt:1.2.16-test

# Check version in logs
# Should see: Started version="1.2.16 (abc123@2025-10-10T...)"
```

### 4. Commit Changes

```bash
# Commit changelog and any other changes
git add CHANGELOG.md
git commit -m "Prepare release 1.2.16"
git push origin main
```

### 5. Create Git Tag

```bash
# Create annotated tag
git tag -a v1.2.16 -m "Release version 1.2.16

- Feature 1
- Feature 2
- Bug fix 1
"

# Verify tag
git show v1.2.16

# Push tag to trigger CircleCI
git push origin v1.2.16
```

### 6. Monitor CircleCI

1. Go to https://app.circleci.com/
2. Navigate to your project
3. Find the workflow for tag `v1.2.16`
4. Wait for tests to complete
5. **Click "Approve"** on the `hold-for-approval` job
6. Wait for Docker build and push to complete

### 7. Verify Release

#### Check Docker Hub

Visit https://hub.docker.com/r/crovlune/plaxt/tags

Verify these tags exist:
- `latest`
- `1.2.16`
- `1.2`

#### Test Deployed Image

```bash
# Pull and run latest
docker pull crovlune/plaxt:latest
docker run --rm crovlune/plaxt:latest

# Check version in output
# Should see: Started version="1.2.16 (...)"

# Test with docker-compose
docker-compose up -d
docker-compose logs plaxt | head -1
```

### 8. Create GitHub Release (Optional)

1. Go to https://github.com/crovlune/plaxt/releases
2. Click "Draft a new release"
3. Choose tag: `v1.2.16`
4. Title: `v1.2.16`
5. Copy changelog section into description
6. Click "Publish release"

## Common Issues

### Tag Already Exists

```bash
# Delete local tag
git tag -d v1.2.16

# Delete remote tag
git push origin :refs/tags/v1.2.16

# Create new tag
git tag -a v1.2.16 -m "Release 1.2.16"
git push origin v1.2.16
```

### CircleCI Workflow Not Triggered

Check:
1. Tag format matches `v*` pattern (e.g., `v1.2.16`, not `1.2.16`)
2. Tag was pushed to GitHub: `git ls-remote --tags origin`
3. CircleCI is connected to repository

### Build Failing

1. Check CircleCI job logs
2. Look for test failures
3. Verify Docker credentials in context
4. Check if tag format is correct in workflow

## Versioning Examples

### Patch Release (Bug Fix)

```bash
# Current: v1.2.15
# New: v1.2.16

git tag -a v1.2.16 -m "Fix manual renewal UI bug"
git push origin v1.2.16
```

Results in Docker tags:
- `crovlune/plaxt:latest`
- `crovlune/plaxt:1.2.16`
- `crovlune/plaxt:1.2` (updates existing)

### Minor Release (New Feature)

```bash
# Current: v1.2.15
# New: v1.3.0

git tag -a v1.3.0 -m "Add Kubernetes support"
git push origin v1.3.0
```

Results in Docker tags:
- `crovlune/plaxt:latest`
- `crovlune/plaxt:1.3.0`
- `crovlune/plaxt:1.3` (new tag)

### Major Release (Breaking Change)

```bash
# Current: v1.2.15
# New: v2.0.0

git tag -a v2.0.0 -m "Require PostgreSQL 15+, drop disk storage"
git push origin v2.0.0
```

Results in Docker tags:
- `crovlune/plaxt:latest`
- `crovlune/plaxt:2.0.0`
- `crovlune/plaxt:2.0` (new tag)

## Rollback

If you need to rollback a release:

### Rollback Docker Hub Tags

```bash
# Re-tag previous version as latest
docker pull crovlune/plaxt:1.2.15
docker tag crovlune/plaxt:1.2.15 crovlune/plaxt:latest
docker push crovlune/plaxt:latest
```

### Rollback Git Tag

```bash
# Delete the bad tag
git tag -d v1.2.16
git push origin :refs/tags/v1.2.16

# Optionally create a new patch version
git tag -a v1.2.17 -m "Hotfix: Revert breaking change"
git push origin v1.2.17
```

## Checklist

Before pushing a release tag:

- [ ] All tests pass locally
- [ ] CHANGELOG.md is updated
- [ ] Version follows semantic versioning
- [ ] Docker image builds locally
- [ ] Version appears correctly in logs
- [ ] Breaking changes are documented
- [ ] Tag message is descriptive

After pushing a release tag:

- [ ] CircleCI workflow is triggered
- [ ] Tests pass in CI
- [ ] Manual approval given
- [ ] Docker images pushed successfully
- [ ] All expected tags exist on Docker Hub
- [ ] Latest tag points to new version
- [ ] Test deployment with new image
- [ ] GitHub release created (optional)

## Getting Help

- CircleCI issues: Check `.circleci/README.md`
- Versioning questions: See CHANGELOG.md
- Docker issues: Check Dockerfile comments
- Build problems: Review CI logs in CircleCI dashboard

---

**Pro tip:** Use `git tag -l 'v*' --sort=-v:refname | head -5` to see your last 5 releases.
