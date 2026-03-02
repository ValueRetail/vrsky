/**
 * PipelineFlowVisualization Component Tests
 */

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { PipelineFlowVisualization } from '@/components/MetricsDisplay/PipelineFlowVisualization'
import type { ConnectionMetrics } from '@/types/models'

describe('PipelineFlowVisualization Component', () => {
  const mockMetrics: ConnectionMetrics = {
    connection_id: 'test-conn',
    tenant_id: 'test-tenant',
    status: 'active',
    components: {
      consumer: {
        status: 'active',
        messages_processed: 1000,
        errors: 0,
        last_update: '2024-01-20T10:00:00Z',
      },
      converter: {
        status: 'active',
        messages_processed: 950,
        errors: 5,
        last_update: '2024-01-20T10:00:00Z',
      },
      filter: {
        status: 'active',
        messages_processed: 900,
        filtered_out: 50,
        errors: 0,
        last_update: '2024-01-20T10:00:00Z',
      },
      producer: {
        status: 'active',
        messages_sent: 900,
        messages_processed: 900,
        errors: 0,
        last_update: '2024-01-20T10:00:00Z',
      },
    },
    total_messages_in: 1000,
    total_messages_out: 900,
    total_errors: 5,
    errors_per_second: 0.001,
    throughput_mps: 100,
    last_updated: '2024-01-20T10:00:00Z',
  }

  describe('Empty state', () => {
    it('should render empty state when metrics is null', () => {
      render(<PipelineFlowVisualization metrics={null} />)

      expect(screen.getByText('No pipeline data available')).toBeInTheDocument()
    })

    it('should render empty state when metrics is undefined', () => {
      render(<PipelineFlowVisualization metrics={undefined} />)

      expect(screen.getByText('No pipeline data available')).toBeInTheDocument()
    })
  })

  describe('Component rendering', () => {
    it('should render pipeline title', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      expect(screen.getByText('Pipeline Flow')).toBeInTheDocument()
    })

    it('should render all component names', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      // Component names appear in SVG
      const text = document.querySelectorAll('text')
      const componentNames = Array.from(text).map(el => el.textContent)
      expect(componentNames).toContain('Consumer')
      expect(componentNames).toContain('Converter')
      expect(componentNames).toContain('Filter')
      expect(componentNames).toContain('Producer')
    })

    it('should render SVG diagram', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const svg = container.querySelector('svg')
      expect(svg).toBeInTheDocument()
    })

    it('should render component boxes in SVG', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const rects = container.querySelectorAll('rect')
      // 4 component boxes (Consumer, Converter, Filter, Producer)
      expect(rects.length).toBeGreaterThanOrEqual(4)
    })

    it('should render arrows between components', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const lines = container.querySelectorAll('line')
      // Should have arrows connecting components
      expect(lines.length).toBeGreaterThan(0)
    })

    it('should render status indicators (circles)', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const circles = container.querySelectorAll('circle')
      // 4 status circles (one per component)
      expect(circles.length).toBeGreaterThanOrEqual(4)
    })
  })

  describe('Status display', () => {
    it('should display component status grid', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      // Status grid should display all components
      const allConsumers = screen.getAllByText('Consumer')
      const allConverters = screen.getAllByText('Converter')
      expect(allConsumers.length).toBeGreaterThan(0)
      expect(allConverters.length).toBeGreaterThan(0)
    })

    it('should render active status with correct colors', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      // Check for green status indicator for active
      const statusElements = container.querySelectorAll('[class*="bg-green"]')
      expect(statusElements.length).toBeGreaterThan(0)
    })

    it('should handle idle status', () => {
      const idleMetrics: ConnectionMetrics = {
        ...mockMetrics,
        components: {
          ...mockMetrics.components,
          consumer: { ...mockMetrics.components.consumer, status: 'idle' },
        },
      }

      const { container } = render(<PipelineFlowVisualization metrics={idleMetrics} />)

      const statusElements = container.querySelectorAll('[class*="bg-gray"]')
      expect(statusElements.length).toBeGreaterThan(0)
    })

    it('should handle error status', () => {
      const errorMetrics: ConnectionMetrics = {
        ...mockMetrics,
        components: {
          ...mockMetrics.components,
          producer: { ...mockMetrics.components.producer, status: 'error' },
        },
      }

      const { container } = render(<PipelineFlowVisualization metrics={errorMetrics} />)

      const statusElements = container.querySelectorAll('[class*="bg-red"]')
      expect(statusElements.length).toBeGreaterThan(0)
    })
  })

  describe('Metrics display', () => {
    it('should render message processed counts', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const text = document.querySelectorAll('text')
      const textContent = Array.from(text).map(el => el.textContent)

      expect(textContent).toContain('1000 msgs') // Consumer
      expect(textContent).toContain('950 msgs') // Converter
      expect(textContent).toContain('900 msgs') // Filter
    })

    it('should display error count when errors exist', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const text = document.querySelectorAll('text')
      const textContent = Array.from(text).map(el => el.textContent)

      // Converter has 5 errors, filter has 0
      expect(textContent.some(t => t && t.includes('5 errors'))).toBe(true)
    })

    it('should not display error count when none', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const text = document.querySelectorAll('text')
      const textContent = Array.from(text).map(el => el.textContent)

      // Filter and producer have 0 errors
      expect(textContent.filter(t => t && t.includes('0 errors')).length).toBe(0)
    })
  })

  describe('Overall pipeline metrics', () => {
    it('should display total messages in', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      expect(screen.getByText('Messages In')).toBeInTheDocument()
      expect(screen.getByText('1000')).toBeInTheDocument()
    })

    it('should display total messages out', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      expect(screen.getByText('Messages Out')).toBeInTheDocument()
      // 900 is unique to messages out
      const allText = document.body.textContent
      expect(allText).toContain('900')
    })

    it('should display total errors', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      expect(screen.getByText('Total Errors')).toBeInTheDocument()
      expect(screen.getByText('5')).toBeInTheDocument()
    })

    it('should use red styling when errors exist', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const errorCard = container.querySelector('[class*="bg-red-50"]')
      expect(errorCard).toBeInTheDocument()
    })

    it('should use gray styling when no errors', () => {
      const noErrorMetrics: ConnectionMetrics = {
        ...mockMetrics,
        total_errors: 0,
      }

      const { container } = render(<PipelineFlowVisualization metrics={noErrorMetrics} />)

      const errorCard = container.querySelector('[class*="bg-gray-50"]')
      expect(errorCard).toBeInTheDocument()
    })
  })

  describe('Component status text', () => {
    it('should capitalize status text', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const activeElements = screen.getAllByText('Active')
      expect(activeElements.length).toBeGreaterThan(0)
    })

    it('should display all component statuses', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const statusElements = document.querySelectorAll('[class*="text-green-800"]')
      expect(statusElements.length).toBeGreaterThan(0)
    })
  })

  describe('Component grid layout', () => {
    it('should render grid of 4 components', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      // Check that grid with 4 components exists
      const mainGrid = container.querySelector('.grid.grid-cols-2')
      expect(mainGrid).toBeInTheDocument()
      
      // Check component names are rendered
      const allConsumers = screen.getAllByText('Consumer')
      expect(allConsumers.length).toBeGreaterThan(0)
    })

    it('should display component name in grid item', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)

      // Using getAllByText to handle multiple occurrences of component names
      expect(screen.getAllByText('Consumer').length).toBeGreaterThan(0)
      expect(screen.getAllByText('Converter').length).toBeGreaterThan(0)
      expect(screen.getAllByText('Filter').length).toBeGreaterThan(0)
      expect(screen.getAllByText('Producer').length).toBeGreaterThan(0)
    })
  })

  describe('SVG rendering', () => {
    it('should include arrows with proper styling', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const lines = container.querySelectorAll('line[stroke="#cbd5e1"]')
      expect(lines.length).toBeGreaterThan(0)
    })

    it('should render polygons for arrow heads', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const polygons = container.querySelectorAll('polygon')
      expect(polygons.length).toBeGreaterThan(0)
    })

    it('should render boxes with proper dimensions', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const boxes = container.querySelectorAll('rect[width="100"]')
      expect(boxes.length).toBeGreaterThan(0)
    })

    it('should render boxes with height 60', () => {
      const { container } = render(<PipelineFlowVisualization metrics={mockMetrics} />)

      const boxes = container.querySelectorAll('rect[height="60"]')
      expect(boxes.length).toBeGreaterThan(0)
    })
  })

  describe('Edge cases', () => {
    it('should handle zero metrics', () => {
      const zeroMetrics: ConnectionMetrics = {
        ...mockMetrics,
        total_messages_in: 0,
        total_messages_out: 0,
        total_errors: 0,
        components: {
          ...mockMetrics.components,
          consumer: { ...mockMetrics.components.consumer, messages_processed: 0, errors: 0 },
          converter: { ...mockMetrics.components.converter, messages_processed: 0, errors: 0 },
          filter: { ...mockMetrics.components.filter, messages_processed: 0, errors: 0 },
          producer: { ...mockMetrics.components.producer, messages_sent: 0, errors: 0 },
        },
      }

      render(<PipelineFlowVisualization metrics={zeroMetrics} />)

      expect(screen.getByText('Pipeline Flow')).toBeInTheDocument()
    })

    it('should handle large numbers', () => {
      const largeMetrics: ConnectionMetrics = {
        ...mockMetrics,
        total_messages_in: 1000000,
        total_messages_out: 999999,
        total_errors: 1000,
      }

      render(<PipelineFlowVisualization metrics={largeMetrics} />)

      expect(screen.getByText('1000000')).toBeInTheDocument()
      expect(screen.getByText('1000')).toBeInTheDocument()
    })

    it('should handle all components with errors', () => {
      const allErrorMetrics: ConnectionMetrics = {
        ...mockMetrics,
        components: {
          consumer: { ...mockMetrics.components.consumer, errors: 10 },
          converter: { ...mockMetrics.components.converter, errors: 10 },
          filter: { ...mockMetrics.components.filter, errors: 10 },
          producer: { ...mockMetrics.components.producer, errors: 10 },
        },
      }

      render(<PipelineFlowVisualization metrics={allErrorMetrics} />)

      expect(screen.getByText('Pipeline Flow')).toBeInTheDocument()
    })
  })
})
