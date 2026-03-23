# Canvas Persistence Solution Architecture Analysis

**Analysis Date**: March 17, 2026  
**Codebase Status**: Phase 1 - Pipeline Builder implemented with Konva canvas  
**Total UI Code**: ~7,960 lines of TypeScript

---

## Executive Summary

The VRSky UI is built on a React + Zustand architecture with a Konva-based visual pipeline builder. Currently, there is **NO persistent canvas storage**. All pipeline state is ephemeral and lost on page refresh. The analysis reveals that implementing a robust canvas persistence solution requires:

1. Backend API endpoints for saving/loading pipeline drafts
2. Frontend persistence layer using Zustand stores with localStorage
3. Database schema for draft storage
4. Conflict resolution mechanisms for multi-user scenarios

---

## 1. Current State Analysis

### 1.1 PipelineBuilder Component Structure

**File**: `/home/ludvik/vrsky/ui/src/pages/PipelineBuilder.tsx` (537 lines)

**State Management** (all in React component - NOT persisted):
```typescript
const [nodes, setNodes] = useState<Node[]>([])           // Canvas nodes
const [edges, setEdges] = useState<Edge[]>([])           // Canvas edges
const [selectedNode, setSelectedNode] = useState<Node | null>(null)
const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
const [selectedEdgeId, setSelectedEdgeId] = useState<string | null>(null)
const [edgeContextMenu, setEdgeContextMenu] = useState<...>
const [isLoading, setIsLoading] = useState(false)
const [paletteOpen, setPaletteOpen] = useState(true)
const [deployAttempted, setDeployAttempted] = useState(false)
```

**Key Insight**: State is entirely local component state with NO persistence layer.

### 1.2 Node and Edge Storage Model

**File**: `/home/ludvik/vrsky/ui/src/types/pipeline.ts` (22 lines)

```typescript
export interface Node {
  id: string
  type: 'consumer' | 'filter' | 'converter' | 'producer'
  data: {
    label: string
    config?: Record<string, unknown>
    type?: string
  }
  position: { x: number; y: number }
}

export interface Edge {
  id?: string
  source: string
  target: string
}
```

**Current Storage Mechanism**:
- Nodes/edges stored only in memory during canvas session
- When "Deploy" is clicked, serialized to API payload:
```json
{
  "name": "Pipeline {timestamp}",
  "description": "Created via visual pipeline editor",
  "nodes": [
    { "id": "...", "type": "...", "config": {}, "enabled": true }
  ],
  "edges": [
    { "id": "...", "source": "...", "target": "...", "order": 0 }
  ]
}
```

### 1.3 Current Storage/Persistence Usage

**FINDING**: No localStorage or sessionStorage in pipeline builder. Only theme persisted:

**File**: `/home/ludvik/vrsky/ui/src/components/Layout/Header.tsx`
```typescript
localStorage.setItem('theme', 'light')  // Only theme, not pipeline state
```

**CRITICAL GAP**: There is NO:
- Draft saving mechanism
- Session recovery
- Canvas undo/redo
- Auto-save functionality

---

## 2. User Context & Tenant Handling

### 2.1 Tenant Context Architecture

**File**: `/home/ludvik/vrsky/ui/src/config/env.ts` (31 lines)

```typescript
export const config = {
  apiUrl: getEnv('VITE_API_URL', 'http://localhost:3000'),
  wsUrl: getEnv('VITE_WS_URL', 'http://localhost:3000'),
  tenantId: getEnv('VITE_TENANT_ID', 'tenant-1'),  // ← GLOBAL TENANT ID
  logLevel: 'info',
  isDev: true,
  isProd: false,
}
```

**Tenant Injection**:

**File**: `/home/ludvik/vrsky/ui/src/services/api.ts` (57 lines)

```typescript
// Request interceptor: Add X-Tenant-ID header
apiClient.interceptors.request.use(
  (requestConfig) => {
    requestConfig.headers['X-Tenant-ID'] = config.tenantId
    return requestConfig
  },
  (error) => Promise.reject(error)
)
```

**KEY INSIGHT**: Tenant is statically configured per environment, not per user session. Canvas persistence must be scoped to `config.tenantId`.

### 2.2 UI State Management Pattern

**File**: `/home/ludvik/vrsky/ui/src/store/uiStore.ts` (120 lines)

