/**
 * CanvasSelector Component
 * Horizontal tab-like UI for switching between tenant canvases
 * Based on user mockup: [Tenant 1] ● [Tenant 2] ● [+]
 */

import { useState, useRef, useEffect, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Canvas } from '../types/pipeline'

interface CanvasSelectorProps {
  canvases: Canvas[]
  currentCanvasId: string | null
  canCreateMore: boolean
  onSwitch: (id: string) => void
  onCreate: () => Canvas | null
  onRename: (id: string, newName: string) => void
  onDelete: (id: string) => void
  onBeforeSwitch?: () => void // Called before switching to save current state
}

export default function CanvasSelector({
  canvases,
  currentCanvasId,
  canCreateMore,
  onSwitch,
  onCreate,
  onRename,
  onDelete,
  onBeforeSwitch,
}: CanvasSelectorProps) {
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingName, setEditingName] = useState('')
  const [hoveredId, setHoveredId] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const navigate = useNavigate()

  // Focus input when editing starts
  useEffect(() => {
    if (editingId && inputRef.current) {
      inputRef.current.focus()
      inputRef.current.select()
    }
  }, [editingId])

  const handleDoubleClick = useCallback((canvas: Canvas) => {
    setEditingId(canvas.id)
    setEditingName(canvas.name)
  }, [])

  const handleRenameSubmit = useCallback(() => {
    if (editingId && editingName.trim()) {
      onRename(editingId, editingName.trim())
    }
    setEditingId(null)
    setEditingName('')
  }, [editingId, editingName, onRename])

  const handleRenameKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter') {
        handleRenameSubmit()
      } else if (e.key === 'Escape') {
        setEditingId(null)
        setEditingName('')
      }
    },
    [handleRenameSubmit]
  )

  const handleSwitchCanvas = useCallback(
    (id: string) => {
      if (id === currentCanvasId) return
      if (onBeforeSwitch) onBeforeSwitch()
      onSwitch(id)
    },
    [currentCanvasId, onSwitch, onBeforeSwitch]
  )

  const handleCreateCanvas = useCallback(() => {
    if (!canCreateMore) return
    if (onBeforeSwitch) onBeforeSwitch()
    onCreate()
  }, [canCreateMore, onCreate, onBeforeSwitch])

  const handleDeleteClick = useCallback(
    (e: React.MouseEvent, canvas: Canvas) => {
      e.stopPropagation()
      // Use native browser confirm dialog
      if (window.confirm(`Delete "${canvas.name}"?`)) {
        onDelete(canvas.id)
      }
    },
    [onDelete]
  )

  return (
    <div
      style={{
        position: 'relative',
        zIndex: 10,
        display: 'flex',
        alignItems: 'center',
        gap: '8px',
        padding: '8px 12px',
        backgroundColor: '#f3f4f6',
        borderBottom: '1px solid #e5e7eb',
        overflowX: 'auto',
        minHeight: '48px',
      }}
    >
      {/* Tenant Tabs */}
      {canvases.map((canvas) => {
        const isActive = canvas.id === currentCanvasId
        const isHovered = hoveredId === canvas.id
        const isEditing = editingId === canvas.id
        const showDelete = (isHovered || isActive) && canvases.length > 1

        return (
          <div
            key={canvas.id}
            onMouseEnter={() => setHoveredId(canvas.id)}
            onMouseLeave={() => setHoveredId(null)}
            onClick={() => !isEditing && handleSwitchCanvas(canvas.id)}
            onDoubleClick={() => handleDoubleClick(canvas)}
            style={{
              position: 'relative',
              display: 'flex',
              alignItems: 'center',
              gap: '8px',
              padding: '6px 12px',
              paddingRight: showDelete ? '28px' : '12px',
              backgroundColor: isActive ? 'white' : isHovered ? '#e5e7eb' : '#f9fafb',
              border: isActive ? '1px solid #d1d5db' : '1px solid transparent',
              borderRadius: '6px',
              cursor: isEditing ? 'text' : 'pointer',
              transition: 'all 150ms ease',
              minWidth: '80px',
              maxWidth: '200px',
              boxShadow: isActive ? '0 1px 3px rgba(0, 0, 0, 0.1)' : 'none',
            }}
            title={isEditing ? '' : 'Double-click to rename'}
          >
            {/* Active Indicator Dot */}
            {isActive && (
              <span
                style={{
                  width: '8px',
                  height: '8px',
                  borderRadius: '50%',
                  backgroundColor: '#3b82f6',
                  flexShrink: 0,
                }}
              />
            )}

            {/* Tenant Name (editable) */}
            {isEditing ? (
              <input
                ref={inputRef}
                type="text"
                value={editingName}
                onChange={(e) => setEditingName(e.target.value)}
                onBlur={handleRenameSubmit}
                onKeyDown={handleRenameKeyDown}
                style={{
                  flex: 1,
                  padding: '2px 4px',
                  fontSize: '13px',
                  fontWeight: 500,
                  border: '1px solid #3b82f6',
                  borderRadius: '3px',
                  outline: 'none',
                  minWidth: '60px',
                  backgroundColor: 'white',
                }}
                onClick={(e) => e.stopPropagation()}
              />
            ) : (
              <span
                style={{
                  fontSize: '13px',
                  fontWeight: isActive ? 600 : 500,
                  color: isActive ? '#1f2937' : '#6b7280',
                  whiteSpace: 'nowrap',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                }}
              >
                {canvas.name}
              </span>
            )}

            {/* Delete Button (X) */}
            {showDelete && !isEditing && (
              <button
                onClick={(e) => handleDeleteClick(e, canvas)}
                style={{
                  position: 'absolute',
                  right: '6px',
                  top: '50%',
                  transform: 'translateY(-50%)',
                  width: '16px',
                  height: '16px',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  backgroundColor: 'transparent',
                  border: 'none',
                  borderRadius: '3px',
                  cursor: 'pointer',
                  color: '#9ca3af',
                  fontSize: '12px',
                  fontWeight: 600,
                  padding: 0,
                  transition: 'all 100ms ease',
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.backgroundColor = '#fee2e2'
                  e.currentTarget.style.color = '#dc2626'
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.backgroundColor = 'transparent'
                  e.currentTarget.style.color = '#9ca3af'
                }}
                title="Delete tenant"
              >
                ×
              </button>
            )}
          </div>
        )
      })}

      {/* New Tenant Button (+) */}
      <button
        onClick={handleCreateCanvas}
        disabled={!canCreateMore}
        style={{
          width: '32px',
          height: '32px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          backgroundColor: canCreateMore ? '#f9fafb' : '#f3f4f6',
          border: '1px dashed #d1d5db',
          borderRadius: '6px',
          cursor: canCreateMore ? 'pointer' : 'not-allowed',
          color: canCreateMore ? '#6b7280' : '#9ca3af',
          fontSize: '18px',
          fontWeight: 400,
          flexShrink: 0,
          transition: 'all 150ms ease',
          opacity: canCreateMore ? 1 : 0.5,
        }}
        onMouseEnter={(e) => {
          if (canCreateMore) {
            e.currentTarget.style.backgroundColor = '#e5e7eb'
            e.currentTarget.style.borderColor = '#9ca3af'
          }
        }}
        onMouseLeave={(e) => {
          e.currentTarget.style.backgroundColor = canCreateMore ? '#f9fafb' : '#f3f4f6'
          e.currentTarget.style.borderColor = '#d1d5db'
        }}
        title={canCreateMore ? 'Create new tenant' : 'Maximum 10 tenants reached'}
      >
        +
      </button>

      {/* New pipeline from a template — reopens the onboarding gallery (#93),
          so templates are reachable any time (not just first login) and you
          can build several pipelines from them. */}
      <button
        onClick={() => navigate('/welcome')}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: '6px',
          height: '32px',
          padding: '0 12px',
          backgroundColor: '#eff6ff',
          border: '1px solid #bfdbfe',
          borderRadius: '6px',
          cursor: 'pointer',
          color: '#2563eb',
          fontSize: '13px',
          fontWeight: 600,
          whiteSpace: 'nowrap',
          flexShrink: 0,
        }}
        onMouseEnter={(e) => { e.currentTarget.style.backgroundColor = '#dbeafe' }}
        onMouseLeave={(e) => { e.currentTarget.style.backgroundColor = '#eff6ff' }}
        title="Start a new pipeline from a template"
      >
        ⊞ From template
      </button>

      {/* Tenant Limit Indicator */}
      <span
        style={{
          fontSize: '11px',
          color: '#9ca3af',
          marginLeft: '8px',
          whiteSpace: 'nowrap',
        }}
      >
        {canvases.length}/10
      </span>
    </div>
  )
}
