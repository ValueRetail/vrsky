/**
 * MessageProgressBar Component Tests
 */

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MessageProgressBar } from '@/components/MetricsDisplay/MessageProgressBar'

describe('MessageProgressBar Component', () => {
  describe('Rendering', () => {
    it('should render component title', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      expect(screen.getByText('Throughput')).toBeInTheDocument()
    })

    it('should render custom title when provided', () => {
      render(
        <MessageProgressBar
          messagesIn={100}
          messagesOut={50}
          throughputMps={10}
          title="Custom Title"
        />
      )

      expect(screen.getByText('Custom Title')).toBeInTheDocument()
    })

    it('should render throughput value', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={25.5} />
      )

      expect(screen.getByText('25.50 msgs/sec')).toBeInTheDocument()
    })

    it('should render Input label', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      expect(screen.getByText('Input')).toBeInTheDocument()
    })

    it('should render Output label', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      expect(screen.getByText('Output')).toBeInTheDocument()
    })
  })

  describe('Progress calculation', () => {
    it('should calculate 50% progress when half messages are out', () => {
      const { container } = render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      expect(screen.getByText('50.0%')).toBeInTheDocument()

      // Check progress bar width
      const progressDiv = container.querySelector('div[style*="width"]')
      expect(progressDiv?.getAttribute('style')).toContain('50')
    })

    it('should calculate 0% progress when no messages out', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={0} throughputMps={0} />
      )

      expect(screen.getByText('0.0%')).toBeInTheDocument()
    })

    it('should calculate 100% progress when all messages are out', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={100} throughputMps={50} />
      )

      expect(screen.getByText('100.0%')).toBeInTheDocument()
    })

    it('should cap progress at 100% when out > in', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={150} throughputMps={50} />
      )

      expect(screen.getByText('100.0%')).toBeInTheDocument()
    })

    it('should handle zero messages in', () => {
      render(
        <MessageProgressBar messagesIn={0} messagesOut={0} throughputMps={0} />
      )

      expect(screen.getByText('0.0%')).toBeInTheDocument()
    })

    it('should display correct percentages for various ratios', () => {
      const { rerender } = render(
        <MessageProgressBar messagesIn={1000} messagesOut={750} throughputMps={100} />
      )

      expect(screen.getByText('75.0%')).toBeInTheDocument()

      rerender(
        <MessageProgressBar messagesIn={1000} messagesOut={333} throughputMps={50} />
      )

      expect(screen.getByText('33.3%')).toBeInTheDocument()
    })
  })

  describe('Message counts display', () => {
    it('should display messages in count', () => {
      render(
        <MessageProgressBar messagesIn={500} messagesOut={250} throughputMps={50} />
      )

      expect(screen.getByText('500')).toBeInTheDocument()
    })

    it('should display messages out count', () => {
      render(
        <MessageProgressBar messagesIn={500} messagesOut={250} throughputMps={50} />
      )

      const outputs = screen.getAllByText('250')
      expect(outputs.length).toBeGreaterThan(0)
    })

    it('should display large numbers', () => {
      render(
        <MessageProgressBar messagesIn={1000000} messagesOut={999999} throughputMps={100} />
      )

      expect(screen.getByText('1000000')).toBeInTheDocument()
    })
  })

  describe('Status indicator', () => {
    it('should show green indicator when processing', () => {
      const { container } = render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      const statusIndicator = container.querySelector('[class*="bg-green"]')
      expect(statusIndicator).toBeInTheDocument()
    })

    it('should show gray indicator when no throughput', () => {
      const { container } = render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={0} />
      )

      const statusIndicator = container.querySelector('[class*="bg-gray"]')
      expect(statusIndicator).toBeInTheDocument()
    })

    it('should display "Actively processing" text when throughput > 0', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      expect(screen.getByText('Actively processing')).toBeInTheDocument()
    })

    it('should display "No throughput" text when throughput = 0', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={0} />
      )

      expect(screen.getByText('No throughput')).toBeInTheDocument()
    })

    it('should have pulse animation on indicator', () => {
      const { container } = render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      const statusIndicator = container.querySelector('[class*="animate-pulse"]')
      expect(statusIndicator).toBeInTheDocument()
    })
  })

  describe('Efficiency metrics', () => {
    it('should show "Excellent efficiency" at 99%+ progress', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={99} throughputMps={50} />
      )

      expect(screen.getByText('Excellent efficiency')).toBeInTheDocument()
    })

    it('should show "Good efficiency" at 90-98% progress', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={95} throughputMps={50} />
      )

      expect(screen.getByText('Good efficiency')).toBeInTheDocument()
    })

    it('should show "Fair efficiency" at 75-89% progress', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={80} throughputMps={50} />
      )

      expect(screen.getByText('Fair efficiency')).toBeInTheDocument()
    })

    it('should show "Processing..." at <75% progress', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={50} />
      )

      expect(screen.getByText('Processing...')).toBeInTheDocument()
    })

    it('should handle exactly 99% as excellent', () => {
      render(
        <MessageProgressBar messagesIn={1000} messagesOut={990} throughputMps={50} />
      )

      expect(screen.getByText('Excellent efficiency')).toBeInTheDocument()
    })

    it('should handle exactly 75% as fair', () => {
      render(
        <MessageProgressBar messagesIn={1000} messagesOut={750} throughputMps={50} />
      )

      expect(screen.getByText('Fair efficiency')).toBeInTheDocument()
    })
  })

  describe('Progress bar styling', () => {
    it('should render progress bar container', () => {
      const { container } = render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      const progressBar = container.querySelector('[class*="bg-gradient"]')
      expect(progressBar).toBeInTheDocument()
    })

    it('should have gradient styling on progress', () => {
      const { container } = render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      const gradient = container.querySelector('[class*="from-blue"]')
      expect(gradient).toBeInTheDocument()
    })

    it('should render background bar', () => {
      const { container } = render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      const bgBar = container.querySelector('[class*="bg-gray-100"]')
      expect(bgBar).toBeInTheDocument()
    })

    it('should animate progress changes', () => {
      const { container } = render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      const progressDiv = container.querySelector('[class*="transition-all"]')
      expect(progressDiv).toBeInTheDocument()
      expect(progressDiv).toHaveClass('duration-300')
    })
  })

  describe('Throughput formatting', () => {
    it('should format throughput to 2 decimal places', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10.567} />
      )

      expect(screen.getByText('10.57 msgs/sec')).toBeInTheDocument()
    })

    it('should format zero throughput', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={0} />
      )

      expect(screen.getByText('0.00 msgs/sec')).toBeInTheDocument()
    })

    it('should format small throughput values', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={0.001} />
      )

      expect(screen.getByText('0.00 msgs/sec')).toBeInTheDocument()
    })

    it('should format large throughput values', () => {
      render(
        <MessageProgressBar messagesIn={1000000} messagesOut={999999} throughputMps={9999.99} />
      )

      expect(screen.getByText('9999.99 msgs/sec')).toBeInTheDocument()
    })
  })

  describe('Edge cases', () => {
    it('should handle all zeros', () => {
      render(
        <MessageProgressBar messagesIn={0} messagesOut={0} throughputMps={0} />
      )

      expect(screen.getByText('0.0%')).toBeInTheDocument()
      expect(screen.getByText('0.00 msgs/sec')).toBeInTheDocument()
    })

    it('should handle out > in (over-delivery)', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={200} throughputMps={50} />
      )

      // Should cap at 100%
      expect(screen.getByText('100.0%')).toBeInTheDocument()
    })

    it('should handle fractional progress', () => {
      render(
        <MessageProgressBar messagesIn={3} messagesOut={1} throughputMps={10} />
      )

      expect(screen.getByText('33.3%')).toBeInTheDocument()
    })

    it('should handle very small in values', () => {
      render(
        <MessageProgressBar messagesIn={1} messagesOut={1} throughputMps={1} />
      )

      expect(screen.getByText('100.0%')).toBeInTheDocument()
    })
  })

  describe('Accessibility', () => {
    it('should render proper structure for screen readers', () => {
      render(
        <MessageProgressBar messagesIn={100} messagesOut={50} throughputMps={10} />
      )

      // Should have descriptive text
      expect(screen.getByText('Throughput')).toBeInTheDocument()
      expect(screen.getByText('Input')).toBeInTheDocument()
      expect(screen.getByText('Output')).toBeInTheDocument()
    })

    it('should display all metric values visually', () => {
      render(
        <MessageProgressBar messagesIn={1234} messagesOut={567} throughputMps={123.45} />
      )

      expect(screen.getByText('1234')).toBeInTheDocument()
      expect(screen.getByText('567')).toBeInTheDocument()
      expect(screen.getByText('123.45 msgs/sec')).toBeInTheDocument()
    })
  })
})