Uses Zustand for UI state:
```typescript
interface UIStore {
  sidebarOpen: boolean
  notifications: Notification[]
  confirmDialog: ConfirmDialogConfig | null
  
  toggleSidebar: () => void
  addNotification: (notification: Omit<Notification, 'id'>) => string
  // ... more methods
}

export const useUIStore = create<UIStore>((set, get) => ({
  // Implementation
}))
```

**Connections Store** (not persisted):

**File**: `/home/ludvik/vrsky/ui/src/store/connectionsStore.ts` (96 lines)

```typescript
interface ConnectionsStore {
  connections: Connection[]
  selectedConnection: Connection | null
  loading: boolean
  error: string | null
  
  setConnections: (connections: Connection[]) => void
  addConnection: (connection: Connection) => void
  // ... more methods
}

export const useConnectionsStore = create<ConnectionsStore>((set, get) => ({
  // Implementation
}))
```

---

## 3. Existing API Architecture

### 3.1 Current REST Endpoints

**File**: `/home/ludvik/vrsky/src/pkg/managementapi/handler.go` (620 lines)

**Registered Routes**:
```
POST   /api/v1/connections              → CreateConnection
GET    /api/v1/connections              → ListConnections
GET    /api/v1/connections/{id}         → GetConnection
PUT    /api/v1/connections/{id}         → UpdateConnection
DELETE /api/v1/connections/{id}         → DeleteConnection
POST   /api/v1/connections/{id}/start   → StartConnection
POST   /api/v1/connections/{id}/stop    → StopConnection
GET    /api/v1/connections/{id}/metrics/stream  → Metrics SSE
GET    /api/v1/connections/{id}/metrics/ws     → Metrics WebSocket
POST   /api/v1/connections/{id}/test-message
POST   /api/v1/connections/{id}/auto-generator/start
POST   /api/v1/connections/{id}/auto-generator/stop
GET    /api/v1/connections/{id}/auto-generator/status
```

**FINDING**: No draft/template save endpoints exist.

### 3.2 Connection Model (Backend)

**File**: `/home/ludvik/vrsky/src/pkg/managementapi/models.go` (387 lines)

```go
type Connection struct {
  ID          string
  TenantID    string
  Name        string
  Description string
  
  // Graph-based model (NEW in Phase 1)
  Nodes []*Node
  Edges []*Edge
  
  Status    string
  CreatedAt time.Time
  UpdatedAt time.Time
  StartedAt *time.Time
  StoppedAt *time.Time
  LastError *string
  
  // Legacy fields (DEPRECATED)
  SourceConfig      SourceConfig
  ConverterConfig   ConverterConfig
  FilterConfig      FilterConfig
  DestinationConfig DestinationConfig
}

type Node struct {
  ID      string
  Type    string
  Config  json.RawMessage
  Enabled bool
}

type Edge struct {
  ID     string
  Source string
  Target string
  Order  int
}
```

---

## 4. Persistence Requirements & Gaps

### 4.1 Missing Functionality

| Feature | Current | Required |
|---------|---------|----------|
| **Draft Saving** | ❌ None | API endpoint + UI | 
| **Auto-save** | ❌ None | Timer-based with Zustand |
| **Canvas State Recovery** | ❌ Lost on refresh | localStorage + Zustand persist |
| **Draft Templates** | ❌ None | API + listing UI |
| **Conflict Resolution** | ❌ N/A | Timestamps + optimistic updates |
| **Undo/Redo** | ❌ None | State history store |
| **Version Control** | ❌ None | Database snapshots |

### 4.2 Local Storage Gaps

**Current localStorage Usage**:
- ✅ Theme preference (`theme` key)

**Missing**:
- ❌ Pipeline draft state (nodes + edges)
- ❌ Editor session state (selected nodes, zoom level, scroll position)
- ❌ UI state (sidebar visibility, palette open)
- ❌ Recent connections list

---

## 5. Data Flow Diagrams

### 5.1 Current Data Flow (No Persistence)

```
┌─────────────────┐
│  Canvas Render  │
│  (PipelineBuilder)
└────────┬────────┘
         │
         ▼
┌─────────────────────────────┐
│  React Component State      │
│  (nodes, edges, selected)   │
│                             │
│  - EPHEMERAL (lost on refresh)
│  - NO localStorage backup
│  - NO Zustand store
└────────┬────────────────────┘
         │
         ▼
┌────────────────────┐
│  Deploy Button     │
│  (POST /connections)
└────────┬───────────┘
         │
         ▼
┌────────────────────────┐
│  Backend API           │
│  (Management API)      │
└────────────────────────┘
```

