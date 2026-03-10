import { ReactFlow as ReactFlowComponent, type ReactFlowProps } from 'reactflow'
import { useEffect, useRef } from 'react'
import { nodeTypes } from '../../constants/nodeTypes'

/**
 * ReactFlowWrapper: Handles ReactFlow initialization with React 19 + StrictMode compatibility
 *
 * This wrapper component exists to:
 * 1. Properly initialize ReactFlow with nodeTypes (prevents "new object" warnings)
 * 2. Handle dimension detection reliably (ResizeObserver + resize events)
 * 3. Work seamlessly with HMR (hot module reload) in development
 * 4. Maintain compatibility with React StrictMode
 * 5. Provide a single point of maintenance if ReactFlow's API changes
 *
 * Future Updates:
 * - If ReactFlow fixes their StrictMode/HMR issues, this wrapper becomes a thin pass-through
 * - If ReactFlow updates their API, changes are isolated here
 * - If we add more canvas components, they can follow this same pattern
 */

interface ReactFlowWrapperProps extends Omit<ReactFlowProps, 'nodeTypes'> {
  children: React.ReactNode
}

export default function ReactFlowWrapper(props: ReactFlowWrapperProps) {
  const containerRef = useRef<HTMLDivElement>(null)

  // Ensure ReactFlow properly detects container dimensions on mount and after HMR
  useEffect(() => {
    if (!containerRef.current) return

    // Force ReactFlow to recalculate dimensions after mount
    const resizeTimer = setTimeout(() => {
      window.dispatchEvent(new Event('resize'))
    }, 100)

    // Use ResizeObserver for more reliable dimension updates
    const resizeObserver = new ResizeObserver(() => {
      window.dispatchEvent(new Event('resize'))
    })

    resizeObserver.observe(containerRef.current)

    return () => {
      clearTimeout(resizeTimer)
      resizeObserver.disconnect()
    }
  }, [])

  return (
    <div
      ref={containerRef}
      className="w-full h-full overflow-hidden"
      style={{ width: '100%', height: '100%' }}
    >
      <ReactFlowComponent
        {...props}
        nodeTypes={nodeTypes}
      >
        {props.children}
      </ReactFlowComponent>
    </div>
  )
}
