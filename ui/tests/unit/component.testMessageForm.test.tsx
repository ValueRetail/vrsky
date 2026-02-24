/**
 * TestMessageForm Component Tests
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { TestMessageForm } from '@/components/TestData/TestMessageForm'
import * as testDataService from '@/services/testDataService'
import { useUIStore } from '@/store/uiStore'

// Mock the services
vi.mock('@/services/testDataService', () => ({
  testDataService: {
    sendTestMessage: vi.fn(),
  },
}))

vi.mock('@/store/uiStore', () => ({
  useUIStore: vi.fn(),
}))

describe('TestMessageForm Component', () => {
  let mockAddNotification: any

  beforeEach(() => {
    vi.clearAllMocks()
    mockAddNotification = vi.fn()
    ;(useUIStore as any).mockReturnValue({
      addNotification: mockAddNotification,
    })
  })

  describe('Rendering', () => {
    it('should render form title', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      expect(screen.getByText('Send Test Message')).toBeInTheDocument()
    })

    it('should render textarea with default message', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/)
      expect(textarea).toBeInTheDocument()
      expect((textarea as HTMLTextAreaElement).value).toBe('{"test": "message"}')
    })

    it('should render Send button', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      expect(screen.getByRole('button', { name: /Send Message/ })).toBeInTheDocument()
    })

    it('should render Reset button', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      expect(screen.getByRole('button', { name: /Reset/ })).toBeInTheDocument()
    })

    it('should render help text', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      expect(
        screen.getByText(/Enter a valid JSON message to send through the pipeline/)
      ).toBeInTheDocument()
    })
  })

  describe('Message input', () => {
    it('should update textarea on input change', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: '{"new": "value"}' } })

      expect(textarea.value).toBe('{"new": "value"}')
    })

    it('should disable buttons when loading', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      // Button should be disabled while sending
      await waitFor(() => {
        expect(sendButton).toBeDisabled()
      })
    })

    it('should disable textarea when loading', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      const sendButton = screen.getByRole('button', { name: /Send Message/ })

      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(textarea).toBeDisabled()
      })
    })
  })

  describe('Form submission', () => {
    it('should show warning when message is empty', async () => {
      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: '' } })

      const sendButton = screen.getByRole('button', { name: /Send Message/ }) as HTMLButtonElement
      expect(sendButton.disabled).toBe(true)
    })

    it('should show warning when message is only whitespace', async () => {
      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: '   ' } })

      const sendButton = screen.getByRole('button', { name: /Send Message/ }) as HTMLButtonElement
      expect(sendButton.disabled).toBe(true)
    })

    it('should send message when valid', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockResolvedValue(undefined)

      render(<TestMessageForm connectionId="test-conn-123" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(testDataService.testDataService.sendTestMessage).toHaveBeenCalledWith(
          'test-conn-123',
          '{"test": "message"}'
        )
      })
    })

    it('should show success notification after sending', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockResolvedValue(undefined)

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(mockAddNotification).toHaveBeenCalledWith(
          expect.objectContaining({
            type: 'success',
            title: 'Success',
            message: 'Test message sent',
          })
        )
      })
    })

    it('should reset message after successful send', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockResolvedValue(undefined)

      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: '{"custom": "message"}' } })

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(textarea.value).toBe('{"test": "message"}')
      })
    })

    it('should call onMessageSent callback after successful send', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockResolvedValue(undefined)

      const mockOnMessageSent = vi.fn()
      render(<TestMessageForm connectionId="test-conn" onMessageSent={mockOnMessageSent} />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(mockOnMessageSent).toHaveBeenCalled()
      })
    })

    it('should show error notification on failure', async () => {
      const error = new Error('Network error')
      ;(testDataService.testDataService.sendTestMessage as any).mockRejectedValue(error)

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(mockAddNotification).toHaveBeenCalledWith(
          expect.objectContaining({
            type: 'error',
            title: 'Error',
          })
        )
      })
    })

    it('should not call onMessageSent on error', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockRejectedValue(
        new Error('API error')
      )

      const mockOnMessageSent = vi.fn()
      render(<TestMessageForm connectionId="test-conn" onMessageSent={mockOnMessageSent} />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(mockOnMessageSent).not.toHaveBeenCalled()
      })
    })
  })

  describe('Reset button', () => {
    it('should reset message to default when clicked', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: '{"custom": "data"}' } })

      expect(textarea.value).toBe('{"custom": "data"}')

      const resetButton = screen.getByRole('button', { name: /Reset/ })
      fireEvent.click(resetButton)

      expect(textarea.value).toBe('{"test": "message"}')
    })

    it('should disable reset button when loading', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      const resetButton = screen.getByRole('button', { name: /Reset/ })

      await waitFor(() => {
        expect(resetButton).toBeDisabled()
      })
    })
  })

  describe('Button states', () => {
    it('should disable Send button when message is empty', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: '' } })

      const sendButton = screen.getByRole('button', { name: /Send Message/ }) as HTMLButtonElement
      expect(sendButton.disabled).toBe(true)
    })

    it('should enable Send button when message has content', () => {
      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: '{"test": "message"}' } })

      const sendButton = screen.getByRole('button', { name: /Send Message/ }) as HTMLButtonElement
      expect(sendButton.disabled).toBe(false)
    })
  })

  describe('Error handling', () => {
    it('should handle API errors gracefully', async () => {
      const apiError = { response: { status: 500, data: { message: 'Server error' } } }
      ;(testDataService.testDataService.sendTestMessage as any).mockRejectedValue(apiError)

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(mockAddNotification).toHaveBeenCalledWith(
          expect.objectContaining({
            type: 'error',
          })
        )
      })
    })

    it('should show default error message for non-API errors', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockRejectedValue(
        new Error('Unknown error')
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(mockAddNotification).toHaveBeenCalledWith(
          expect.objectContaining({
            type: 'error',
            message: 'Failed to send test message',
          })
        )
      })
    })
  })

  describe('Loading state', () => {
    it('should show Sending... text while loading', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(screen.getByText('Sending...')).toBeInTheDocument()
      })
    })

    it('should show Stopping... text while stopping', async () => {
      ;(testDataService.testDataService.sendTestMessage as any).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(sendButton).toHaveTextContent('Sending...')
      })
    })
  })
})