### 5.2 Proposed Persistence Architecture

```
┌──────────────────────────────────────┐
│  Canvas Editor (PipelineBuilder)     │
│  - Nodes & Edges state               │
│  - Editor state (selection, zoom)    │
└────────────────────┬─────────────────┘
                     │
        ┌────────────┼────────────┐
        │            │            │
        ▼            ▼            ▼
   ┌─────────┐  ┌─────────┐  ┌──────────┐
   │Zustand  │  │Auto-save│  │undo/redo │
   │Pipeline │  │(Interval)  │ stack    │
   │Store    │  └─────────┘  └──────────┘
   └────┬────┘
        │
   ┌────┴──────────────────────────┐
   │                               │
   ▼                               ▼
┌──────────────┐          ┌────────────────┐
│ localStorage │          │ API Endpoints  │
│ (client)     │          │ (backend)      │
│              │          │                │
│ drafts:{     │          │POST /drafts    │
│  tenant:{}   │          │GET  /drafts    │
│}             │          │PUT  /drafts/{id}
└──────────────┘          │DELETE /drafts/{id}
                          └────────────────┘
                                  │
                                  ▼
                          ┌────────────────┐
                          │PostgreSQL      │
                          │                │
                          │drafts table:   │
                          │- id            │
                          │- tenant_id     │
                          │- name          │
                          │- nodes (JSONB) │
                          │- edges (JSONB) │
                          │- created_at    │
                          │- updated_at    │
                          └────────────────┘
```

---

## 6. Recommended Storage Architecture

### 6.1 Frontend: Zustand Pipeline Store

**Location to create**: `/home/ludvik/vrsky/ui/src/store/pipelineStore.ts`

```typescript
interface PipelineState {
  // Canvas state
  nodes: Node[]
  edges: Edge[]
  
  // Editor state
  selectedNodeId: string | null
  selectedEdgeId: string | null
  zoom: number
  panX: number
  panY: number
  paletteOpen: boolean
  
  // Draft management
  currentDraftId: string | null
  isDirty: boolean
  lastSavedAt: number | null
  
  // Undo/redo
  history: HistoryEntry[]
  historyIndex: number
  
  // Actions
  setNodes: (nodes: Node[]) => void
  setEdges: (edges: Edge[]) => void
  updateNodeConfig: (nodeId: string, config: Record<string, unknown>) => void
  addNode: (node: Node) => void
  deleteNode: (nodeId: string) => void
  addEdge: (edge: Edge) => void
  deleteEdge: (edgeId: string) => void
  setSelectedNode: (nodeId: string | null) => void
  setSelectedEdge: (edgeId: string | null) => void
  setViewState: (zoom: number, panX: number, panY: number) => void
  
  // Draft operations
  setCurrentDraftId: (draftId: string | null) => void
  markDirty: () => void
  markClean: () => void
  
  // Undo/redo
  undo: () => void
  redo: () => void
  pushHistory: (entry: HistoryEntry) => void
}

// Persist middleware configuration
const persistConfig = {
  name: 'pipeline-store',
  partialize: (state) => ({
    nodes: state.nodes,
    edges: state.edges,
    zoom: state.zoom,
    panX: state.panX,
    panY: state.panY,
    paletteOpen: state.paletteOpen,
  }),
  storage: createJSONStorage(() => localStorage),
}
```

### 6.2 Backend: Drafts Schema

**New database table** `pipeline_drafts`:

```sql
CREATE TABLE IF NOT EXISTS pipeline_drafts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT,
  nodes JSONB NOT NULL DEFAULT '[]'::jsonb,
  edges JSONB NOT NULL DEFAULT '[]'::jsonb,
  editor_state JSONB,  -- {zoom, panX, panY, paletteOpen}
  is_template BOOLEAN DEFAULT false,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
  updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
  created_by TEXT,
  last_modified_by TEXT,
  
  CONSTRAINT fk_tenant FOREIGN KEY (tenant_id) 
    REFERENCES tenants(id) ON DELETE CASCADE,
  CONSTRAINT unique_draft_per_tenant UNIQUE (tenant_id, name)
);

CREATE INDEX idx_drafts_tenant ON pipeline_drafts(tenant_id);
CREATE INDEX idx_drafts_updated ON pipeline_drafts(updated_at DESC);
```

