#!/bin/bash
# Get version from git tag or use commit SHA as fallback
# Usage: ./scripts/get-version.sh [SERVICE_NAME]
# Output: version string (e.g., "v1.2.3" or "sha-a1b2c3d4")

set -e

SERVICE_NAME="${1:-.}"

# Check if we're on a tag
VERSION=$(git describe --exact-match --tags 2>/dev/null || true)

if [ -z "$VERSION" ]; then
    # Fall back to short commit SHA
    VERSION="sha-$(git rev-parse --short HEAD)"
fi

# Remove 'v' prefix if present for non-v-prefixed services
if [[ ! "$VERSION" =~ ^v[0-9] ]] && [[ "$VERSION" != "sha-"* ]]; then
    VERSION="v${VERSION}"
fi

echo "$VERSION"
