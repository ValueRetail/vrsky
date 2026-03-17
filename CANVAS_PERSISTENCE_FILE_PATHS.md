# VRSKY Codebase Canvas Persistence Analysis - File Paths & Locations

## UI Frontend Files (React + Zustand)

### State Management Stores
- /home/ludvik/vrsky/ui/src/store/uiStore.ts (120 lines)
  - Reference: Zustand pattern for state management
  - Contains: UI notifications, sidebar, dialogs

- /home/ludvik/vrsky/ui/src/store/connectionsStore.ts (96 lines)
  - Reference: Zustand pattern for connection state
  - Contains: CRUD state, no persistence

- /home/ludvik/vrsky/ui/src/store/messageLogStore.ts
  - Reference: Zustand pattern for logging

- /home/ludvik/vrsky/ui/src/store/metricsStore.ts
  - Reference: Zustand pattern for metrics

### Pipeline Builder Components
- /home/ludvik/vrsky/ui/src/pages/PipelineBuilder.tsx (537 lines)
  - MAIN FILE: Canvas editor with React state
  - Contains: nodes[], edges[], selectedNode, selectedNodeId, etc.
  - Current Issue: ALL state is ephemeral, no persistence
  - Needs: Integration with Zustand store

- /home/ludvik/vrsky/ui/src/pages/PipelineBuilderPage.tsx
  - Wrapper: Provides ReactFlowProvider context

- /home/ludvik/vrsky/ui/src/components/Pipeline/KonvaCanvas.tsx (199 lines)
  - Renders: Konva stage with nodes and edges
  - Connection drawing: Bezier curves

- /home/ludvik/vrsky/ui/src/components/Pipeline/KonvaNode.tsx
  - Individual node rendering

- /home/ludvik/vrsky/ui/src/components/Pipeline/KonvaConnection.tsx
  - Connection/edge rendering

- /home/ludvik/vrsky/ui/src/components/Pipeline/PropertyEditor.tsx
  - Node configuration panel (right sidebar)

- /home/ludvik/vrsky/ui/src/components/Pipeline/ComponentPalette.tsx
  - Left sidebar drag-drop palette

### Type Definitions
- /home/ludvik/vrsky/ui/src/types/pipeline.ts (22 lines)
  - Node interface: id, type, data{label, config, type}, position{x, y}
  - Edge interface: id, source, target

- /home/ludvik/vrsky/ui/src/types/models.ts (231 lines)
  - Connection, Tenant, Notification models
  - Source/Converter/Filter/Destination configs

- /home/ludvik/vrsky/ui/src/types/api.ts (166 lines)
  - API request/response types

### Services & API Clients
- /home/ludvik/vrsky/ui/src/services/api.ts (57 lines)
  - Axios instance
  - Adds X-Tenant-ID header automatically
  - Tenant injection: config.tenantId from environment

- /home/ludvik/vrsky/ui/src/services/connectionService.ts (110 lines)
  - REST client for connections
  - Pattern to follow for draftService

- /home/ludvik/vrsky/ui/src/services/metricsService.ts
  - Metrics API calls

- /home/ludvik/vrsky/ui/src/services/testDataService.ts
  - Test data generation API calls

- /home/ludvik/vrsky/ui/src/services/websocket.ts
  - WebSocket for real-time metrics

### Configuration
- /home/ludvik/vrsky/ui/src/config/env.ts (31 lines)
  - API URL, WS URL, Tenant ID (static per environment)
  - Tenant is configured globally, not per user

- /home/ludvik/vrsky/ui/src/main.tsx
  - App entry point
  - localStorage.getItem('theme') for theme recovery

- /home/ludvik/vrsky/ui/src/App.tsx
  - Main app routing

### Hooks
- /home/ludvik/vrsky/ui/src/hooks/useNodeDrag.ts (29 lines)
  - Grid snapping for node positions
  - Pattern: useCallback for performance

- /home/ludvik/vrsky/ui/src/hooks/useConnectionDrawing.ts (129 lines)
  - Connection drawing state management
  - Pattern: useState for drawing state

- /home/ludvik/vrsky/ui/src/hooks/useMetrics.ts
  - Metrics polling hook

### UI Components
- /home/ludvik/vrsky/ui/src/components/Layout/Header.tsx
  - Theme toggle: localStorage.setItem('theme', 'light')
  - ONLY localStorage usage in codebase

- /home/ludvik/vrsky/ui/src/components/Common/Toast.tsx
  - Notification display

- /home/ludvik/vrsky/ui/src/components/Common/ConfirmDialog.tsx
  - Confirmation dialog

### Utilities
- /home/ludvik/vrsky/ui/src/utils/validation.ts (11KB)
  - Pipeline validation logic
  - validatePipelineConnections() function

- /home/ludvik/vrsky/ui/src/utils/nodeNumbering.ts
  - Node renumbering utilities

- /home/ludvik/vrsky/ui/src/utils/errors.ts (2KB)
  - Error handling utilities

### Configuration Files
- /home/ludvik/vrsky/ui/package.json
  - Dependencies: Zustand 5.0.11, Axios 1.13.5, React 18.3.1
  - Zustand is already available

