# Docker File Components Deployment Guide

This guide explains how to deploy, configure, and run the File Consumer and File Producer components using Docker and Docker Compose.

## Table of Contents
1. [Quick Start](#quick-start)
2. [Prerequisites](#prerequisites)
3. [Building Docker Images](#building-docker-images)
4. [Docker Compose Setup](#docker-compose-setup)
5. [Configuration](#configuration)
6. [Running Services](#running-services)
7. [Monitoring & Logging](#monitoring--logging)
8. [Troubleshooting](#troubleshooting)
9. [Production Deployment](#production-deployment)

## Quick Start

**5-Minute Setup**:

```bash
# 1. Clone/navigate to project
cd /home/ludvik/vrsky

# 2. Build Docker images
docker-compose -f docker-compose-files.yml build

# 3. Start services
docker-compose -f docker-compose-files.yml up -d

# 4. Verify services running
docker-compose -f docker-compose-files.yml ps

# 5. Test pipeline
echo "Hello World" > data/input/test.txt

# 6. Check output
ls -la data/output/

# 7. View logs
docker-compose -f docker-compose-files.yml logs -f file-consumer
```

## Prerequisites

### System Requirements

- **Docker**: Version 20.10 or later
- **Docker Compose**: Version 1.29 or later
- **Disk Space**: Minimum 2GB free (for images + test data)
- **Memory**: Minimum 2GB available
- **Linux/macOS/Windows**: Docker Desktop supports all platforms

### Installation

**macOS (with Homebrew)**:
```bash
brew install docker docker-compose
# or install Docker Desktop: https://www.docker.com/products/docker-desktop
```

**Linux (Ubuntu/Debian)**:
```bash
sudo apt-get install docker.io docker-compose
sudo usermod -aG docker $USER
```

**Windows**: Download [Docker Desktop for Windows](https://www.docker.com/products/docker-desktop)

### Verify Installation

```bash
docker --version          # Should show Docker version
docker-compose --version # Should show Compose version
docker ps                 # Should list containers
```

## Building Docker Images

### Prerequisites Explained

The Dockerfiles use multi-stage builds:

**Stage 1 (Builder)**:
- Go 1.21 Alpine image
- Compiles source code
- Installs dependencies

**Stage 2 (Runtime)**:
- Alpine 3.18 base image
- Copies compiled binary only
- Final size: ~25-30MB

### Build Manually

```bash
# Build file-consumer image
docker build \
  -t vrsky-file-consumer:latest \
  -f src/cmd/file-consumer/Dockerfile .

# Build file-producer image
docker build \
  -t vrsky-file-producer:latest \
  -f src/cmd/file-producer/Dockerfile .

# Verify images
docker images | grep vrsky
```

### Build via Docker Compose

```bash
# Build all images defined in docker-compose-files.yml
docker-compose -f docker-compose-files.yml build

# Build specific service
docker-compose -f docker-compose-files.yml build file-consumer

# Build without cache (fresh build)
docker-compose -f docker-compose-files.yml build --no-cache
```

### Build Options

```bash
# Tag with custom registry
docker tag vrsky-file-consumer:latest myregistry/vrsky-file-consumer:v1.0.0

# Push to registry
docker push myregistry/vrsky-file-consumer:v1.0.0

# Build for multiple platforms
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t vrsky-file-consumer:latest .
```

## Docker Compose Setup

### File Structure

```
vrsky/
├── docker-compose-files.yml     # Services for file pipeline
├── docker-compose.yml           # Services for HTTP pipeline
├── src/
│   ├── cmd/
│   │   ├── file-consumer/Dockerfile
│   │   └── file-producer/Dockerfile
│   ├── pkg/io/
│   │   ├── file_input.go
│   │   └── file_output.go
│   └── ...
├── data/
│   ├── input/                   # Input files directory
│   ├── output/                  # Output files directory
│   ├── archive/                 # Processed files archive
│   └── error/                   # Error files directory
└── ...
```

### Compose File Overview

```yaml
version: '3.8'

services:
  nats:                           # NATS message broker
  file-consumer:                  # File Consumer service
  file-producer:                  # File Producer service

networks:
  vrsky-files-network:           # Custom network

volumes:
  nats-data:                     # NATS persistence
```

### Creating Data Directories

```bash
# Create data directories for bind mounts
mkdir -p data/input data/output data/archive data/error

# Set proper permissions
chmod 755 data/input data/output data/archive data/error

# Optional: Pre-populate with test files
echo "test data" > data/input/sample.txt
```

## Configuration

### Environment Variables in Docker Compose

Edit `docker-compose-files.yml` to configure:

```yaml
services:
  file-consumer:
    environment:
      FILE_INPUT_DIR: "/data/input"
      FILE_INPUT_ARCHIVE_DIR: "/data/archive"
      FILE_INPUT_ERROR_DIR: "/data/error"
      FILE_INPUT_POLL_INTERVAL: "5s"
      FILE_INPUT_MAX_RETRIES: "3"
```

### Override Variables at Runtime

```bash
# Set via command line
docker-compose -f docker-compose-files.yml run \
  -e FILE_INPUT_DIR=/custom/input \
  file-consumer

# Set via .env file
cat > .env << EOF
FILE_INPUT_DIR=/data/input
FILE_INPUT_ARCHIVE_DIR=/data/archive
FILE_INPUT_POLL_INTERVAL=5s
EOF

docker-compose -f docker-compose-files.yml up -d
```

### Common Configuration Scenarios

#### Scenario 1: Fast Processing (1s poll)

```yaml
file-consumer:
  environment:
    FILE_INPUT_POLL_INTERVAL: "1s"
    FILE_INPUT_BUFFER_SIZE: "1000"
```

#### Scenario 2: Large File Handling

```yaml
file-producer:
  environment:
    FILE_OUTPUT_CHUNK_SIZE: "1048576"      # 1MB chunks
    FILE_OUTPUT_FSYNC_INTERVAL: "100"      # Fsync every 100MB
    FILE_OUTPUT_MAX_FILE_SIZE: "107374182400"  # 100GB
```

#### Scenario 3: Archive with Retention

```yaml
file-consumer:
  environment:
    FILE_INPUT_ARCHIVE_DIR: "/data/archive"
    FILE_INPUT_DELETE_AFTER_PROCESSING: "false"
    FILE_INPUT_ARCHIVE_RETENTION_DAYS: "30"
```

## Running Services

### Start Services

```bash
# Start all services in background
docker-compose -f docker-compose-files.yml up -d

# Start with logs visible
docker-compose -f docker-compose-files.yml up

# Start specific service
docker-compose -f docker-compose-files.yml up -d file-consumer

# Start with build
docker-compose -f docker-compose-files.yml up -d --build
```

### Check Service Status

```bash
# List all containers
docker-compose -f docker-compose-files.yml ps

# Expected output:
# NAME                    STATUS
# vrsky-nats-files        Up (healthy)
# vrsky-file-consumer     Up (healthy)
# vrsky-file-producer     Up (healthy)
```

### Verify Services are Ready

```bash
# Wait for services to be healthy
docker-compose -f docker-compose-files.yml up -d
sleep 10

# Check NATS
docker-compose -f docker-compose-files.yml exec nats nc -z localhost 4222 && echo "NATS is ready"

# Check file-consumer
docker-compose -f docker-compose-files.yml exec file-consumer test -f /app/file-consumer && echo "Consumer is ready"

# Check file-producer
docker-compose -f docker-compose-files.yml exec file-producer test -f /app/file-producer && echo "Producer is ready"
```

### Stop Services

```bash
# Stop all services
docker-compose -f docker-compose-files.yml stop

# Stop specific service
docker-compose -f docker-compose-files.yml stop file-consumer

# Stop and remove containers
docker-compose -f docker-compose-files.yml down

# Stop, remove containers, and delete volumes
docker-compose -f docker-compose-files.yml down -v
```

### Restart Services

```bash
# Restart all services
docker-compose -f docker-compose-files.yml restart

# Restart specific service
docker-compose -f docker-compose-files.yml restart file-consumer

# Restart with rebuild
docker-compose -f docker-compose-files.yml up -d --build
```

## Monitoring & Logging

### View Logs

```bash
# View all logs
docker-compose -f docker-compose-files.yml logs

# View specific service logs
docker-compose -f docker-compose-files.yml logs file-consumer
docker-compose -f docker-compose-files.yml logs file-producer
docker-compose -f docker-compose-files.yml logs nats

# Follow logs (tail -f)
docker-compose -f docker-compose-files.yml logs -f

# Show last 100 lines
docker-compose -f docker-compose-files.yml logs --tail=100

# Show logs since specific time
docker-compose -f docker-compose-files.yml logs --since=10m
```

### Real-time Monitoring

```bash
# Monitor containers
docker-compose -f docker-compose-files.yml top

# Monitor resource usage
docker stats

# Watch continuously
watch -n 2 "docker-compose -f docker-compose-files.yml ps"
```

### Health Checks

```bash
# Check container health
docker-compose -f docker-compose-files.yml exec file-consumer test -f /app/file-consumer

# View health status
docker ps --format "{{.Names}}\t{{.Status}}"

# Expected:
# vrsky-nats-files        Up 5 minutes (healthy)
# vrsky-file-consumer     Up 5 minutes (healthy)
# vrsky-file-producer     Up 5 minutes (healthy)
```

### Accessing Container Shell

```bash
# Interactive shell in file-consumer
docker-compose -f docker-compose-files.yml exec -it file-consumer sh

# Run command in container
docker-compose -f docker-compose-files.yml exec file-consumer ls -la /data/input

# Check environment variables
docker-compose -f docker-compose-files.yml exec file-consumer env | grep FILE_
```

## Troubleshooting

### Issue: Services won't start

**Symptoms**: `docker-compose up` fails immediately

**Causes**:
- Port already in use (4222 for NATS)
- Docker daemon not running
- Insufficient disk space

**Solutions**:
```bash
# 1. Check if Docker daemon is running
docker ps

# 2. Free up disk space
df -h

# 3. Check for port conflicts
lsof -i :4222

# 4. Kill process using port
kill -9 $(lsof -t -i :4222)

# 5. Try again
docker-compose -f docker-compose-files.yml up -d
```

### Issue: Services are stuck/not healthy

**Symptoms**: Containers showing status "Restarting" or unhealthy

**Causes**:
- Build errors
- Configuration errors
- Missing volumes
- Insufficient resources

**Solutions**:
```bash
# 1. Check logs for errors
docker-compose -f docker-compose-files.yml logs

# 2. Rebuild images
docker-compose -f docker-compose-files.yml down -v
docker-compose -f docker-compose-files.yml build --no-cache

# 3. Start fresh
docker-compose -f docker-compose-files.yml up -d

# 4. Check system resources
docker stats

# 5. Increase Docker resource limits (if needed)
# Edit Docker Desktop settings or dockerd configuration
```

### Issue: Files not appearing in output directory

**Symptoms**: Input files aren't being processed

**Causes**:
- Services not healthy
- Volume mounts not working
- File permissions issue
- Consumer/Producer not connected

**Solutions**:
```bash
# 1. Verify services are healthy
docker-compose -f docker-compose-files.yml ps

# 2. Check volume mounts
docker inspect vrsky-file-consumer | grep -A 10 Mounts

# 3. Check file permissions
ls -la data/input
ls -la data/output

# 4. Fix permissions if needed
chmod 777 data/input data/output data/archive data/error

# 5. Check logs
docker-compose -f docker-compose-files.yml logs file-consumer
docker-compose -f docker-compose-files.yml logs file-producer

# 6. Manually place test file
echo "test" > data/input/test.txt
sleep 10
ls -la data/output/
```

### Issue: "Permission denied" errors

**Symptoms**: Errors about permission denied when writing files

**Causes**:
- Volume mounted with wrong permissions
- User/group mismatch
- SELinux/AppArmor restrictions

**Solutions**:
```bash
# 1. Check current permissions
ls -la data/output

# 2. Make writable by everyone (development only)
chmod 777 data/output data/input

# 3. Fix ownership
sudo chown -R $USER:$USER data/

# 4. Mount volume with specific user
docker-compose -f docker-compose-files.yml run \
  --user $(id -u):$(id -g) \
  file-producer

# 5. Check SELinux status
getenforce

# 6. Disable SELinux for testing
sudo setenforce 0
```

### Issue: "Disk full" errors

**Symptoms**: Write fails with "no space left on device"

**Causes**:
- Output directory on full disk
- Archive files accumulating
- Test files not cleaned up

**Solutions**:
```bash
# 1. Check disk space
df -h

# 2. Find large directories
du -sh data/*

# 3. Clean up test files
rm -rf data/output/*
rm -rf data/archive/*

# 4. Clean up old archives
find data/archive -mtime +30 -delete

# 5. Move output to different disk
mkdir -p /mnt/large-disk/output
docker-compose -f docker-compose-files.yml down
mv data/output/* /mnt/large-disk/output/
docker-compose -f docker-compose-files.yml up -d
```

## Production Deployment

### Kubernetes Deployment

```yaml
# consumer-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: file-consumer
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: file-consumer
        image: vrsky-file-consumer:v1.0.0
        env:
        - name: FILE_INPUT_DIR
          value: "/data/input"
        - name: NATS_URL
          value: "nats://nats-cluster:4222"
        volumeMounts:
        - name: input-data
          mountPath: /data/input
        resources:
          requests:
            memory: "100Mi"
            cpu: "100m"
          limits:
            memory: "500Mi"
            cpu: "500m"
      volumes:
      - name: input-data
        persistentVolumeClaim:
          claimName: input-pvc
```

### Docker Swarm Deployment

```bash
# Initialize Swarm
docker swarm init

# Create overlay network
docker network create -d overlay vrsky-files

# Deploy service
docker service create \
  --name file-consumer \
  --network vrsky-files \
  --env FILE_INPUT_DIR=/data/input \
  --env NATS_URL=nats://nats:4222 \
  --mount type=bind,source=/data/input,target=/data/input \
  vrsky-file-consumer:latest
```

### Production Checklist

- [ ] Use Docker registry (private or public)
- [ ] Version all images (v1.0.0, not latest)
- [ ] Use resource limits (memory, CPU)
- [ ] Set restart policies (restart: always)
- [ ] Configure health checks
- [ ] Use volumes for data persistence
- [ ] Monitor container logs
- [ ] Set up alerting for unhealthy services
- [ ] Regular backups of archive/error directories
- [ ] Test disaster recovery procedures

### Recommended Settings for Production

```yaml
services:
  file-consumer:
    restart: always
    environment:
      FILE_INPUT_POLL_INTERVAL: "5s"
      FILE_INPUT_MAX_RETRIES: "3"
      FILE_INPUT_ARCHIVE_DIR: "/data/archive"
      FILE_INPUT_ERROR_DIR: "/data/error"
    resources:
      limits:
        memory: 512M
        cpus: '0.5'
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"

  file-producer:
    restart: always
    environment:
      FILE_OUTPUT_CHUNK_SIZE: "524288"
      FILE_OUTPUT_FSYNC_INTERVAL: "20"
    resources:
      limits:
        memory: 512M
        cpus: '0.5'
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
```

## Testing

### E2E Test with Docker

```bash
# Run Docker E2E test script
./test/test-file-pipeline-docker.sh

# Run with verbose output
./test/test-file-pipeline-docker.sh --verbose

# Preserve artifacts on success
./test/test-file-pipeline-docker.sh --preserve

# Expected output:
# [Test 1] Building Docker images
# ✓ PASS: file-consumer image built successfully
# [Test 2] Starting Docker Compose services
# ✓ PASS: Docker Compose services started
# ... (more tests)
# ✓ All Docker E2E tests passed!
```

## Performance Tuning

### For High Throughput

```yaml
file-consumer:
  environment:
    FILE_INPUT_POLL_INTERVAL: "1s"
    FILE_INPUT_BUFFER_SIZE: "1000"

file-producer:
  environment:
    FILE_OUTPUT_CHUNK_SIZE: "1048576"
    FILE_OUTPUT_FSYNC_INTERVAL: "100"
  resources:
    limits:
      memory: 1G
      cpus: '1.0'
```

### For Large Files

```yaml
file-producer:
  environment:
    FILE_OUTPUT_CHUNK_SIZE: "2097152"      # 2MB chunks
    FILE_OUTPUT_MAX_FILE_SIZE: "107374182400"  # 100GB
    FILE_OUTPUT_FSYNC_INTERVAL: "500"
  resources:
    limits:
      memory: 2G
      cpus: '2.0'
```

## Cleanup

```bash
# Stop all services
docker-compose -f docker-compose-files.yml down

# Remove all data
rm -rf data/

# Remove Docker images
docker rmi vrsky-file-consumer:latest
docker rmi vrsky-file-producer:latest

# Remove NATS volume
docker volume rm vrsky-nats-data

# Clean up everything including orphans
docker-compose -f docker-compose-files.yml down -v --remove-orphans
```

## See Also

- `FILE_CONSUMER_GUIDE.md` - Configuration reference
- `FILE_PRODUCER_GUIDE.md` - Configuration reference
- `FILE_COMPONENTS_ARCHITECTURE.md` - Design decisions
- Docker Documentation: https://docs.docker.com/
- Docker Compose: https://docs.docker.com/compose/
