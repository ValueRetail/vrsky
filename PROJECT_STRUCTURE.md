# VRSky Project Structure

**Last Updated**: February 2, 2026  
**Status**: Reorganized - All implementation code in `src/` folder

---

## Overview

The VRSky project is organized with a clean separation between:
- **Root directory**: Project metadata, documentation, and delegating build files
- **`src/` folder**: All implementation code (Go packages, Docker, Makefile, configuration)

This keeps the root directory clean while organizing all active development in `src/`.

---

## Directory Structure

```
vrsky/
├── 📄 README.md                        Project overview
├── 📄 LICENSE                          License file
├── 📄 COMMERCIAL_LICENSE.md            Commercial licensing
├── 📄 AGENTS.md                        Developer guide (existing)
│
├── 📄 Makefile                         ROOT: Delegates to src/Makefile
├── 📄 docker-compose.yml               ROOT: Delegates to src/docker-compose.yml
├── 📄 go.mod                           ROOT: Shared Go module definition
│
├── 📄 IMPLEMENTATION_SUMMARY.md        Producer implementation overview
├── 📄 PRODUCER_TEST_GUIDE.md           Producer testing & deployment guide
├── 📄 PROJECT_STRUCTURE.md             This file
│
├── docs/                               Project documentation (existing)
├── infrastructure/                     Infrastructure configs (existing)
├── internal/logging/                   Internal utilities (existing)
│
└── 🗂️ src/                             ✨ ALL IMPLEMENTATION CODE
    ├── 📄 Makefile                     Build automation (11 targets)
    ├── 📄 go.mod                       Go module dependencies
    ├── 📄 docker-compose.yml           Local development environment
    │
    ├── 📄 IMPLEMENTATION_SUMMARY.md    Full implementation overview
    ├── 📄 PRODUCER_TEST_GUIDE.md       Testing scenarios & debugging
    │
    ├── 🗂️ cmd/
    │   └── producer/
    │       ├── main.go                 Entry point (98 lines)
    │       ├── producer.go             Main loop (127 lines)
    │       └── Dockerfile              Multi-stage build (46 lines)
    │
    ├── 🗂️ pkg/
    │   ├── envelope/
    │   │   └── envelope.go             Message wrapper (43 lines)
    │   │
    │   ├── component/
    │   │   ├── component.go            Component interface (43 lines)
    │   │   ├── io.go                   Input/Output interfaces (29 lines)
    │   │   └── producer.go             Producer interface (18 lines)
    │   │
    │   └── io/
    │       ├── factory.go              Factory pattern (44 lines)
    │       ├── nats_input.go           NATS subscriber (159 lines)
    │       ├── http_output.go          HTTP client (136 lines)
    │       └── placeholders.go         Future implementations (80 lines)
    │
    └── 🗂️ internal/
        └── config/
            └── config.go               Config loader (60 lines)
```

---

## File Organization

### Root Level Files

These files provide project-level metadata and delegation to src/:

| File | Purpose |
|------|---------|
| `README.md` | Project overview and quick start |
| `LICENSE`, `COMMERCIAL_LICENSE.md` | Licensing information |
| `AGENTS.md` | Developer guide (existing) |
| `Makefile` | **Delegates all targets to `src/Makefile`** |
| `docker-compose.yml` | **Delegates to `src/docker-compose.yml`** with correct paths |
| `go.mod` | Shared Go module definition |

### Documentation Files

These can be at root or in src/ (both are valid):

| File | Location | Purpose |
|------|----------|---------|
| `IMPLEMENTATION_SUMMARY.md` | Root + src/ | Complete implementation overview |
| `PRODUCER_TEST_GUIDE.md` | Root + src/ | Testing scenarios & deployment |
| `PROJECT_STRUCTURE.md` | Root | This file (project organization) |

### Implementation in src/

All code is in `src/` to keep root clean:

