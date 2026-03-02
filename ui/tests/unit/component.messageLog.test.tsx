/**
 * MessageLog Component Tests
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MessageLog, type MessageLogEntry } from '@/components/TestData/MessageLog'

describe('MessageLog Component', () => {
  const mockMessages: MessageLogEntry[] = [
    {
      id: '1',
      timestamp: '2024-01-20T10:00:00Z',
      status: 'sent',
      message: '{"test": "message1"}',
    },
    {
      id: '2',
      timestamp: '2024-01-20T10:00:01Z',
      status: 'processed',
      message: '{"test": "message2"}',
      result: '{"processed": true}',
    },
    {
      id: '3',
      timestamp: '2024-01-20T10:00:02Z',
      status: 'error',
      message: '{"test": "message3"}',
      error: 'Connection timeout',
    },
  ]

  describe('Empty state', () => {
    it('should render empty state when no messages', () => {
      render(<MessageLog messages={[]} />)

      expect(screen.getByText(/No messages yet/)).toBeInTheDocument()
    })

    it('should suggest sending a test message', () => {
      render(<MessageLog messages={[]} />)

      expect(
        screen.getByText(/Send a test message or start the auto-generator/)
      ).toBeInTheDocument()
    })
  })

  describe('Message display', () => {
    it('should render all messages on first page', () => {
      render(<MessageLog messages={mockMessages} pageSize={10} />)

      expect(screen.getByText('{"test": "message1"}', { selector: 'p' })).toBeInTheDocument()
      expect(screen.getByText('{"test": "message2"}', { selector: 'p' })).toBeInTheDocument()
      expect(screen.getByText('{"test": "message3"}', { selector: 'p' })).toBeInTheDocument()
    })

    it('should display message count', () => {
      render(<MessageLog messages={mockMessages} />)

      expect(screen.getByText(/3 total messages/)).toBeInTheDocument()
    })

    it('should render status icons', () => {
      render(<MessageLog messages={mockMessages} />)

      expect(screen.getByText('📤')).toBeInTheDocument() // sent
      expect(screen.getByText('✅')).toBeInTheDocument() // processed
      expect(screen.getByText('❌')).toBeInTheDocument() // error
    })

    it('should display formatted timestamps', () => {
      render(<MessageLog messages={mockMessages} />)

      // Just verify that timestamps are displayed (exact format depends on locale)
      const timeElements = screen.getAllByText(/\d{1,2}:\d{2}:\d{2}/)
      expect(timeElements.length).toBeGreaterThanOrEqual(3)
    })

    it('should apply correct status colors', () => {
      const { container } = render(<MessageLog messages={mockMessages} />)

      const bgColors = container.querySelectorAll('[class*="bg-blue-50"], [class*="bg-green-50"], [class*="bg-red-50"]')
      expect(bgColors.length).toBeGreaterThanOrEqual(3)
    })
  })

  describe('Pagination', () => {
    const manyMessages = Array.from({ length: 25 }, (_, i) => ({
      id: String(i),
      timestamp: new Date(2024, 0, 20, 10, 0, i).toISOString(),
      status: 'sent' as const,
      message: `{"msg": ${i}}`,
    }))

    it('should not show pagination when all messages fit on one page', () => {
      render(<MessageLog messages={mockMessages} pageSize={10} />)

      expect(screen.queryByText(/Page \d+ of \d+/)).not.toBeInTheDocument()
    })

    it('should show pagination controls when needed', () => {
      render(<MessageLog messages={manyMessages} pageSize={10} />)

      expect(screen.getByText(/Page 1 of 3/)).toBeInTheDocument()
    })

    it('should disable Previous button on first page', () => {
      render(<MessageLog messages={manyMessages} pageSize={10} />)

      const prevButton = screen.getByText('Previous') as HTMLButtonElement
      expect(prevButton.disabled).toBe(true)
    })

    it('should enable Next button when not on last page', () => {
      render(<MessageLog messages={manyMessages} pageSize={10} />)

      const nextButton = screen.getByText('Next') as HTMLButtonElement
      expect(nextButton.disabled).toBe(false)
    })

    it('should navigate to next page', () => {
      render(<MessageLog messages={manyMessages} pageSize={10} />)

      // First page should show messages 0-9
      expect(screen.getByText('{"msg": 0}', { selector: 'p' })).toBeInTheDocument()
      expect(screen.queryByText('{"msg": 10}', { selector: 'p' })).not.toBeInTheDocument()

      // Click Next
      const nextButton = screen.getByText('Next')
      fireEvent.click(nextButton)

      // Second page should show messages 10-19
      expect(screen.getByText('{"msg": 10}', { selector: 'p' })).toBeInTheDocument()
      expect(screen.queryByText('{"msg": 0}', { selector: 'p' })).not.toBeInTheDocument()
    })

    it('should navigate to previous page', () => {
      render(<MessageLog messages={manyMessages} pageSize={10} />)

      // Go to second page
      const nextButton = screen.getByText('Next')
      fireEvent.click(nextButton)

      // Go back to first page
      const prevButton = screen.getByText('Previous')
      fireEvent.click(prevButton)

      // First page should show messages 0-9
      expect(screen.getByText('{"msg": 0}', { selector: 'p' })).toBeInTheDocument()
    })

    it('should disable Next button on last page', () => {
      render(<MessageLog messages={manyMessages} pageSize={10} />)

      // Navigate to last page
      const nextButton = screen.getByText('Next')
      fireEvent.click(nextButton)
      fireEvent.click(nextButton)

      // Next button should now be disabled
      expect((screen.getByText('Next') as HTMLButtonElement).disabled).toBe(true)
    })

    it('should show correct page count', () => {
      render(<MessageLog messages={manyMessages} pageSize={10} />)

      expect(screen.getByText(/Page 1 of 3/)).toBeInTheDocument()

      fireEvent.click(screen.getByText('Next'))
      expect(screen.getByText(/Page 2 of 3/)).toBeInTheDocument()

      fireEvent.click(screen.getByText('Next'))
      expect(screen.getByText(/Page 3 of 3/)).toBeInTheDocument()
    })

    it('should respect custom pageSize', () => {
      render(<MessageLog messages={manyMessages} pageSize={5} />)

      expect(screen.getByText(/Page 1 of 5/)).toBeInTheDocument()
    })
  })

  describe('Modal functionality', () => {
    it('should open modal when message is clicked', () => {
      render(<MessageLog messages={mockMessages} />)

      const messageButton = screen.getAllByRole('button')[0] // First message button
      fireEvent.click(messageButton)

      expect(screen.getByText('Message Details')).toBeInTheDocument()
    })

    it('should display message metadata in modal', () => {
      render(<MessageLog messages={mockMessages} />)

      fireEvent.click(screen.getAllByRole('button')[0])

      expect(screen.getByText('Metadata')).toBeInTheDocument()
      expect(screen.getByText('ID')).toBeInTheDocument()
      expect(screen.getByText('Status')).toBeInTheDocument()
      expect(screen.getByText('Timestamp')).toBeInTheDocument()
    })

    it('should display message content in modal', () => {
      render(<MessageLog messages={mockMessages} />)

      fireEvent.click(screen.getAllByRole('button')[0])

      expect(screen.getByText('Message')).toBeInTheDocument()
      // Message content should be in a pre tag
      const preElements = screen.getAllByText(/{"test": "message1"}/)
      expect(preElements.length).toBeGreaterThan(0)
    })

    it('should display result section when available', () => {
      render(<MessageLog messages={mockMessages} />)

      // Click on message with result
      fireEvent.click(screen.getAllByRole('button')[1])

      expect(screen.getByText('Result')).toBeInTheDocument()
      expect(screen.getByText(/{"processed": true}/)).toBeInTheDocument()
    })

    it('should display error section when available', () => {
      render(<MessageLog messages={mockMessages} />)

      // Click on message with error
      fireEvent.click(screen.getAllByRole('button')[2])

      expect(screen.getByText('Error')).toBeInTheDocument()
      expect(screen.getByText('Connection timeout')).toBeInTheDocument()
    })

    it('should close modal when X button is clicked', () => {
      render(<MessageLog messages={mockMessages} />)

      fireEvent.click(screen.getAllByRole('button')[0])
      expect(screen.getByText('Message Details')).toBeInTheDocument()

      // Find close button (X)
      const closeButton = screen.getAllByText('×')[0] as HTMLElement
      fireEvent.click(closeButton)

      expect(screen.queryByText('Message Details')).not.toBeInTheDocument()
    })

    it('should close modal when Close button is clicked', () => {
      render(<MessageLog messages={mockMessages} />)

      fireEvent.click(screen.getAllByRole('button')[0])
      expect(screen.getByText('Message Details')).toBeInTheDocument()

      const closeButton = screen.getByRole('button', { name: 'Close' })
      fireEvent.click(closeButton)

      expect(screen.queryByText('Message Details')).not.toBeInTheDocument()
    })

    it('should display message ID in modal', () => {
      render(<MessageLog messages={mockMessages} />)

      fireEvent.click(screen.getAllByRole('button')[0])

      expect(screen.getByText(mockMessages[0].id)).toBeInTheDocument()
    })

    it('should display message status in modal', () => {
      render(<MessageLog messages={mockMessages} />)

      fireEvent.click(screen.getAllByRole('button')[0])

      expect(screen.getByText(/sent/i)).toBeInTheDocument()
    })

    it('should not display result section when not available', () => {
      render(<MessageLog messages={mockMessages} />)

      // Click on message without result
      fireEvent.click(screen.getAllByRole('button')[0])

      expect(screen.queryByText('Result')).not.toBeInTheDocument()
    })

    it('should not display error section when not available', () => {
      render(<MessageLog messages={mockMessages} />)

      // Click on message without error
      fireEvent.click(screen.getAllByRole('button')[0])

      expect(screen.queryByText('Error')).not.toBeInTheDocument()
    })

    it('should show formatted timestamp in modal', () => {
      render(<MessageLog messages={mockMessages} />)

      fireEvent.click(screen.getAllByRole('button')[0])

      // The timestamp should be formatted by toLocaleString()
      expect(screen.getByText(/2024/)).toBeInTheDocument()
    })
  })

  describe('Accessibility', () => {
    it('should render message buttons with proper role', () => {
      render(<MessageLog messages={mockMessages} />)

      const buttons = screen.getAllByRole('button')
      expect(buttons.length).toBeGreaterThan(0)
    })

    it('should have proper heading hierarchy', () => {
      render(<MessageLog messages={mockMessages} />)

      expect(screen.getByText('Message Log')).toBeInTheDocument()
    })
  })
})