### 6.3 Backend: API Endpoints

**New endpoints to implement** in handler:

```go
// Draft Management
POST   /api/v1/drafts              → CreateDraft
GET    /api/v1/drafts              → ListDrafts (paginated, per-tenant)
GET    /api/v1/drafts/{id}         → GetDraft
PUT    /api/v1/drafts/{id}         → UpdateDraft (with conflict detection)
DELETE /api/v1/drafts/{id}         → DeleteDraft
POST   /api/v1/drafts/{id}/deploy  → DeployDraft (creates Connection)

// Templates
GET    /api/v1/templates           → ListTemplates (public/system templates)
POST   /api/v1/drafts/{id}/save-as-template  → SaveAsTemplate
```

### 6.4 Request/Response Models

```typescript
// Frontend → Backend
interface CreateDraftRequest {
  name: string
  description?: string
  nodes: Node[]
  edges: Edge[]
  editorState?: {
    zoom: number
    panX: number
    panY: number
    paletteOpen: boolean
  }
}

interface UpdateDraftRequest {
  name?: string
  description?: string
  nodes?: Node[]
  edges?: Edge[]
  editorState?: Record<string, unknown>
  expectedUpdatedAt?: string  // For conflict detection
}

interface DraftResponse {
  id: string
  tenantId: string
  name: string
  description: string
  nodes: Node[]
  edges: Edge[]
  editorState?: Record<string, unknown>
  isTemplate: boolean
  createdAt: string
  updatedAt: string
  createdBy?: string
  lastModifiedBy?: string
}

interface ListDraftsResponse {
  data: DraftResponse[]
  total: number
  page: number
  pageSize: number
}
```

---

## 7. Architecture Patterns Used in Codebase

### 7.1 Zustand Pattern (Established)

**Location**: `/home/ludvik/vrsky/ui/src/store/`

All stores follow this pattern:
```typescript
export const useXxxStore = create<Interface>((set, get) => ({
  // State
  field: initialValue,
  
  // Actions
  setField: (value) => set({ field: value }),
  
  // Selectors
  getField: () => get().field,
}))
```

**Implication**: New PipelineStore should follow same pattern with Zustand.

### 7.2 Repository Pattern (Backend)

**File**: `/home/ludvik/vrsky/src/pkg/managementapi/repository.go`

Database access is abstracted through interfaces:
```go
type Repository interface {
  CreateConnection(ctx context.Context, conn *Connection) error
  GetConnection(ctx context.Context, id string) (*Connection, error)
  ListConnections(ctx context.Context, tenantID string) ([]*Connection, error)
  // ...
}

type PostgresRepository struct {
  db *sql.DB
}

func (r *PostgresRepository) CreateConnection(ctx context.Context, conn *Connection) error {
  // Implementation
}
```

**Implication**: Add `CreateDraft`, `GetDraft`, `ListDrafts`, `UpdateDraft`, `DeleteDraft` methods to Repository interface.

### 7.3 Handler Pattern (Backend)

**File**: `/home/ludvik/vrsky/src/pkg/managementapi/handler.go`

HTTP handlers follow this pattern:
```go
func (h *Handler) CreateConnection(w http.ResponseWriter, r *http.Request) {
  ctx := r.Context()
  
  // Extract tenant
  tenantID, err := GetTenantIDFromContext(ctx)
  
  // Parse request
  var req CreateConnectionRequest
  if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
    // Handle error
  }
  
  // Validate
  if err := h.validator.ValidateConnection(conn); err != nil {
    // Handle error
  }
  
  // Persist
  if err := h.repo.CreateConnection(ctx, conn); err != nil {
    // Handle error
  }
  
  // Respond
  writeJSON(w, http.StatusCreated, SuccessResponse{Data: conn})
}
```

**Implication**: Draft handlers should follow identical pattern.

### 7.4 API Client Pattern (Frontend)

**File**: `/home/ludvik/vrsky/ui/src/services/connectionService.ts`

```typescript
export async function createConnection(data: unknown): Promise<Connection> {
  const response = await apiClient.post<CreateConnectionResponse>(
    '/api/v1/connections',
    data
  )
  return response.data as unknown as Connection
}

export const connectionService = {
  create: createConnection,
  get: getConnection,
  list: listConnections,
  update: updateConnection,
  delete: deleteConnection,
  start: startConnection,
  stop: stopConnection,
}
```

**Implication**: Create `draftService.ts` with similar structure.

