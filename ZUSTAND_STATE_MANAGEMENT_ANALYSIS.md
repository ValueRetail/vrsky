# VRSky Codebase Exploration: Zustand Store & State Management Analysis

**Date**: March 17, 2026  
**Exploration Focus**: Zustand patterns, node/edge management, localStorage usage, backend API structure, tenant context

---

## 1. ZUSTAND PATTERN DETAILS

### 1.1 Store Architecture Overview

**File Location**: `/home/ludvik/vrsky/ui/src/store/`

The codebase uses **4 separate Zustand stores**, each with distinct responsibilities:

#### **A. UIStore** (`uiStore.ts`)
- **Purpose**: UI state management (notifications, dialogs, sidebar)
- **Pattern Type**: Flat state with action methods
- **State Properties**:
  - `sidebarOpen: boolean` - Sidebar visibility
  - `notifications: Notification[]` - Toast notifications queue
  - `confirmDialog: ConfirmDialogConfig | null` - Modal dialogs

**Key Actions**:
```typescript
// Sidebar management
toggleSidebar(): void
setSidebarOpen(open: boolean): void

// Notification management
addNotification(notification: Omit<Notification, 'id'>): string
removeNotification(id: string): void
clearNotifications(): void

// Confirm dialog
showConfirmDialog(config: ConfirmDialogConfig): void
hideConfirmDialog(): void

// Helper methods
getNotificationCount(): number
showSuccessNotification(title, message, duration?): string
showErrorNotification(title, message, duration?): string
showWarningNotification(title, message, duration?): string
showInfoNotification(title, message, duration?): string
```

**Implementation Pattern**:
```typescript
export const useUIStore = create<UIStore>((set, get) => ({
  // Initial state
  sidebarOpen: true,
  notifications: [],
  
  // Actions using set() and get()
  addNotification: (notification) => {
    const id = `notification-${Date.now()}-${Math.random()}`
    set((state) => ({
      notifications: [...state.notifications, newNotification],
    }))
    
    // Auto-remove with timeout
    if (newNotification.duration && newNotification.duration > 0) {
      setTimeout(() => {
        get().removeNotification(id)
      }, newNotification.duration)
    }
    return id
  },
}))
```

#### **B. ConnectionsStore** (`connectionsStore.ts`)
- **Purpose**: Connection CRUD operations and list management
- **Pattern Type**: Entity store with filtering helpers
- **State Properties**:
  - `connections: Connection[]` - List of all connections
  - `selectedConnection: Connection | null` - Currently selected
  - `loading: boolean` - Loading state
  - `error: string | null` - Error state

**Key Actions**:
```typescript
// CRUD operations
setConnections(connections: Connection[]): void
addConnection(connection: Connection): void
updateConnection(connection: Connection): void
deleteConnection(id: string): void

// Selection & state
setSelectedConnection(connection: Connection | null): void
setLoading(loading: boolean): void
setError(error: string | null): void
clear(): void

// Helpers (query methods)
getConnectionById(id: string): Connection | undefined
getRunningConnections(): Connection[]
getStoppedConnections(): Connection[]
getErrorConnections(): Connection[]
```

#### **C. MetricsStore** (`metricsStore.ts`)
- **Purpose**: Real-time metrics from WebSocket/SSE streams
- **Pattern Type**: Map-based state for efficient lookups
- **State Properties**:
  - `metricsMap: Map<string, ConnectionMetrics>` - Metrics by connection ID
  - `updateTimestamp: Record<string, number>` - Last update times

**Key Actions**:
```typescript
updateMetrics(connectionId: string, metrics: ConnectionMetrics): void
setMetrics(metricsMap: Map<string, ConnectionMetrics>): void
clearMetrics(): void
removeMetrics(connectionId: string): void

// Helpers
getMetricsByConnectionId(connectionId: string): ConnectionMetrics | undefined
getAllMetrics(): ConnectionMetrics[]
getLastUpdateTime(connectionId: string): number | undefined
```

