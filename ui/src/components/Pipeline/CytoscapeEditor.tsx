import { useEffect, useRef, useState } from 'react'
import cytoscape from 'cytoscape'
import type { Node, Edge } from '../../types/pipeline'

interface CytoscapeEditorProps {
  nodes: Node[]
  edges: Edge[]
  onNodeSelect: (node: Node) => void
  onDragOver: (event: React.DragEvent<HTMLDivElement>) => void
  onDrop: (event: React.DragEvent<HTMLDivElement>) => void
}

export default function CytoscapeEditor({
  nodes,
  edges,
  onNodeSelect,
  onDragOver,
  onDrop,
}: CytoscapeEditorProps) {
  // Unused parameters are removed from destructuring
  const containerRef = useRef<HTMLDivElement>(null)
  const cyRef = useRef<any>(null)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)

  // Initialize Cytoscape graph
  useEffect(() => {
    if (!containerRef.current) {
      console.warn('[CytoscapeEditor] Container ref is null, skipping initialization')
      return
    }

    console.log('[CytoscapeEditor] Starting initialization...')
    console.log('[CytoscapeEditor] Container element:', containerRef.current)
    console.log('[CytoscapeEditor] Container dimensions:', {
      offsetWidth: containerRef.current.offsetWidth,
      offsetHeight: containerRef.current.offsetHeight,
      clientWidth: containerRef.current.clientWidth,
      clientHeight: containerRef.current.clientHeight,
    })
    console.log('[CytoscapeEditor] Container computed style:', {
      width: window.getComputedStyle(containerRef.current).width,
      height: window.getComputedStyle(containerRef.current).height,
      display: window.getComputedStyle(containerRef.current).display,
      position: window.getComputedStyle(containerRef.current).position,
    })

    // Small delay to ensure DOM is fully ready (Cytoscape needs to measure container)
    const timeoutId = setTimeout(() => {
      try {
        // Create Cytoscape instance
        console.log('[CytoscapeEditor] Creating Cytoscape instance...')
        const cy = cytoscape({
          container: containerRef.current,
          style: [
            {
              selector: 'node',
              style: {
                'content': 'data(label)',
                'text-valign': 'center',
                'text-halign': 'center',
                'background-color': 'data(bgColor)',
                'border-color': 'data(borderColor)',
                'border-width': '2px',
                'padding': '10px',
                'font-size': '12px',
                'font-weight': 'bold',
                'color': '#ffffff',
                'width': '120px',
                'height': '60px',
                'shape': 'rectangle',
                'text-wrap': 'wrap',
                'text-max-width': '100px',
              },
            },
            {
              selector: 'node:selected',
              style: {
                'border-width': '3px',
                'border-color': '#000000',
              },
            },
            {
              selector: 'edge',
              style: {
                'target-arrow-shape': 'triangle',
                'line-color': '#ccc',
                'target-arrow-color': '#ccc',
                'curve-style': 'bezier',
                'width': '2px',
              },
            },
          ],
          layout: {
            name: 'preset',
          },
        })

        console.log('[CytoscapeEditor] Cytoscape instance created successfully:', cy)
        console.log('[CytoscapeEditor] Canvas element in DOM:', containerRef.current?.querySelector('canvas'))

        cyRef.current = cy

        // Handle node click
        cy.on('tap', 'node', (event) => {
          console.log('[CytoscapeEditor] Node clicked:', event.target.id())
          const node = event.target
          const nodeId = node.id()
          setSelectedNodeId(nodeId)

          // Find the original node data
          const originalNode = nodes.find((n) => n.id === nodeId)
          if (originalNode) {
            onNodeSelect(originalNode)
          }
        })

        // Handle panning (deselect on canvas click)
        cy.on('tap', (event) => {
          if (event.target === cy) {
            console.log('[CytoscapeEditor] Canvas clicked, deselecting node')
            setSelectedNodeId(null)
          }
        })

        // Enable dragging nodes
        cy.elements().forEach((el: any) => {
          if (el.isNode()) {
            el.draggable(true)
          }
        })

        // Update graph when nodes/edges change
        console.log('[CytoscapeEditor] Updating graph with initial data')
        updateGraph(cy, nodes, edges, selectedNodeId)

        console.log('[CytoscapeEditor] Initialization complete!')
      } catch (error) {
        console.error('[CytoscapeEditor] Failed to initialize Cytoscape:', error)
        if (error instanceof Error) {
          console.error('[CytoscapeEditor] Error message:', error.message)
          console.error('[CytoscapeEditor] Error stack:', error.stack)
        }
      }
    }, 100) // Small delay for DOM readiness

    return () => {
      clearTimeout(timeoutId)
      if (cyRef.current) {
        console.log('[CytoscapeEditor] Cleaning up Cytoscape instance')
        cyRef.current.destroy()
      }
    }
  }, [nodes, edges, selectedNodeId, onNodeSelect])



  return (
    <div
      ref={containerRef}
      className="w-full h-full overflow-hidden bg-gray-50"
      style={{
        width: '100%',
        height: '100%',
        position: 'relative',
      }}
      onDragOver={onDragOver}
      onDrop={onDrop}
    />
  )
}

