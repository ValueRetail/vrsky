/**
 * PipelineFlowVisualization Component Tests
 *
 * The component is now driven by the connection's real nodes/edges (#129):
 * it renders only the components the pipeline actually has, in flow order, and
 * maps each to its live metrics bucket. (It used to always draw a fixed
 * Consumer→Converter→Filter→Producer chain from metrics alone.)
 */

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { PipelineFlowVisualization } from '@/components/MetricsDisplay/PipelineFlowVisualization'
import type { ConnectionMetrics, ConnectionNode, ConnectionEdge } from '@/types/models'

const fullNodes: ConnectionNode[] = [
  { id: 'n1', type: 'consumer', config: { type: 'http' } },
  { id: 'n2', type: 'converter', config: {} },
  { id: 'n3', type: 'filter', config: {} },
  { id: 'n4', type: 'producer', config: { type: 'file' } },
]
const fullEdges: ConnectionEdge[] = [
  { source: 'n1', target: 'n2' },
  { source: 'n2', target: 'n3' },
  { source: 'n3', target: 'n4' },
]

const mockMetrics: ConnectionMetrics = {
  connection_id: 'test-conn',
  tenant_id: 'test-tenant',
  status: 'active',
  components: {
    consumer: { status: 'active', messages_processed: 1000, errors: 0, last_update: '' },
    converter: { status: 'active', messages_processed: 950, errors: 5, last_update: '' },
    filter: { status: 'active', messages_processed: 900, filtered_out: 50, errors: 0, last_update: '' },
    producer: { status: 'active', messages_sent: 900, messages_processed: 900, errors: 0, last_update: '' },
  },
  total_messages_in: 1000,
  total_messages_out: 900,
  total_errors: 5,
  errors_per_second: 0.001,
  throughput_mps: 100,
  last_updated: '',
}

describe('PipelineFlowVisualization', () => {
  describe('Empty state', () => {
    it('shows "no pipeline defined" when there are no nodes', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} nodes={[]} />)
      expect(screen.getByText(/No pipeline defined/i)).toBeInTheDocument()
    })

    it('shows "no pipeline defined" when nodes is omitted', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} />)
      expect(screen.getByText(/No pipeline defined/i)).toBeInTheDocument()
    })
  })

  describe('Data-driven rendering', () => {
    it('renders a box only for the nodes the pipeline actually has', () => {
      // consumer → producer only: must NOT invent Converter/Filter boxes.
      const nodes: ConnectionNode[] = [
        { id: 'a', type: 'consumer', config: { type: 'http' } },
        { id: 'b', type: 'producer', config: { type: 'file' } },
      ]
      const { container } = render(
        <PipelineFlowVisualization metrics={mockMetrics} nodes={nodes} edges={[{ source: 'a', target: 'b' }]} />
      )
      const boxes = container.querySelectorAll('rect')
      expect(boxes.length).toBe(2)
      const svgText = Array.from(document.querySelectorAll('text')).map((t) => t.textContent)
      expect(svgText).toContain('Consumer')
      expect(svgText).toContain('Producer')
      expect(svgText).not.toContain('Converter')
      expect(svgText).not.toContain('Filter')
    })

    it('renders all four when present, in flow order', () => {
      const { container } = render(
        <PipelineFlowVisualization metrics={mockMetrics} nodes={fullNodes} edges={fullEdges} />
      )
      expect(container.querySelectorAll('rect').length).toBe(4)
      const svgText = Array.from(document.querySelectorAll('text')).map((t) => t.textContent)
      expect(svgText).toContain('Consumer')
      expect(svgText).toContain('Producer')
    })

    it('maps live metrics onto the matching nodes', () => {
      render(<PipelineFlowVisualization metrics={mockMetrics} nodes={fullNodes} edges={fullEdges} />)
      const svgText = Array.from(document.querySelectorAll('text')).map((t) => t.textContent)
      expect(svgText).toContain('1000 msgs') // consumer
      expect(svgText.some((t) => t?.includes('5 errors'))).toBe(true) // converter
    })

    it('shows a dash for nodes without metrics yet', () => {
      render(<PipelineFlowVisualization metrics={undefined} nodes={fullNodes} edges={fullEdges} />)
      const svgText = Array.from(document.querySelectorAll('text')).map((t) => t.textContent)
      expect(svgText).toContain('—')
    })
  })

  describe('Idle / running state', () => {
    it('reassures when running with metrics but no traffic', () => {
      const idle: ConnectionMetrics = {
        ...mockMetrics,
        components: {
          consumer: { status: 'idle', messages_processed: 0, errors: 0, last_update: '' },
          converter: { status: 'idle', messages_processed: 0, errors: 0, last_update: '' },
          filter: { status: 'idle', messages_processed: 0, filtered_out: 0, errors: 0, last_update: '' },
          producer: { status: 'idle', messages_sent: 0, messages_processed: 0, errors: 0, last_update: '' },
        },
      }
      render(<PipelineFlowVisualization metrics={idle} nodes={fullNodes} edges={fullEdges} status="running" />)
      expect(screen.getByText(/no traffic yet/i)).toBeInTheDocument()
    })

    it('says it is waiting for metrics when running without any', () => {
      render(<PipelineFlowVisualization metrics={undefined} nodes={fullNodes} edges={fullEdges} status="running" />)
      expect(screen.getByText(/waiting for metrics/i)).toBeInTheDocument()
    })
  })

  it('always renders the title when a pipeline is present', () => {
    render(<PipelineFlowVisualization metrics={mockMetrics} nodes={fullNodes} edges={fullEdges} />)
    expect(screen.getByText('Pipeline Flow')).toBeInTheDocument()
  })
})