**Note**: Uses `Map` instead of objects for O(1) lookups on thousands of connections.

#### **D. MessageLogStore** (`messageLogStore.ts`)
- **Purpose**: Test message logs for connection debugging
- **Pattern Type**: Map-based state with size limits
- **State Properties**:
  - `logs: Map<string, MessageLogEntry[]>` - Logs by connection ID

**Key Actions**:
```typescript
addMessage(connectionId: string, message: MessageLogEntry): void
addMessages(connectionId: string, messages: MessageLogEntry[]): void
getMessages(connectionId: string): MessageLogEntry[]
clearMessages(connectionId: string): void
clearAllMessages(): void

// Helper
getRecentMessages(connectionId: string, limit: number): MessageLogEntry[]
```

**Memory Management**: Keeps only last 1000 messages per connection.

### 1.2 Zustand Pattern Characteristics

All stores follow this pattern:
```typescript
export const useXxxStore = create<XxxStore>((set, get) => ({
  // Initial state
  stateProperty: value,
  
  // Synchronous mutations via set()
  mutateState: (arg) => set((state) => ({
    stateProperty: newValue,
  })),
  
  // Queries via get()
  getState: () => {
    const { stateProperty } = get()
    return stateProperty
  },
}))
```

**Key Characteristics**:
- ✅ No DevTools setup
- ✅ No middleware (persist, immer, etc.)
- ✅ Direct state mutations via `set((state) => ({...}))`
- ✅ Read-only access via `get()`
- ✅ Automatic subscription for React components
- ✅ Usage: `const { state, action } = useStore()`

---

## 2. NODES & EDGES STATE MANAGEMENT IN PIPELINEBUILDER

### 2.1 Current State Management (React Hooks)

**File**: `/home/ludvik/vrsky/ui/src/pages/PipelineBuilder.tsx`

**State Variables** (NOT in Zustand):
```typescript
const [nodes, setNodes] = useState<Node[]>([])
const [edges, setEdges] = useState<Edge[]>([])
const [selectedNode, setSelectedNode] = useState<Node | null>(null)
const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
const [selectedEdgeId, setSelectedEdgeId] = useState<string | null>(null)
const [edgeContextMenu, setEdgeContextMenu] = useState<{ edgeId: string; x: number; y: number } | null>(null)
const [isLoading, setIsLoading] = useState(false)
const [paletteOpen, setPaletteOpen] = useState(true)
const [deployAttempted, setDeployAttempted] = useState(false)
const [canvasWidth, setCanvasWidth] = useState(window.innerWidth)
```

### 2.2 Node Structure

**Type Definition** (`/home/ludvik/vrsky/ui/src/types/pipeline.ts`):
```typescript
interface Node {
  id: string                        // e.g., "consumer-1234567890-0.5"
  type: 'consumer' | 'filter' | 'converter' | 'producer'
  data: {
    label: string                   // e.g., "Consumer 1"
    config?: Record<string, unknown> // Component configuration
    type?: string
  }
  position: { x: number; y: number } // Canvas position (snapped to 20px grid)
}
```

**Node Creation Process** (from drag-drop):
```typescript
const handleDrop = (event: React.DragEvent<HTMLDivElement>) => {
  const nodeType = event.dataTransfer.getData('nodeType')
  const rect = canvasContainer.current.getBoundingClientRect()
  
  // Snap to grid (20px)
  const x = Math.round(event.clientX / GRID_SIZE) * GRID_SIZE
  const y = Math.round(event.clientY / GRID_SIZE) * GRID_SIZE
  
  const newNode: Node = {
    id: `${nodeType}-${Date.now()}-${Math.random()}`, // Unique ID
    type: nodeType,
    data: {
      label: getNodeLabel(nodeType, nodes), // e.g., "Consumer 1"
      type: nodeType,
      config: {},
    },
    position: { x, y },
  }
  
  setNodes((nds) => [...nds, newNode])
}
```