- **cmd/** - One binary per service: the control plane (`management-api`),
  the shared transforms (`data-filter`, `data-converter`) and the connector
  services that run pipeline sources and destinations (`file-consumer`,
  `http-producer`, `sitoo-consumer`, …)
- **pkg/** - All Go packages (envelope, component, io)
- **internal/config/** - Configuration loader
- **Makefile** - Build automation (11 targets)
- **docker-compose.yml** - Local dev environment
- **go.mod** - Go module definition

---

## How to Use

### From Root Directory

All commands work from the root:

```bash
# Build binary (delegates to src/)
make build

# Build Docker image
make docker-build

# Start local environment
docker-compose up -d

# All other commands also delegate
make help      # Show all available commands
make test      # Run tests
make fmt       # Format code
make clean     # Clean artifacts
```

### From src/ Directory

You can also work directly in src/:

```bash
cd src/
make build
docker-compose up -d
```

---

## Build Process

### Root Makefile (Delegation)

```makefile
# Executes in src/ directory
help docker-build clean:
  @$(MAKE) -C src help
```

### Root docker-compose.yml (Context Adjustment)

```yaml
http-producer:
  build:
    context: .                  # Build context is the repo root
    dockerfile: src/cmd/http-producer/Dockerfile
```

### src/Makefile (Actual Implementation)

The actual build commands are in `src/Makefile`:

```makefile
build:
  @$(GO) build -o $(BIN_DIR)/ ./cmd/...
```

---

## Code Structure (in src/)

### 4 Go Packages

**1. pkg/envelope/**
- Message wrapper struct
- Adds unique ID and metadata to messages

**2. pkg/component/**
- Component interface (base for all components)
- Input/Output interfaces (pluggable I/O)
- Producer interface

**3. pkg/io/**
- Factory pattern to create I/O implementations
- NATS Input implementation (complete)
- HTTP Output implementation (complete)
- Placeholder implementations for future

**4. internal/config/**
- Configuration loader from environment variables
- JSON validation
- Error handling

### I/O Interface Pattern

The architecture uses a generic I/O pattern:

```
Configuration (JSON env vars)
    ↓
Factory creates Input/Output
    ↓
Producer Main Loop
    ├─ Read from Input
    ├─ Wrap in Envelope  
    ├─ Write to Output
    └─ Repeat
```

---

## Building & Deployment

### Local Development

```bash
# From root
make build                    # Binaries in src/bin/
docker-compose up -d          # Start the local TEST environment
docker-compose logs http-producer  # View results
```

### Docker Deployment

```bash
# Build image
make docker-build             # vrsky/producer:latest

# Push to registry (if configured)
make docker-push

# Run in production
docker-compose up -d
```

---

## Git Organization

The reorganization is tracked in a single commit:

```
commit 9608a50
  refactor: move all implementation files into src/ folder
  
  - Move cmd/, pkg/, internal/config/ → src/
  - Move build files (Makefile, go.mod) → src/
  - Create root delegating Makefile
  - Update docker-compose.yml with correct paths
```

---

## Maintenance Notes

### When Adding New Code

1. **Go packages**: Add to `src/pkg/`
2. **Commands**: Add to `src/cmd/`
3. **Tests**: Co-locate with code in `src/`
4. **Documentation**: Can be in `src/` or root

### When Updating Build

1. **Local changes**: Update `src/Makefile`
2. **Docker changes**: Update the service's `src/cmd/<service>/Dockerfile`
3. **Compose changes**: Update `docker-compose.yml`
4. **Root delegation**: Usually no change needed (already delegates)

### When Releasing

1. Ensure all code is in `src/`
2. Update version in `src/Makefile` (if needed)
3. Tag release: `git tag vX.Y.Z`
4. Build and push: `make docker-push`

---

## Key Benefits

✅ **Clean Root Directory**
- Project metadata at top level
- Implementation details hidden in src/

✅ **Organized Development**
- All active code in one place
- Easy to find and manage

✅ **Flexible Building**
- Works from root (delegating Makefile)
- Works from src/ (direct Makefile)
- Docker builds from src/ context

✅ **Scalable**
- Future components go in src/
- Same structure for Consumer, Converter, Filter
- Shared go.mod at root

✅ **Documentation**
- Overview docs at root for visibility
- Detailed docs in src/ near implementation

---

## Next Steps

### Phase 2: Consumer Components — done

Sources are served by connector services (`src/cmd/*-consumer`), each an SDK
Consumer that loads per-connection config from the connections table.

### Phase 3: Converter & Filter

Add new components in `src/cmd/` following the established pattern.

### Phase 4: Integration

Wire components together in the data plane.

---

## Summary

The VRSky project now has:
- ✅ Clean root directory with project metadata
- ✅ All implementation code organized in `src/`
- ✅ Delegating Makefile from root to src/
- ✅ Clear separation of concerns
- ✅ Ready for multi-component platform

**Status**: Reorganization complete and committed  
**Branch**: `Feature/components-start`  
**Latest Commit**: `9608a50` (refactor: move all implementation files into src/)
