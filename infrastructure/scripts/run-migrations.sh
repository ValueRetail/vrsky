#!/bin/bash

# Run database migrations using golang-migrate
# This script is used during Docker startup and manual migrations

set -e

# Configuration
MIGRATIONS_PATH="${MIGRATIONS_PATH:-./infrastructure/migrations}"
DATABASE_URL="${DATABASE_URL:-postgres://vrsky_user:vrsky_pass@postgres:5432/vrsky}"

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check if migrations directory exists
if [ ! -d "$MIGRATIONS_PATH" ]; then
    echo -e "${RED}ERROR: Migrations directory not found: $MIGRATIONS_PATH${NC}"
    exit 1
fi

echo -e "${YELLOW}Running database migrations...${NC}"
echo "Migrations path: $MIGRATIONS_PATH"
echo "Database: $DATABASE_URL"

# Check if migrate binary is available
if ! command -v migrate &> /dev/null; then
    echo -e "${RED}ERROR: 'migrate' command not found. Install golang-migrate to continue.${NC}"
    echo "Installation: https://github.com/golang-migrate/migrate/tree/master/cmd/migrate"
    exit 1
fi

# Run migrations
if migrate -path "$MIGRATIONS_PATH" -database "$DATABASE_URL" up; then
    echo -e "${GREEN}Migrations completed successfully!${NC}"
    
    # Show current migration version
    VERSION=$(migrate -path "$MIGRATIONS_PATH" -database "$DATABASE_URL" version 2>&1 || echo "unknown")
    echo "Current migration version: $VERSION"
else
    # Check if error is because we're already at latest version
    if migrate -path "$MIGRATIONS_PATH" -database "$DATABASE_URL" version 2>&1 | grep -q "no change"; then
        echo -e "${GREEN}Already at latest migration version.${NC}"
    else
        echo -e "${RED}Migration failed!${NC}"
        exit 1
    fi
fi