### 2.3 Edge Structure

**Type Definition**:
```typescript
interface Edge {
  id?: string      // Optional; generated if missing
  source: string   // Source node ID
  target: string   // Target node ID
}
```

**Edge Creation** (via custom hook `useConnectionDrawing`):
- Drawn via Konva canvas mouse events
- Source → Target connection
- Stored in state immediately after drawing

### 2.4 State Mutations

**Add Node**:
```typescript
setNodes((nds) => [...nds, newNode])
```

**Update Node Config**:
```typescript
const updateNodeConfig = (config: Record<string, unknown>) => {
  if (!selectedNode) return
  setNodes((nds) =>
    nds.map((node) =>
      node.id === selectedNode.id
        ? { ...node, data: { ...node.data, config } }
        : node
    )
  )
}
```

**Delete Node**:
```typescript
setNodes((nds) => nds.filter((n) => n.id !== selectedNode.id))
setEdges((eds) =>
  eds.filter((e) => e.source !== selectedNode.id && e.target !== selectedNode.id)
)
```

**Add Edge**:
```typescript
// From useConnectionDrawing hook
setEdges((prevEdges) => [
  ...prevEdges,
  {
    id: `edge-${Date.now()}`,
    source: connectionStart.nodeId,
    target: connectionPreviewEnd.nodeId,
  },
])
```

**Delete Edge**:
```typescript
setEdges((eds) => eds.filter((e) => e.id !== edgeId))
```

### 2.5 Validation

**File**: `/home/ludvik/vrsky/ui/src/utils/validation.ts`

Validates in real-time via `useMemo`:
```typescript
const validationResult: ValidationResult = useMemo(() => {
  return validatePipelineConnections(nodes, edges)
}, [nodes, edges])

// Validation checks:
// - At least one consumer node
// - At least one producer node
// - All consumers connected to downstream nodes
// - All producers have at least one incoming edge
// - No cycles or orphaned nodes
```

### 2.6 Data Transformation for Backend

**Payload Format** (sent to `/api/v1/connections`):
```typescript
const buildConnectionPayload = () => {
  return {
    name: `Pipeline ${new Date().toLocaleTimeString()}`,
    description: 'Created via visual pipeline editor',
    nodes: nodes.map((node) => ({
      id: node.id,
      type: node.type,
      config: node.data.config || {},
      enabled: true,
    })),
    edges: edges.map((edge, index) => ({
      id: edge.id || `edge-${index}`,
      source: edge.source,
      target: edge.target,
      order: index,
    })),
  }
}
```

---

## 3. LOCALSTORAGE USAGE

### 3.1 Current Usage

**Location**: Theme persistence only

**File**: `/home/ludvik/vrsky/ui/src/main.tsx`
```typescript
// Initialize dark mode from localStorage on app startup
if (typeof window !== 'undefined') {
  const savedTheme = localStorage.getItem('theme')
  const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
  const isDark = savedTheme === 'dark' || (savedTheme === null && prefersDark)
  
  if (isDark) {
    document.documentElement.classList.add('dark')
  }
}
```

**File**: `/home/ludvik/vrsky/ui/src/components/Layout/Header.tsx`
```typescript
const toggleDarkMode = () => {
  const html = document.documentElement
  if (html.classList.contains('dark')) {
    html.classList.remove('dark')
    try {
      localStorage.setItem('theme', 'light')
    } catch (e) {
      console.warn('localStorage not available:', e)
    }
  } else {
    html.classList.add('dark')
    try {
      localStorage.setItem('theme', 'dark')
    } catch (e) {
      console.warn('localStorage not available:', e)
    }
  }
}
```

**Key Properties**:
- ✅ Error handling for private/sandboxed contexts
- ✅ Fallback to system preference if no saved theme
- ✅ Only theme preference persisted (no connection data)

### 3.2 What's NOT Persisted

