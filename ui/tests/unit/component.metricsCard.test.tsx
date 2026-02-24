/**
 * MetricsCard Component Tests
 */

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MetricsCard } from '@/components/MetricsDisplay/MetricsCard'

describe('MetricsCard Component', () => {
  it('should render label and value', () => {
    render(<MetricsCard label="Test Metric" value={42} />)

    expect(screen.getByText('Test Metric')).toBeInTheDocument()
    expect(screen.getByText('42')).toBeInTheDocument()
  })

  it('should render unit when provided', () => {
    render(<MetricsCard label="Throughput" value={10.5} unit="msgs/sec" />)

    expect(screen.getByText('msgs/sec')).toBeInTheDocument()
  })

  it('should render loading skeleton', () => {
    const { container } = render(<MetricsCard label="Test" value={0} loading />)
    expect(container.querySelector('.animate-pulse')).toBeInTheDocument()
  })

  it('should apply correct color class', () => {
    const { container: container1 } = render(
      <MetricsCard label="Test" value={1} color="blue" />
    )
    expect(container1.querySelector('.bg-blue-50')).toBeInTheDocument()

    const { container: container2 } = render(
      <MetricsCard label="Test" value={1} color="red" />
    )
    expect(container2.querySelector('.bg-red-50')).toBeInTheDocument()
  })

  it('should render trend indicator', () => {
    render(
      <MetricsCard label="Test" value={100} trend="up" trendValue={"+10%" as any} />
    )

    expect(screen.getByText('↑')).toBeInTheDocument()
    expect(screen.getByText('+10%')).toBeInTheDocument()
  })

  it('should display string values', () => {
    render(<MetricsCard label="Status" value="Active" />)

    expect(screen.getByText('Active')).toBeInTheDocument()
  })

  it('should render icon when provided', () => {
    render(<MetricsCard label="Test" value={1} icon="📊" />)

    expect(screen.getByText('📊')).toBeInTheDocument()
  })
})