// Helper function to update Cytoscape graph
function updateGraph(
  cy: any,
  nodes: Node[],
  edges: Edge[],
  selectedNodeId: string | null
) {
  console.log('[updateGraph] Updating graph with nodes:', nodes.length, 'edges:', edges.length)

  try {
    // Convert nodes to Cytoscape format
    const cytoscapeNodes: any[] = nodes.map((node) => {
      const bgColor = getBgColor(node.type)
      const borderColor = getBorderColor(node.type)

      return {
        data: {
          id: node.id,
          label: node.data.label,
          bgColor,
          borderColor,
          x: Math.random() * 800,
          y: Math.random() * 600,
        },
        position: {
          x: Math.random() * 800,
          y: Math.random() * 600,
        },
      } as any
    })

    // Convert edges to Cytoscape format
    const cytoscapeEdges: any[] = edges.map((edge) => ({
      data: {
        id: `${edge.source}-${edge.target}`,
        source: edge.source,
        target: edge.target,
      },
    } as any))

    console.log('[updateGraph] Converted nodes:', cytoscapeNodes.length, 'converted edges:', cytoscapeEdges.length)

    // Update Cytoscape elements
    cy.elements().remove()
    cy.add([...cytoscapeNodes, ...cytoscapeEdges])

    console.log('[updateGraph] Elements added to Cytoscape, total elements:', cy.elements().length)

    // Apply layout only if there are nodes
    if (nodes.length > 0) {
      const layout = cy.layout({
        name: 'grid',
        rows: Math.ceil(Math.sqrt(nodes.length)),
        cols: Math.ceil(Math.sqrt(nodes.length)),
        condense: true,
        spacingFactor: 2,
      })
      layout.run()
      console.log('[updateGraph] Layout applied')
    }

    // Highlight selected node
    if (selectedNodeId) {
      const selectedNode = cy.getElementById(selectedNodeId)
      if (selectedNode.length > 0) {
        selectedNode.select()
        console.log('[updateGraph] Selected node:', selectedNodeId)
      }
    }

    console.log('[updateGraph] Graph update complete!')
  } catch (error) {
    console.error('[updateGraph] Failed to update graph:', error)
    if (error instanceof Error) {
      console.error('[updateGraph] Error message:', error.message)
      console.error('[updateGraph] Error stack:', error.stack)
    }
  }
}

// Helper functions for node colors
function getBgColor(type?: string): string {
  switch (type) {
    case 'input':
      return '#3b82f6' // Blue
    case 'filter':
      return '#fb923c' // Orange
    case 'converter':
      return '#ec4899' // Pink
    case 'output':
      return '#10b981' // Emerald
    default:
      return '#6b7280' // Gray
  }
}

function getBorderColor(type?: string): string {
  switch (type) {
    case 'input':
      return '#1e40af' // Darker blue
    case 'filter':
      return '#d97706' // Darker orange
    case 'converter':
      return '#be185d' // Darker pink
    case 'output':
      return '#065f46' // Darker emerald
    default:
      return '#374151' // Dark gray
  }
}