❌ **Nodes/Edges state** - Lost on page reload  
❌ **Connection list** - Fetched from backend on load  
❌ **Metrics** - Real-time from WebSocket  
❌ **Form data** - Only in component state  

**Design Philosophy**: **Ephemeral UI state** - truths live on backend

---

## 4. BACKEND CONNECTION SAVE ENDPOINTS

### 4.1 API Structure

**Base Path**: `/api/v1/connections`  
**Authentication**: X-Tenant-ID header (required)

### 4.2 Connection Endpoints

#### **POST /api/v1/connections** - Create Connection
**Frontend Call** (`/home/ludvik/vrsky/ui/src/services/connectionService.ts`):
```typescript
export async function createConnection(data: unknown): Promise<Connection> {
  const response = await apiClient.post<CreateConnectionResponse>(
    '/api/v1/connections',
    data
  )
  return response.data as unknown as Connection
}
```

**Backend Handler** (`/home/ludvik/vrsky/src/pkg/managementapi/handler.go`):
```go
func (h *Handler) CreateConnection(w http.ResponseWriter, r *http.Request) {
  // 1. Extract tenant ID from context (set by middleware)
  tenantID, err := GetTenantIDFromContext(ctx)
  
  // 2. Parse request body
  var req CreateConnectionRequest
  if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
    writeError(w, http.StatusBadRequest, "InvalidJSON", ...)
    return
  }
  
  // 3. Create connection object
  conn := NewConnection(tenantID, req)
  
  // 4. Validate topology (DAG validation)
  if len(conn.Nodes) > 0 {
    if err := h.validator.ValidateDAG(conn); err != nil {
      // Return validation errors
      writeError(w, http.StatusBadRequest, "DAGValidationError", ...)
      return
    }
  }
  
  // 5. Save to database
  if err := h.repo.CreateConnection(ctx, conn); err != nil {
    writeError(w, http.StatusInternalServerError, "DatabaseError", ...)
    return
  }
  
  // 6. Create event log
  event := NewConnectionEvent(conn.ID, conn.TenantID, "created", eventData)
  h.repo.CreateConnectionEvent(ctx, event)
  
  // 7. Return created connection
  w.Header().Set("Location", fmt.Sprintf("/api/v1/connections/%s", conn.ID))
  writeJSON(w, http.StatusCreated, SuccessResponse{Data: conn})
}
```

**Request Payload Structure**:
```typescript
interface CreateConnectionRequest {
  name: string
  description: string
  nodes: Array<{
    id: string
    type: 'consumer' | 'filter' | 'converter' | 'producer'
    config: Record<string, unknown>
    enabled: boolean
  }>
  edges: Array<{
    id: string
    source: string
    target: string
    order: number
  }>
}
```

**Response Structure**:
```json
{
  "data": {
    "id": "conn-uuid",
    "tenant_id": "tenant-1",
    "name": "Pipeline 14:30:45",
    "description": "Created via visual pipeline editor",
    "status": "stopped",
    "nodes": [...],
    "edges": [...],
    "created_at": "2026-03-17T14:30:45Z",
    "updated_at": "2026-03-17T14:30:45Z"
  }
}
```

#### **GET /api/v1/connections** - List Connections
```typescript
export async function listConnections(
  page?: number,
  pageSize?: number
): Promise<ListConnectionsResponse> {
  const params = new URLSearchParams()
  if (page !== undefined) params.append('page', page.toString())
  if (pageSize !== undefined) params.append('page_size', pageSize.toString())
  
  const url = `/api/v1/connections?${params.toString()}`
  const response = await apiClient.get<ListConnectionsResponse>(url)
  return response.data
}
```

#### **GET /api/v1/connections/{id}** - Get Single Connection
```typescript
export async function getConnection(id: string): Promise<Connection> {
  const response = await apiClient.get<GetConnectionResponse>(
    `/api/v1/connections/${id}`
  )
  return response.data as unknown as Connection
}
```

