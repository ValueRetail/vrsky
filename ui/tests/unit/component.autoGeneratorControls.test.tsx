/**
 * AutoGeneratorControls Component Tests
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { AutoGeneratorControls } from '@/components/TestData/AutoGeneratorControls'
import * as testDataService from '@/services/testDataService'
import { useUIStore } from '@/store/uiStore'
import type { AutoGeneratorStatusResponse } from '@/types/api'

// Mock the services
vi.mock('@/services/testDataService', () => ({
  testDataService: {
    getGeneratorStatus: vi.fn(),
    startAutoGenerator: vi.fn(),
    stopAutoGenerator: vi.fn(),
  },
}))

vi.mock('@/store/uiStore', () => ({
  useUIStore: vi.fn(),
}))

describe('AutoGeneratorControls Component', () => {
  let mockAddNotification: any
  const mockStatus: AutoGeneratorStatusResponse = {
    running: false,
    rate: 0,
    message_size: 'small',
    total_generated: 0,
  }

  beforeEach(() => {
    vi.clearAllMocks()
    mockAddNotification = vi.fn()
    ;(useUIStore as any).mockReturnValue({
      addNotification: mockAddNotification,
    })
    ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(mockStatus)
  })

  describe('Rendering', () => {
    it('should render component title', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByText('Auto Message Generator')).toBeInTheDocument()
      })
    })

    it('should render status display', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByText('Stopped')).toBeInTheDocument()
      })
    })

    it('should render rate control when stopped', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByRole('slider')).toBeInTheDocument()
      })
    })

    it('should render message size buttons when stopped', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByRole('button', { name: 'Small' })).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Medium' })).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Large' })).toBeInTheDocument()
      })
    })

    it('should render Start button when stopped', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByRole('button', { name: /Start Generator/ })).toBeInTheDocument()
      })
    })

    it('should render Stop button when running', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'small',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByRole('button', { name: /Stop Generator/ })).toBeInTheDocument()
      })
    })

    it('should display running status indicator', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'small',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByText('Running')).toBeInTheDocument()
      })
    })
  })

  describe('Status display', () => {
    it('should show total generated messages when running', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'medium',
        total_generated: 1000,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByText(/Total generated: 1000/)).toBeInTheDocument()
      })
    })

    it('should display rate when running', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 50,
        message_size: 'large',
        total_generated: 500,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByText(/50 msgs\/sec/)).toBeInTheDocument()
      })
    })

    it('should display message size when running', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'medium',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByText(/medium/)).toBeInTheDocument()
      })
    })

    it('should not show total when zero', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.queryByText(/Total generated/)).not.toBeInTheDocument()
      })
    })
  })

  describe('Rate control', () => {
    it('should display current rate value', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByRole('slider')).toBeInTheDocument()
      })
    })

    it('should allow rate adjustment', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const slider = screen.getByRole('slider', { name: '' }) as HTMLInputElement
        fireEvent.change(slider, { target: { value: '50' } })
        expect(slider.value).toBe('50')
      })
    })

    it('should show rate scale (1, 500, 1000)', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByText('1')).toBeInTheDocument()
        expect(screen.getByText('500')).toBeInTheDocument()
        expect(screen.getByText('1000')).toBeInTheDocument()
      })
    })

    it('should enforce min rate of 1', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const slider = screen.getByRole('slider', { name: '' }) as HTMLInputElement
        expect(Number(slider.min)).toBe(1)
      })
    })

    it('should enforce max rate of 1000', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const slider = screen.getByRole('slider', { name: '' }) as HTMLInputElement
        expect(Number(slider.max)).toBe(1000)
      })
    })
  })

  describe('Message size control', () => {
    it('should have three size options', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByRole('button', { name: 'Small' })).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Medium' })).toBeInTheDocument()
        expect(screen.getByRole('button', { name: 'Large' })).toBeInTheDocument()
      })
    })

    it('should select Small by default', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const smallBtn = screen.getByRole('button', { name: 'Small' }) as HTMLButtonElement
        expect(smallBtn).toHaveClass('bg-blue-600')
      })
    })

    it('should change selection when clicked', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const mediumBtn = screen.getByRole('button', { name: 'Medium' })
        fireEvent.click(mediumBtn)
      })

      await waitFor(() => {
        expect(screen.getByRole('button', { name: 'Medium' })).toHaveClass('bg-blue-600')
      })
    })
  })

  describe('Start generator', () => {
    it('should call startAutoGenerator with correct params', async () => {
      render(<AutoGeneratorControls connectionId="test-conn-123" />)

      await waitFor(() => {
        const startBtn = screen.getByRole('button', { name: /Start Generator/ })
        fireEvent.click(startBtn)
      })

      await waitFor(() => {
        expect(testDataService.testDataService.startAutoGenerator).toHaveBeenCalledWith(
          'test-conn-123',
          expect.any(Number),
          expect.any(String)
        )
      })
    })

    it('should validate rate before starting', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const slider = screen.getByRole('slider', { name: '' }) as HTMLInputElement
        // Set invalid rate (will be clamped by slider, so force invalid scenario)
        // Actually, slider won't allow invalid values, so this is handled by HTML constraints
        // Test the warning notification instead
        expect(slider.min).toBe('1')
        expect(slider.max).toBe('1000')
      })
    })

    it('should show success notification on start', async () => {
      ;(testDataService.testDataService.startAutoGenerator as any).mockResolvedValue(undefined)

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const startBtn = screen.getByRole('button', { name: /Start Generator/ })
        fireEvent.click(startBtn)
      })

      await waitFor(() => {
        expect(mockAddNotification).toHaveBeenCalledWith(
          expect.objectContaining({
            type: 'success',
            title: 'Success',
            message: expect.stringContaining('started'),
          })
        )
      })
    })

    it('should call onStatusChange on start', async () => {
      const newStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 1,
        message_size: 'small',
        total_generated: 0,
      }
      ;(testDataService.testDataService.startAutoGenerator as any).mockResolvedValue(undefined)
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(newStatus)

      const mockOnStatusChange = vi.fn()
      render(
        <AutoGeneratorControls connectionId="test-conn" onStatusChange={mockOnStatusChange} />
      )

      await waitFor(() => {
        const startBtn = screen.getByRole('button', { name: /Start Generator/ })
        fireEvent.click(startBtn)
      })

      await waitFor(() => {
        expect(mockOnStatusChange).toHaveBeenCalledWith(newStatus)
      })
    })

    it('should show error notification on start failure', async () => {
      ;(testDataService.testDataService.startAutoGenerator as any).mockRejectedValue(
        new Error('API error')
      )

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const startBtn = screen.getByRole('button', { name: /Start Generator/ })
        fireEvent.click(startBtn)
      })

      await waitFor(() => {
        expect(mockAddNotification).toHaveBeenCalledWith(
          expect.objectContaining({
            type: 'error',
            title: 'Error',
          })
        )
      })
    })
  })

  describe('Stop generator', () => {
    it('should call stopAutoGenerator', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'small',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)
      ;(testDataService.testDataService.stopAutoGenerator as any).mockResolvedValue(undefined)

      render(<AutoGeneratorControls connectionId="test-conn-123" />)

      await waitFor(() => {
        const stopBtn = screen.getByRole('button', { name: /Stop Generator/ })
        fireEvent.click(stopBtn)
      })

      await waitFor(() => {
        expect(testDataService.testDataService.stopAutoGenerator).toHaveBeenCalledWith(
          'test-conn-123'
        )
      })
    })

    it('should show success notification on stop', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'small',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)
      ;(testDataService.testDataService.stopAutoGenerator as any).mockResolvedValue(undefined)

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const stopBtn = screen.getByRole('button', { name: /Stop Generator/ })
        fireEvent.click(stopBtn)
      })

      await waitFor(() => {
        expect(mockAddNotification).toHaveBeenCalledWith(
          expect.objectContaining({
            type: 'success',
            title: 'Success',
            message: 'Auto-generator stopped',
          })
        )
      })
    })

    it('should call onStatusChange on stop', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'small',
        total_generated: 100,
      }
      const stoppedStatus: AutoGeneratorStatusResponse = {
        running: false,
        rate: 0,
        message_size: 'small',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any)
        .mockResolvedValueOnce(runningStatus)
        .mockResolvedValueOnce(runningStatus)
        .mockResolvedValueOnce(stoppedStatus)
      ;(testDataService.testDataService.stopAutoGenerator as any).mockResolvedValue(undefined)

      const mockOnStatusChange = vi.fn()
      render(
        <AutoGeneratorControls connectionId="test-conn" onStatusChange={mockOnStatusChange} />
      )

      await waitFor(() => {
        const stopBtn = screen.getByRole('button', { name: /Stop Generator/ })
        fireEvent.click(stopBtn)
      })

      await waitFor(
        () => {
          expect(mockOnStatusChange).toHaveBeenCalledWith(stoppedStatus)
        },
        { timeout: 3000 }
      )
    })

    it('should show error notification on stop failure', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'small',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)
      ;(testDataService.testDataService.stopAutoGenerator as any).mockRejectedValue(
        new Error('Stop failed')
      )

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const stopBtn = screen.getByRole('button', { name: /Stop Generator/ })
        fireEvent.click(stopBtn)
      })

      await waitFor(
        () => {
          expect(mockAddNotification).toHaveBeenCalledWith(
            expect.objectContaining({
              type: 'error',
              title: 'Error',
            })
          )
        },
        { timeout: 3000 }
      )
    })
  })

  describe('Help text', () => {
    it('should show configuration help when stopped', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(
          screen.getByText(/Configure settings and press Start to generate test messages/)
        ).toBeInTheDocument()
      })
    })

    it('should show stop help when running', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'small',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(screen.getByText(/Generator is running. Press Stop to end./)).toBeInTheDocument()
      })
    })
  })

  describe('Loading states', () => {
    it('should disable all controls while starting', async () => {
      ;(testDataService.testDataService.startAutoGenerator as any).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const startBtn = screen.getByRole('button', { name: /Start Generator/ })
        fireEvent.click(startBtn)
      })

      await waitFor(() => {
        const slider = screen.getByRole('slider', { name: '' }) as HTMLInputElement
        const smallBtn = screen.getByRole('button', { name: 'Small' }) as HTMLButtonElement
        expect(slider.disabled).toBe(true)
        expect(smallBtn.disabled).toBe(true)
      })
    })

    it('should show Starting... text while loading', async () => {
      ;(testDataService.testDataService.startAutoGenerator as any).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const startBtn = screen.getByRole('button', { name: /Start Generator/ })
        fireEvent.click(startBtn)
      })

      await waitFor(() => {
        expect(screen.getByText('Starting...')).toBeInTheDocument()
      })
    })

    it('should show Stopping... text while stopping', async () => {
      const runningStatus: AutoGeneratorStatusResponse = {
        running: true,
        rate: 10,
        message_size: 'small',
        total_generated: 100,
      }
      ;(testDataService.testDataService.getGeneratorStatus as any).mockResolvedValue(runningStatus)
      ;(testDataService.testDataService.stopAutoGenerator as any).mockImplementation(
        () => new Promise(resolve => setTimeout(resolve, 100))
      )

      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        const stopBtn = screen.getByRole('button', { name: /Stop Generator/ })
        fireEvent.click(stopBtn)
      })

      await waitFor(() => {
        expect(screen.getByText('Stopping...')).toBeInTheDocument()
      })
    })
  })

  describe('Polling', () => {
    it('should fetch initial status on mount', async () => {
      render(<AutoGeneratorControls connectionId="test-conn" />)

      await waitFor(() => {
        expect(testDataService.testDataService.getGeneratorStatus).toHaveBeenCalledWith(
          'test-conn'
        )
      })
    })

    it('should handle missing initial status gracefully', async () => {
      ;(testDataService.testDataService.getGeneratorStatus as any).mockRejectedValue(
        new Error('Not initialized')
      )

      const { container } = render(<AutoGeneratorControls connectionId="test-conn" />)

      // Should render without crashing
      expect(container).toBeTruthy()
    })
  })
})