---

## 8. Integration Points & Considerations

### 8.1 Multi-Tenancy

- ✅ Already implemented via X-Tenant-ID header
- ✅ TenantID middleware validates all requests
- **For drafts**: All draft queries scoped by `WHERE tenant_id = $1`
- **For UI**: localStorage should use key like `drafts:${config.tenantId}:{draftId}`

### 8.2 Conflict Resolution

**Scenario**: User A saves draft, User B saves same draft simultaneously

**Solution**: Last-write-wins with optimistic updates
```typescript
// In UpdateDraftRequest:
expectedUpdatedAt?: string

// In backend:
if (draft.UpdatedAt.Unix() != expectedUpdatedAt) {
  return 409 Conflict
}
```

### 8.3 Auto-Save Mechanism

```typescript
// In pipelineStore:
useEffect(() => {
  const interval = setInterval(() => {
    if (isDirty && currentDraftId) {
      updateDraftAPI(currentDraftId, state)
        .then(() => markClean())
        .catch(err => showErrorNotification('Auto-save failed', err.message))
    }
  }, 30000)  // 30 seconds
  
  return () => clearInterval(interval)
}, [isDirty, currentDraftId])
```

### 8.4 Undo/Redo Implementation

```typescript
interface HistoryEntry {
  nodes: Node[]
  edges: Edge[]
  timestamp: number
}

// In store:
pushHistory: (action: HistoryEntry) => {
  const newHistory = state.history.slice(0, state.historyIndex + 1)
  newHistory.push(action)
  set({
    history: newHistory,
    historyIndex: newHistory.length - 1,
  })
}

undo: () => {
  if (state.historyIndex > 0) {
    const newIndex = state.historyIndex - 1
    const entry = state.history[newIndex]
    set({
      nodes: entry.nodes,
      edges: entry.edges,
      historyIndex: newIndex,
    })
  }
}
```

---

## 9. File Structure & Creation Plan

### 9.1 Files to Create/Modify (Frontend)

**New Files**:
```
ui/src/store/pipelineStore.ts          - Zustand store with persistence
ui/src/services/draftService.ts        - API client for drafts
ui/src/hooks/useDraftAutoSave.ts       - Auto-save hook
ui/src/hooks/useDraftHistory.ts        - Undo/redo hook
ui/src/components/Pipeline/SaveDraftDialog.tsx
ui/src/components/Pipeline/LoadDraftDialog.tsx
ui/src/types/draft.ts                  - TypeScript definitions
```

**Modified Files**:
```
ui/src/pages/PipelineBuilder.tsx       - Integrate store and auto-save
ui/src/pages/PipelineBuilderPage.tsx   - Add draft context provider
ui/src/App.tsx                         - Add draft recovery on app init
ui/src/types/models.ts                 - Add Draft model
```

### 9.2 Files to Create/Modify (Backend)

**New Files**:
```
src/pkg/managementapi/draft.go         - Draft model & methods
src/pkg/managementapi/draft_repository.go - DB operations
src/pkg/managementapi/draft_handler.go - HTTP handlers
```

**Modified Files**:
```
src/pkg/managementapi/repository.go    - Add draft methods to interface
src/pkg/managementapi/handler.go       - Add draft handler methods
src/pkg/managementapi/handler.go (RegisterRoutes) - Register draft routes
src/cmd/management-api/main.go         - DB migrations
```

### 9.3 Database Migrations

```sql
-- Migration: 002_create_pipeline_drafts_table.sql
BEGIN;

CREATE TABLE IF NOT EXISTS pipeline_drafts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT,
  nodes JSONB NOT NULL DEFAULT '[]'::jsonb,
  edges JSONB NOT NULL DEFAULT '[]'::jsonb,
  editor_state JSONB,
  is_template BOOLEAN DEFAULT false,
  created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
  updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
  created_by TEXT,
  last_modified_by TEXT,
  
  CONSTRAINT fk_tenant FOREIGN KEY (tenant_id) 
    REFERENCES tenants(id) ON DELETE CASCADE,
  CONSTRAINT unique_draft_per_tenant UNIQUE (tenant_id, name)
);

CREATE INDEX idx_drafts_tenant ON pipeline_drafts(tenant_id);
CREATE INDEX idx_drafts_updated ON pipeline_drafts(updated_at DESC);

COMMIT;
```

---

## 10. Security & Privacy Considerations

### 10.1 Tenant Isolation