#### **PUT /api/v1/connections/{id}** - Update Connection
```typescript
export async function updateConnection(
  id: string,
  data: unknown
): Promise<Connection> {
  const response = await apiClient.put<UpdateConnectionResponse>(
    `/api/v1/connections/${id}`,
    data
  )
  return response.data as unknown as Connection
}
```

#### **DELETE /api/v1/connections/{id}** - Delete Connection
```typescript
export async function deleteConnection(id: string): Promise<void> {
  await apiClient.delete<DeleteConnectionResponse>(`/api/v1/connections/${id}`)
}
```

#### **POST /api/v1/connections/{id}/start** - Start Connection
```typescript
export async function startConnection(id: string): Promise<Connection> {
  await apiClient.post<StartConnectionResponse>(
    `/api/v1/connections/${id}/start`,
    {}
  )
  return getConnection(id)
}
```

#### **POST /api/v1/connections/{id}/stop** - Stop Connection
```typescript
export async function stopConnection(id: string): Promise<Connection> {
  await apiClient.post<StopConnectionResponse>(
    `/api/v1/connections/${id}/stop`,
    {}
  )
  return getConnection(id)
}
```

### 4.3 Metrics Endpoints

#### **GET /api/v1/connections/{id}/metrics**
```typescript
export async function getConnectionMetrics(
  connectionId: string
): Promise<ConnectionMetricsResponse> {
  const response = await apiClient.get<ConnectionMetricsResponse>(
    `/api/v1/connections/${connectionId}/metrics`
  )
  return response.data
}
```

### 4.4 Test Data Endpoints

#### **POST /api/v1/connections/{id}/test-message**
```typescript
export async function sendTestMessage(
  connectionId: string,
  message: string
): Promise<void> {
  await apiClient.post<TestMessageResponse>(
    `/api/v1/connections/${connectionId}/test-message`,
    { payload: JSON.parse(message) }
  )
}
```

#### **POST /api/v1/connections/{id}/auto-generator/start**
```typescript
export async function startAutoGenerator(
  connectionId: string,
  rate: number = 1
): Promise<void> {
  await apiClient.post<StartGeneratorResponse>(
    `/api/v1/connections/${connectionId}/auto-generator/start`,
    { rate_per_second: rate }
  )
}
```

#### **POST /api/v1/connections/{id}/auto-generator/stop**
```typescript
export async function stopAutoGenerator(connectionId: string): Promise<void> {
  await apiClient.post<StopGeneratorResponse>(
    `/api/v1/connections/${connectionId}/auto-generator/stop`,
    {}
  )
}
```

#### **GET /api/v1/connections/{id}/auto-generator/status**
```typescript
export async function getAutoGeneratorStatus(
  connectionId: string
): Promise<AutoGeneratorStatusResponse> {
  const response = await apiClient.get<AutoGeneratorStatusResponse>(
    `/api/v1/connections/${connectionId}/auto-generator/status`
  )
  return response.data
}
```

### 4.5 Real-time Metrics Stream (SSE)

**URL**: `/api/v1/connections/{id}/metrics/stream`  
**Protocol**: Server-Sent Events (not WebSocket)

**Implementation** (`/home/ludvik/vrsky/ui/src/services/websocket.ts`):
```typescript
const url = `${config.wsUrl}/api/v1/connections/${connectionId}/metrics/stream`
const eventSource = new EventSource(url)

eventSource.onmessage = (event) => {
  const message = JSON.parse(event.data) as {
    type: 'metrics'
    data: ConnectionMetricsResponse
  }
  if (message.type === 'metrics') {
    onMessageCallback(message.data)
  }
}
```

---

## 5. TENANT CONTEXT & X-TENANT-ID HEADER

### 5.1 Tenant Configuration

**File**: `/home/ludvik/vrsky/ui/src/config/env.ts`

