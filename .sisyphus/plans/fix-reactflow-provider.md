# Fix ReactFlow Provider - Canvas Not Rendering

**Status**: Ready for Implementation  
**Priority**: Critical (Demo-blocking)  
**Effort**: 5 minutes  
**Impact**: Makes canvas functional

---

## Problem

Canvas area is **completely blank** (white screen). Sidebar works perfectly, but the main ReactFlow canvas doesn't render anything - no grid, no controls visible.

### Root Cause

`useNodesState` and `useEdgesState` hooks in PipelineBuilder require a `<ReactFlowProvider>` React Context wrapper. This provider is currently **missing**, so the hooks fail silently and the canvas doesn't render.

---

## Solution: Add ReactFlowProvider Wrapper

Create a new page wrapper component that provides the ReactFlow context.

### Files to Create/Modify

**Create** (1 new file):
```
ui/src/pages/PipelineBuilderPage.tsx (10 lines)
```

**Modify** (1 file):
```
ui/src/App.tsx (2 lines: import + route)
```

---

## Tasks

- [ ] **1.1** Create `ui/src/pages/PipelineBuilderPage.tsx` with ReactFlowProvider wrapper
  - Import `ReactFlowProvider` from 'reactflow'
  - Import `PipelineBuilder` from './PipelineBuilder'
  - Wrap PipelineBuilder with ReactFlowProvider
  - Export as default

- [ ] **1.2** Update `ui/src/App.tsx` import
  - Replace: `import PipelineBuilder from './pages/PipelineBuilder'`
  - With: `import PipelineBuilderPage from './pages/PipelineBuilderPage'`

- [ ] **1.3** Update `ui/src/App.tsx` route element
  - Replace: `element={<PipelineBuilder />}`
  - With: `element={<PipelineBuilderPage />}`

- [ ] **1.4** Build and verify
  - Run: `npm run build` in ui directory
  - Verify no TypeScript errors
  - Verify dev server loads page without errors
  - Verify canvas shows light grid background (not blank)

- [ ] **1.5** Test functionality
  - Sidebar visible and collapsible
  - Canvas shows grid pattern
  - Can drag Consumer from sidebar to canvas
  - Deploy button visible top-right
  - Property editor slides in when clicking node

---

## Implementation Code

### PipelineBuilderPage.tsx (NEW FILE)

```typescript
import { ReactFlowProvider } from 'reactflow'
import PipelineBuilder from './PipelineBuilder'

export default function PipelineBuilderPage() {
  return (
    <ReactFlowProvider>
      <PipelineBuilder />
    </ReactFlowProvider>
  )
}
```

### App.tsx Changes (MODIFY)

```diff
- import PipelineBuilder from './pages/PipelineBuilder'
+ import PipelineBuilderPage from './pages/PipelineBuilderPage'

...

- <Route path="/connections/create" element={<PipelineBuilder />} />
+ <Route path="/connections/create" element={<PipelineBuilderPage />} />
```

---

## Verification Checklist

After implementation, verify:

- [ ] No TypeScript compilation errors
- [ ] Build completes successfully
- [ ] Dev server starts without errors
- [ ] Canvas area shows light grid background
- [ ] Grid is NOT blank/white
- [ ] Sidebar components visible on left
- [ ] Deploy button visible top-right
- [ ] Can see ReactFlow controls (zoom/pan)
- [ ] Can drag Consumer from sidebar
- [ ] No console errors in browser

---

## Why This Works

**Before** (Broken):
```
PipelineBuilder
├─ useNodesState() ← ERROR: No context!
├─ useEdgesState() ← ERROR: No context!
└─ Returns broken state
   └─ Canvas doesn't render
```

**After** (Works):
```
PipelineBuilderPage
└─ <ReactFlowProvider> ← Provides context
   └─ PipelineBuilder
      ├─ useNodesState() ← OK: Context available
      ├─ useEdgesState() ← OK: Context available
      └─ Canvas renders with grid
```

---

## Expected Result

**Canvas will show**:
- Light grid background (currently blank/white)
- ReactFlow controls (zoom, pan, fit view)
- Drag-drop area ready for nodes
- Sidebar palette (already working)
- Deploy button (already visible)

---

## Rollback (if needed)

1. Delete `PipelineBuilderPage.tsx`
2. Restore App.tsx imports/routes to original
3. Rebuild

---

## Dependencies

- `reactflow` - Already installed v11.11.4
- No new packages needed
- No API changes

---

## Time Estimate

- Create file: 2 min
- Update App.tsx: 1 min
- Build & test: 2 min
- **Total: 5 minutes**

---

## Related Work

This fix enables Phase 2-4 of the Node-RED redesign plan:
- Phase 2: Styling & Polish (requires functional canvas)
- Phase 3: Functionality Testing (requires functional canvas)
- Phase 4: Refinement (requires functional canvas)