- /home/ludvik/vrsky/ui/tsconfig.json
  - TypeScript configuration

---

## Backend Files (Go + PostgreSQL)

### Handler Layer
- /home/ludvik/vrsky/src/pkg/managementapi/handler.go (620 lines)
  - RegisterRoutes() registers REST endpoints
  - Current endpoints: POST/GET/PUT/DELETE /connections, etc.
  - Pattern: GET tenant from context, decode JSON, validate, persist, respond

- /home/ludvik/vrsky/src/pkg/managementapi/handler_test.go
  - Handler tests

### Models & Domain Objects
- /home/ludvik/vrsky/src/pkg/managementapi/models.go (387 lines)
  - Connection struct: ID, TenantID, Name, Description, Nodes[], Edges[], Status
  - Node struct: ID, Type, Config (json.RawMessage), Enabled
  - Edge struct: ID, Source, Target, Order
  - SourceConfig, ConverterConfig, FilterConfig, DestinationConfig

- /home/ludvik/vrsky/src/pkg/managementapi/models_test.go

### Data Access Layer
- /home/ludvik/vrsky/src/pkg/managementapi/repository.go
  - Repository interface: defines CRUD contract
  - Methods: CreateConnection, GetConnection, ListConnections, UpdateConnection, DeleteConnection

- /home/ludvik/vrsky/src/pkg/managementapi/postgres_repository.go
  - PostgresRepository implementation
  - SQL queries for connection CRUD

### Utilities & Support
- /home/ludvik/vrsky/src/pkg/managementapi/validator.go
  - ValidateConnection(): connection validation
  - ValidateDAG(): directed acyclic graph validation for nodes/edges

- /home/ludvik/vrsky/src/pkg/managementapi/validator_test.go

- /home/ludvik/vrsky/src/pkg/managementapi/errors.go
  - Custom error types (ConflictError, ConfigError, DAGValidationError)

- /home/ludvik/vrsky/src/pkg/managementapi/auth.go
  - Tenant validation middleware

### Publish/Subscribe
- /home/ludvik/vrsky/src/pkg/managementapi/nats_publisher.go
  - NATS message publishing

- /home/ludvik/vrsky/src/pkg/managementapi/nats_subscriber.go
  - NATS message subscription

### Real-Time Features
- /home/ludvik/vrsky/src/pkg/managementapi/websocket.go
  - WebSocket support for metrics

- /home/ludvik/vrsky/src/pkg/managementapi/metrics_cache.go
  - Metrics caching and broadcasting

### Test Data
- /home/ludvik/vrsky/src/pkg/managementapi/test_generator.go
  - Auto-generator for test messages

- /home/ludvik/vrsky/src/pkg/managementapi/api_consumer_handler.go
  - API consumer endpoint handling

### Client Registry
- /home/ludvik/vrsky/src/pkg/managementapi/client_registry.go
  - WebSocket client tracking

### Server Setup
- /home/ludvik/vrsky/src/cmd/management-api/main.go (218 lines)
  - Server setup and initialization
  - Route registration: restHandler.RegisterRoutes(mux)
  - Middleware: TenantIDMiddleware, CORSMiddleware, LoggingMiddleware

- /home/ludvik/vrsky/src/cmd/management-api/config.go
  - Configuration loading

- /home/ludvik/vrsky/src/cmd/management-api/cors.go
  - CORS middleware implementation

- /home/ludvik/vrsky/src/cmd/management-api/tenant.go
  - Tenant validation and extraction

---

## Database & Infrastructure

### Docker Compose
- /home/ludvik/vrsky/docker-compose.yml
  - PostgreSQL, NATS, MinIO services

- /home/ludvik/vrsky/docker-compose.ui.yml
  - UI development compose

### Infrastructure
- /home/ludvik/vrsky/infrastructure/
  - Kubernetes manifests
  - Helm charts

- /home/ludvik/vrsky/data/
  - Data directory for docker volumes

---

## Documentation Files (Key References)

- /home/ludvik/vrsky/AGENTS.md
  - Project overview and setup instructions
  - Tech stack details

- /home/ludvik/vrsky/ARCHITECTURE_ANALYSIS.md
  - Architecture overview

- /home/ludvik/vrsky/PROJECT_STRUCTURE.md
  - Project layout explanation

- /home/ludvik/vrsky/BACKEND_ARCHITECTURE_COMPLETE_ANALYSIS.md
  - Backend architecture details

- /home/ludvik/vrsky/CODEBASE_ANALYSIS.md
  - Codebase overview

---

## Summary Statistics

### Frontend (UI)
- Total TypeScript/TSX: ~7,960 lines
- Main component: PipelineBuilder.tsx (537 lines)
- Stores (4 total): uiStore, connectionsStore, messageLogStore, metricsStore
- NO persistence in pipeline builder
- Only localStorage usage: theme preference

### Backend (API)
- Main handler: handler.go (620 lines)
- Models: models.go (387 lines)
- Existing REST routes: 13 endpoints for connections
- Database: PostgreSQL with Repository pattern

### Current Gaps
1. No draft save/load API endpoints
2. No localStorage persistence for canvas state
3. No Zustand store for pipeline state
4. No undo/redo history
5. No auto-save mechanism
6. No draft templates

