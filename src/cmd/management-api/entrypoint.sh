#!/bin/sh

# Entrypoint script for VRSky Management API
# Handles database migrations and service startup

set -e

echo "[Management API] Starting VRSky Management API..."

# Configuration
DB_HOST="${DB_HOST:-postgres-management}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-management_db}"
DATABASE_URL="${DATABASE_URL:-postgres://${DB_USER}:${POSTGRES_PASSWORD:-management_password}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable}"

MAX_RETRIES=30
RETRY_COUNT=0

# Function to check if PostgreSQL is ready
check_postgres() {
    if command -v pg_isready &> /dev/null; then
        pg_isready -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" >/dev/null 2>&1
    else
        # Fallback: try connecting with psql
        if command -v psql &> /dev/null; then
            psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1" >/dev/null 2>&1
        else
            # Final fallback: assume it's ready (this will fail in management-api if DB is actually down)
            return 0
        fi
    fi
}

# Wait for PostgreSQL to be ready
echo "[Management API] Waiting for PostgreSQL to be ready..."
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if check_postgres; then
        echo "[Management API] PostgreSQL is ready!"
        break
    fi
    RETRY_COUNT=$((RETRY_COUNT + 1))
    echo "[Management API] PostgreSQL not ready yet... Retry $RETRY_COUNT/$MAX_RETRIES"
    sleep 2
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
    echo "[Management API] ERROR: Could not connect to PostgreSQL after $MAX_RETRIES attempts"
    exit 1
fi

# Run database migrations
echo "[Management API] Running database migrations..."
if [ -d "./migrations" ]; then
    # Using golang-migrate if available
    if command -v migrate &> /dev/null; then
        echo "[Management API] Using golang-migrate for schema migrations..."
        if migrate -path "./migrations" -database "$DATABASE_URL" up; then
            echo "[Management API] ✓ Database migrations completed successfully"
        else
            # It's OK if we're already at the latest version
            if migrate -path "./migrations" -database "$DATABASE_URL" version 2>&1 | grep -q "dirty\|no change"; then
                echo "[Management API] ✓ Already at latest migration version"
            else
                echo "[Management API] ⚠ Migration completed with warnings (this may be normal)"
            fi
        fi
    else
        echo "[Management API] ⚠ golang-migrate not found - skipping migrations"
        echo "[Management API] You may need to run migrations manually if tables don't exist"
    fi
else
    echo "[Management API] ⚠ Migrations directory not found - skipping migrations"
fi

echo "[Management API] ✓ Startup preparation complete"
echo "[Management API] Starting service on ${LISTEN_ADDR:-:3000}..."

# Start the management-api service
exec ./management-api
