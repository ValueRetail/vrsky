/**
 * TestMessageForm Component Tests
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
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
  let mockAddNotification: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.clearAllMocks()
    mockAddNotification = vi.fn()
    vi.mocked(useUIStore).mockReturnValue({
      addNotification: mockAddNotification,
    } as unknown as ReturnType<typeof useUIStore>)
  })

  // A test that ends while a send is still in flight lets the component's
  // `finally { setLoading(false) }` run after the test, and on a slow CI runner
  // after the jsdom environment is gone: "ReferenceError: window is not
  // defined", an unhandled rejection that fails the whole Test UI job (main,
  // 2026-10-22). This runs before the shared cleanup (hooks unwind in reverse
  // order) and fails any such test deterministically, whatever the timing.
  afterEach(() => {
    try {
      expect(
        screen.queryByText('Sending...'),
        'the test ended with a send still in flight; await sendSettled() before it returns'
      ).toBeNull()
    } finally {
      // A failing after-hook skips the shared cleanup; unmount here too so one
      // offender does not leak its form into every test after it.
      cleanup()
    }
  })

  // Wait for the send a test started to finish: the button leaves "Sending...".
  const sendSettled = () =>
    waitFor(() => {
      expect(screen.queryByText('Sending...')).toBeNull()
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
      vi.mocked(testDataService.testDataService.sendTestMessage).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      // Button should be disabled while sending
      await waitFor(() => {
        expect(sendButton).toBeDisabled()
      })

      await sendSettled()
    })

    it('should disable textarea when loading', async () => {
      vi.mocked(testDataService.testDataService.sendTestMessage).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      const sendButton = screen.getByRole('button', { name: /Send Message/ })

      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(textarea).toBeDisabled()
      })

      await sendSettled()
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
      vi.mocked(testDataService.testDataService.sendTestMessage).mockResolvedValue(undefined)

      render(<TestMessageForm connectionId="test-conn-123" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(testDataService.testDataService.sendTestMessage).toHaveBeenCalledWith(
          'test-conn-123',
          '{"test": "message"}'
        )
      })

      await sendSettled()
    })

    it('should show success notification after sending', async () => {
      vi.mocked(testDataService.testDataService.sendTestMessage).mockResolvedValue(undefined)

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

      await sendSettled()
    })

    it('should reset message after successful send', async () => {
      vi.mocked(testDataService.testDataService.sendTestMessage).mockResolvedValue(undefined)

      render(<TestMessageForm connectionId="test-conn" />)

      const textarea = screen.getByPlaceholderText(/Enter JSON message/) as HTMLTextAreaElement
      fireEvent.change(textarea, { target: { value: '{"custom": "message"}' } })

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(textarea.value).toBe('{"test": "message"}')
      })

      await sendSettled()
    })

    it('should call onMessageSent callback after successful send', async () => {
      vi.mocked(testDataService.testDataService.sendTestMessage).mockResolvedValue(undefined)

      const mockOnMessageSent = vi.fn()
      render(<TestMessageForm connectionId="test-conn" onMessageSent={mockOnMessageSent} />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(mockOnMessageSent).toHaveBeenCalled()
      })

      await sendSettled()
    })

    it('should show error notification on failure', async () => {
      const error = new Error('Network error')
      vi.mocked(testDataService.testDataService.sendTestMessage).mockRejectedValue(error)

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

      await sendSettled()
    })

    it('should not call onMessageSent on error', async () => {
      vi.mocked(testDataService.testDataService.sendTestMessage).mockRejectedValue(
        new Error('API error')
      )

      const mockOnMessageSent = vi.fn()
      render(<TestMessageForm connectionId="test-conn" onMessageSent={mockOnMessageSent} />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)
      await sendSettled()

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
      vi.mocked(testDataService.testDataService.sendTestMessage).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      const resetButton = screen.getByRole('button', { name: /Reset/ })

      await waitFor(() => {
        expect(resetButton).toBeDisabled()
      })

      await sendSettled()
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
      vi.mocked(testDataService.testDataService.sendTestMessage).mockRejectedValue(apiError)

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

      await sendSettled()
    })

    it('should show default error message for non-API errors', async () => {
      vi.mocked(testDataService.testDataService.sendTestMessage).mockRejectedValue(
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

      await sendSettled()
    })
  })

  describe('Loading state', () => {
    it('should show Sending... text while loading', async () => {
      vi.mocked(testDataService.testDataService.sendTestMessage).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(screen.getByText('Sending...')).toBeInTheDocument()
      })

      await sendSettled()
    })

    it('should show Stopping... text while stopping', async () => {
      vi.mocked(testDataService.testDataService.sendTestMessage).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<TestMessageForm connectionId="test-conn" />)

      const sendButton = screen.getByRole('button', { name: /Send Message/ })
      fireEvent.click(sendButton)

      await waitFor(() => {
        expect(sendButton).toHaveTextContent('Sending...')
      })

      await sendSettled()
    })
  })
})