- ❌ **DO NOT** query drafts without tenant filter
- ✅ **DO** always validate `GetTenantIDFromContext()` matches draft owner
- ✅ **DO** scope all localStorage keys to tenant ID

### 10.2 Sensitive Data in Drafts

**Config contains secrets**:
- HTTP auth tokens (Bearer, API keys)
- Database connection strings
- Encryption keys

**Mitigation**:
1. Encrypt config fields in database (AES-256-GCM)
2. Never log config values
3. Sanitize config in API responses (return placeholder for secrets)
4. Use environment variables for sensitive defaults

### 10.3 Draft Versioning & Auditing

```typescript
interface DraftAuditEntry {
  draftId: string
  action: 'created' | 'updated' | 'deleted' | 'deployed'
  changedAt: string
  changedBy: string
  previousState?: Record<string, unknown>
}

// Log all changes to audit table
```

---

## 11. Performance Considerations

### 11.1 localStorage Size Limits

- **Browser limit**: ~5-10MB per origin
- **Pipeline estimate**: ~50KB per draft (nodes + edges + config)
- **Safe storage**: ~100-200 drafts locally

**Decision**: Keep only recent 5 drafts in localStorage, rest on backend.

### 11.2 Auto-Save Throttling

```typescript
// Debounce rapid changes
const debouncedAutoSave = debounce(() => {
  updateDraftAPI()
}, 3000)

// Don't save if nothing changed
if (isDirty) debouncedAutoSave()
```

### 11.3 Network Optimization

- Use PATCH for incremental updates (partial delta sync)
- Compress large node/edge arrays
- Implement background sync queue

---

## 12. Recommended Implementation Sequence

### Phase 1: Foundation (Week 1-2)
1. Create Zustand pipelineStore
2. Create draftService API client
3. Add draft routes to backend handler
4. Create pipeline_drafts database table
5. Implement CRUD operations (Create, Read, Update, Delete)

### Phase 2: Frontend Integration (Week 2-3)
1. Replace component state with Zustand store
2. Add Save/Load dialogs
3. Integrate localStorage persistence
4. Add auto-save hook
5. Test draft recovery on page refresh

### Phase 3: Advanced Features (Week 3-4)
1. Implement undo/redo history
2. Add conflict detection
3. Implement draft templates
4. Add draft versioning/auditing
5. Add encryption for sensitive config

### Phase 4: Polish (Week 4)
1. Performance optimization
2. Error handling & recovery
3. User documentation
4. Load testing

---

## 13. Key Files Summary

### Frontend Key Files

| File | Lines | Purpose |
|------|-------|---------|
| `/ui/src/pages/PipelineBuilder.tsx` | 537 | Main canvas component - needs store integration |
| `/ui/src/store/uiStore.ts` | 120 | Zustand UI store - reference pattern |
| `/ui/src/store/connectionsStore.ts` | 96 | Zustand connections store - reference pattern |
| `/ui/src/services/connectionService.ts` | 110 | API client pattern to follow |
| `/ui/src/types/pipeline.ts` | 22 | Node/Edge types |
| `/ui/src/types/models.ts` | 231 | API response types |
| `/ui/src/config/env.ts` | 31 | Tenant config injection |

### Backend Key Files

| File | Lines | Purpose |
|------|-------|---------|
| `/src/pkg/managementapi/handler.go` | 620 | HTTP handlers - needs draft handlers |
| `/src/pkg/managementapi/models.go` | 387 | Connection models - needs draft models |
| `/src/pkg/managementapi/repository.go` | varies | DB interface - needs draft methods |
| `/src/pkg/managementapi/postgres_repository.go` | varies | PostgreSQL impl - needs draft SQL |
| `/src/cmd/management-api/main.go` | 218 | Server setup & routes |

---

## 14. Conclusion

**Current State**: Canvas state is entirely ephemeral with no persistence mechanism.

**Recommended Approach**: 
1. Implement Zustand store with localStorage backup (frontend)
2. Create draft management API endpoints (backend)
3. Add pipeline_drafts database table
4. Integrate auto-save with conflict resolution

**Estimated Effort**: 
- Backend: 20-30 hours (CRUD ops, migrations, tests)
- Frontend: 15-20 hours (store, components, hooks)
- Total: 35-50 hours (including testing & optimization)

**Breaking Changes**: None - fully backward compatible. Drafts are new feature, not modification to existing Connection model.