```typescript
export const config = {
  apiUrl: getEnv('VITE_API_URL', 'http://localhost:3000'),
  wsUrl: getEnv('VITE_WS_URL', 'http://localhost:3000'),
  tenantId: getEnv('VITE_TENANT_ID', 'tenant-1'),  // ← Single tenant per deployment
  logLevel: getEnv('VITE_LOG_LEVEL', 'info'),
  isDev: import.meta.env.DEV,
  isProd: import.meta.env.PROD,
}
```

**Environment Variables** (.env or .env.local):
```bash
VITE_API_URL=http://localhost:3000
VITE_WS_URL=http://localhost:3000
VITE_TENANT_ID=tenant-1
VITE_LOG_LEVEL=info
```

### 5.2 Header Injection

**File**: `/home/ludvik/vrsky/ui/src/services/api.ts`

```typescript
const apiClient: AxiosInstance = axios.create({
  baseURL: config.apiUrl,
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json',
  },
})

// Request interceptor: Add X-Tenant-ID header to ALL requests
apiClient.interceptors.request.use(
  (requestConfig) => {
    requestConfig.headers['X-Tenant-ID'] = config.tenantId
    return requestConfig
  },
  (error) => Promise.reject(error)
)
```

**Result**: Every API request automatically includes:
```http
X-Tenant-ID: tenant-1
```

### 5.3 Backend Tenant Validation

**File**: `/home/ludvik/vrsky/src/cmd/management-api/auth.go`

```go
// TenantIDMiddleware validates X-Tenant-ID header and stores in context
func TenantIDMiddleware(tenantHeader string) func(http.Handler) http.Handler {
  return func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
      tenantID := r.Header.Get(tenantHeader)
      if tenantID == "" {
        http.Error(w, "Missing tenant ID", http.StatusUnauthorized)
        return
      }
      
      // Store in context
      ctx := context.WithValue(r.Context(), TenantIDContextKey, tenantID)
      next.ServeHTTP(w, r.WithContext(ctx))
    })
  }
}

// GetTenantIDFromContext retrieves from request context
func GetTenantIDFromContext(ctx context.Context) (string, error) {
  tenantID, ok := ctx.Value(TenantIDContextKey).(string)
  if !ok {
    return "", fmt.Errorf("tenant ID not found in context")
  }
  return tenantID, nil
}
```

### 5.4 Backend Connection Isolation

**Database Queries**:
```sql
-- All queries filtered by tenant_id
SELECT * FROM connections WHERE tenant_id = $1 AND id = $2

-- Implicit multi-tenancy enforcement
INSERT INTO connections (id, tenant_id, name, ...)
VALUES ($1, $2, $3, ...)
```

**NATS Account Isolation** (Phase 1E+):
```go
// Each tenant gets isolated NATS account
// Messages never cross tenant boundaries via NATS
```

---

## 6. FRONTEND SERVICES ARCHITECTURE

### 6.1 Service Layer Organization

**Location**: `/home/ludvik/vrsky/ui/src/services/`

```
services/
├── api.ts                    ← Axios instance with interceptors
├── connectionService.ts      ← CRUD operations
├── metricsService.ts         ← Metrics queries
├── testDataService.ts        ← Test data generation
└── websocket.ts              ← SSE/metrics streaming
```

### 6.2 API Client Pattern

**File**: `api.ts`

```typescript
import axios, { AxiosError } from 'axios'
import { config } from '@/config/env'

const apiClient: AxiosInstance = axios.create({
  baseURL: config.apiUrl,
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json',
  },
})

// Request interceptor: Tenant header + error handling
apiClient.interceptors.request.use(
  (requestConfig) => {
    requestConfig.headers['X-Tenant-ID'] = config.tenantId
    return requestConfig
  },
  (error) => Promise.reject(error)
)

// Response interceptor: Global error handling
apiClient.interceptors.response.use(
  (response) => response,
  (error: AxiosError) => {
    if (!error.response) {
      return Promise.reject({
        code: 'NETWORK_ERROR',
        message: 'Network request failed',
      })
    }
    
    const status = error.response.status
    const data = error.response.data as Record<string, unknown>
    
    return Promise.reject({
      code: `HTTP_${status}`,
      message: data.message || `HTTP Error ${status}`,
      details: { status, ...data },
    })
  }
)

export default apiClient
```

