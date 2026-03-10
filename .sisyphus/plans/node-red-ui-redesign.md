# Node-RED Style UI Redesign Plan

**Status**: Planning Phase  
**Target**: Complete redesign of PipelineBuilder to match Node-RED layout  
**Priority**: High (Demo-critical)

---

## Overview

Transform the current button-based interface into a professional **Node-RED style layout**:
- Full-screen canvas (center)
- Collapsible left sidebar with draggable component palette
- Right-slide property panel (on node click)
- Top-right Deploy button
- Auto-numbered nodes (Consumer 1, Consumer 2, etc.)
- Manual save via Deploy button only

---

## Architecture Changes

### Layout Structure
```
┌─────────────────────────────────────────────┐
│ PipelineBuilder (Full Screen)               │
├──────────┬──────────────────────────────────┤
│          │                                  │
│  Sidebar │     ReactFlow Canvas             │ ← Full flowchart
│          │     (Entire center/right)        │
│ (Drag    │                                  │
│  source) │                                  │  ┌──────────────┐
│          │                                  │  │ Deploy (top) │
│          │                                  │  │ right corner │
│          │                                  │  └──────────────┘
│          │                                  │
│          ├─────────┐                        │
│          │ Editor  │ (slides from right)    │
│          │ on click│                        │
└──────────┴──────────┴────────────────────────┘
```

### Component Palette (Left Sidebar)
- **Collapsible**: Toggle button at top
- **Scrollable list** of components:
  - Consumer (📥)
  - Filter (🔍)
  - Converter (🔄)
  - Producer (📤)
- **Drag-and-drop enabled**: Drag from sidebar onto canvas
- **Icon + Label** per component

### Main Canvas
- **Full ReactFlow** taking up most of the screen
- **Light/white grid background** (like Node-RED)
- **Auto-zoom controls** (ReactFlow's built-in)

### Property Editor
- **Right-slide panel** when node clicked
- **Width**: ~400px
- **Overlays canvas** (doesn't shrink it)
- **Close button** to dismiss

### Node Numbering
- Track count per type: Consumer 1, Consumer 2, etc.
- Update labels when nodes added/deleted
- Recount on each operation

### Deploy Button
- **Top-right corner** (fixed position)
- **Only way to save** pipeline
- **Validation**: ≥1 Consumer + ≥1 Producer

---

## Tasks

### Phase 1: Layout Redesign (6 tasks)

- [ ] **1.1** Create `ComponentPalette.tsx` sidebar
  - Collapsible toggle
  - Draggable component items
  - Icon + Label per type
  - Fixed width (~200px)

- [ ] **1.2** Refactor `PipelineBuilder.tsx` layout
  - Remove button-based UI
  - Full-screen canvas
  - Sidebar + Canvas structure
  - Deploy button top-right (fixed)

- [ ] **1.3** Enable drag-and-drop mechanics
  - useReactFlow hook
  - onDragStart from palette
  - onDrop on canvas
  - Auto-add node at position

- [ ] **1.4** Implement node auto-numbering
  - Utility: `getNodeLabel(nodeType, allNodes)`
  - Returns "Consumer 1" format
  - Call on create/delete
  - Update all labels

- [ ] **1.5** Update node components
  - Keep pill-shaped design
  - Show "Consumer 1" labels
  - Maintain color coding
  - Ensure readability

- [ ] **1.6** Implement right-slide property panel
  - Slide animation from right
  - Semi-transparent overlay
  - Close on click/button
  - Panel z-index over canvas

### Phase 2: Styling & Polish (4 tasks)

- [ ] **2.1** Canvas styling
  - White/light background
  - Subtle grid lines
  - Good contrast
  - Controls visible

- [ ] **2.2** Sidebar styling
  - Light background
  - Hover effects
  - Clear labels
  - Collapse animation

- [ ] **2.3** Deploy button styling
  - Fixed top-right
  - Prominent color
  - Loading state
  - Accessible

- [ ] **2.4** Property editor styling
  - Slide animation
  - Dark overlay
  - Clean forms
  - Smooth transitions

### Phase 3: Functionality (4 tasks)

- [ ] **3.1** Test drag-and-drop
  - Drag Consumer → canvas
  - Multiple nodes work
  - Auto-numbering correct
  - Nodes selectable

- [ ] **3.2** Test property editor
  - Click node → editor slides in
  - Configure node
  - Save updates node
  - Close works

- [ ] **3.3** Test deployment
  - Create Consumer 1 + Producer 1
  - Configure both
  - Deploy → backend POST
  - Success toast
  - Canvas resets

- [ ] **3.4** Test error handling
  - No Consumer → error
  - No Producer → error
  - Missing config → error
  - API errors shown
  - No console errors

### Phase 4: Refinement (2 tasks)

- [ ] **4.1** Node deletion and renumbering
  - Delete Consumer 1 → auto-renumber
  - Edges stay connected
  - Labels update
  - No orphaned nodes

- [ ] **4.2** Final polish
  - No console warnings
  - Responsive layout
  - Smooth animations
  - Demo-ready

---

## Files to Create/Modify

### New Files
- `ComponentPalette.tsx` - Sidebar palette
- `nodeNumbering.ts` - Auto-numbering utility

### Modified Files
- `PipelineBuilder.tsx` - Layout restructure
- `PropertyEditor.tsx` - Slide-in animation
- `ConsumerNode.tsx` - Label format
- `FilterNode.tsx` - Label format
- `ConverterNode.tsx` - Label format
- `ProducerNode.tsx` - Label format

---

## Key Implementation

### Drag-and-Drop
```typescript
// Palette item
const handleDragStart = (type: string) => {
  e.dataTransfer.setData('nodeType', type)
}

// Canvas drop
const onCanvasDrop = (e: DragEvent) => {
  const type = e.dataTransfer.getData('nodeType')
  const pos = { x: e.clientX, y: e.clientY }
  addNodeAtPosition(type, pos)
}
```

### Node Numbering
```typescript
export function getNodeLabel(nodeType: string, nodes: Node[]): string {
  const count = nodes.filter(n => n.data.type === nodeType).length + 1
  return `${nodeType} ${count}`
}
```

### Right-Slide Panel
```typescript
<div className={`
  fixed right-0 h-full w-96
  transition-transform ${selectedNode ? '' : 'translate-x-full'}
  z-50
`}>
```

---

## Testing Checklist

- [ ] Sidebar collapse/expand
- [ ] Drag Consumer → appears as "Consumer 1"
- [ ] Drag second → "Consumer 2"
- [ ] Delete Consumer 1 → renumbered
- [ ] Click node → editor slides in
- [ ] Configure + Save
- [ ] Deploy successful
- [ ] No console errors

---

## Success Criteria

✅ Node-RED layout match  
✅ Drag-and-drop working  
✅ Auto-numbering correct  
✅ Right-slide panel  
✅ Smooth animations  
✅ Deploy validated  
✅ Demo-ready  

---

## Effort Estimate

- Phase 1: 4-5 hours
- Phase 2: 1-2 hours
- Phase 3: 2-3 hours
- Phase 4: 1-2 hours
- **Total**: ~8-12 hours