### 6.3 Service Patterns

All services follow this pattern:

```typescript
// Named exports for flexibility
export async function createConnection(data: unknown): Promise<Connection> {
  const response = await apiClient.post<CreateConnectionResponse>(
    '/api/v1/connections',
    data
  )
  return response.data as unknown as Connection
}

// Barrel export for grouped access
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

### 6.4 Type Safety

**Response Types** (`/home/ludvik/vrsky/ui/src/types/api.ts`):

```typescript
export interface APIResponse<T = unknown> {
  data: T
  error?: string
  status: number
}

export interface CreateConnectionResponse {
  id: string
  tenant_id: string
  name: string
  description: string
  status: 'stopped' | 'running' | 'error'
  created_at: string
  updated_at: string
}

export interface ListConnectionsResponse {
  connections: GetConnectionResponse[]
  total: number
  page: number
  page_size: number
}
```

---

## 7. DEPLOYMENT & CONFIGURATION

### 7.1 Frontend Environment Setup

**.env.local**:
```bash
VITE_API_URL=http://localhost:3000
VITE_WS_URL=http://localhost:3000
VITE_TENANT_ID=tenant-1
VITE_LOG_LEVEL=info
```

**.env.prod**:
```bash
VITE_API_URL=https://api.vrsky.example.com
VITE_WS_URL=https://api.vrsky.example.com
VITE_TENANT_ID=tenant-1
VITE_LOG_LEVEL=warn
```

### 7.2 Backend Configuration

**File**: `/home/ludvik/vrsky/src/cmd/management-api/config.go`

```go
type Config struct {
  ServiceName   string
  Version       string
  ListenAddr    string      // :3000
  DatabaseURL   string      // PostgreSQL connection
  NATSUrl       string      // nats://localhost:4222
  TenantHeader  string      // "X-Tenant-ID"
  CORSOrigins   []string
  ReadTimeout   time.Duration
  WriteTimeout  time.Duration
}
```

---

## 8. SUMMARY TABLE

| Aspect | Current Implementation | Pattern |
|--------|----------------------|---------|
| **UI State** | `useUIStore` (Zustand) | Flat, no middleware |
| **Connection List** | `useConnectionsStore` (Zustand) | Entity store with helpers |
| **Metrics** | `useMetricsStore` (Zustand) | Map-based for performance |
| **Pipeline Editor** | React `useState` | Component-level only |
| **Node/Edge Persistence** | None (ephemeral) | Sent to backend on deploy |
| **localStorage** | Theme only | Error-safe access |
| **Tenant Context** | Config + Axios interceptor | Implicit on all requests |
| **API Base** | `/api/v1/connections` | REST with CRUD ops |
| **Streaming** | SSE (EventSource) | Per-connection `/metrics/stream` |
| **Error Handling** | Axios interceptor + notifications | Global + local toasts |

---

## 9. KEY FINDINGS

✅ **Strengths**:
- Clean separation of concerns (stores by responsibility)
- Type-safe API responses
- Tenant context automatically injected
- Error handling at multiple layers
- Real-time metrics via SSE (not polling)
- Graceful degradation (localStorage errors caught)

⚠️ **Limitations**:
- No persistence of canvas state (nodes/edges ephemeral)
- No undo/redo system
- No collaborative editing
- Single tenant per deployment (multi-tenant via separate deployments)
- No optimistic updates

🚀 **Next Steps**:
- Consider Zustand for pipeline state persistence
- Add immer middleware for immutable updates
- Implement undo/redo via command pattern
- Add localStorage for canvas auto-save
- Implement WebSocket instead of SSE for bidirectional communication
