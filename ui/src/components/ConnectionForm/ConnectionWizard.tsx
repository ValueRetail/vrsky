import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import z from 'zod'
import StepNavigation from './StepNavigation'
import BasicInfoStep from './steps/BasicInfoStep'
import SourceStep from './steps/SourceStep'
import ConverterStep from './steps/ConverterStep'
import FilterStep from './steps/FilterStep'
import DestinationStep from './steps/DestinationStep'
import ReviewStep from './steps/ReviewStep'
import type { ConnectionFormData } from '../../types/forms'

const basicInfoSchema = z.object({
  basicInfo: z.object({
    name: z.string().min(1, 'Name is required').min(3, 'Name must be at least 3 characters'),
    description: z.string().default(''),
  }),
})

interface ConnectionWizardProps {
  initialData?: ConnectionFormData
  onSubmit: (data: ConnectionFormData) => Promise<void>
  onCancel: () => void
}

const defaultFormData: ConnectionFormData = {
  basicInfo: { name: '', description: '' },
  source: { type: 'http', config: { url: '', method: 'GET', headers: {} } },
  converter: { type: 'schema', config: { inputSchema: {} } },
  filter: { type: 'rules', config: { rules: [] } },
  destination: { type: 'http', config: { url: '', method: 'POST', headers: {} } },
}

export default function ConnectionWizard({ initialData, onSubmit, onCancel }: ConnectionWizardProps) {
  const [currentStep, setCurrentStep] = useState(0)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [formData, setFormData] = useState<ConnectionFormData>(initialData || defaultFormData)

  const { watch } = useForm({
    resolver: zodResolver(basicInfoSchema),
    defaultValues: formData,
    mode: 'onChange',
  })

  const basicInfo = watch('basicInfo', formData.basicInfo)

  const steps = [
    { title: 'Basic Info', description: 'Connection name and description' },
    { title: 'Source', description: 'Data source configuration' },
    { title: 'Converter', description: 'Data transformation rules' },
    { title: 'Filter', description: 'Message filtering logic' },
    { title: 'Destination', description: 'Data destination configuration' },
    { title: 'Review', description: 'Review and submit' },
  ]

  const handleNext = () => {
    if (currentStep === 0) {
      // Validate basic info
      if (!basicInfo.name) {
        return
      }
      // Update with description guaranteed as string
      setFormData(prev => ({
        ...prev,
        basicInfo: {
          name: basicInfo.name,
          description: basicInfo.description || '',
        },
      }))
    }
    setCurrentStep(prev => Math.min(prev + 1, steps.length - 1))
  }

  const handlePrevious = () => {
    setCurrentStep(prev => Math.max(prev - 1, 0))
  }

  const handleBasicInfoUpdate = (_field: string, data: unknown) => {
    setFormData(prev => ({
      ...prev,
      basicInfo: data as any,
    }))
  }

  const handleStepUpdate = (stepName: Exclude<keyof ConnectionFormData, 'basicInfo'>, data: unknown) => {
    setFormData(prev => ({
      ...prev,
      [stepName]: data,
    }))
  }

  const handleFinalSubmit = async () => {
    try {
      setIsSubmitting(true)
      await onSubmit(formData)
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <div className="bg-white rounded-lg border border-gray-200 p-8">
      <StepNavigation
        currentStep={currentStep}
        steps={steps}
        onSelectStep={setCurrentStep}
      />

      <div className="mt-8 mb-8 min-h-[400px]">
        {currentStep === 0 && <BasicInfoStep formData={formData} onChange={handleBasicInfoUpdate} />}
        {currentStep === 1 && <SourceStep formData={formData} onChange={handleStepUpdate} />}
        {currentStep === 2 && <ConverterStep formData={formData} onChange={handleStepUpdate} />}
        {currentStep === 3 && <FilterStep formData={formData} onChange={handleStepUpdate} />}
        {currentStep === 4 && <DestinationStep formData={formData} onChange={handleStepUpdate} />}
        {currentStep === 5 && <ReviewStep formData={formData} />}
      </div>

      <div className="flex justify-between pt-8 border-t border-gray-200">
        <button
          onClick={onCancel}
          className="px-6 py-2 text-gray-700 bg-gray-100 rounded-md hover:bg-gray-200"
        >
          Cancel
        </button>
        <div className="flex gap-3">
          {currentStep > 0 && (
            <button
              onClick={handlePrevious}
              className="px-6 py-2 text-gray-700 bg-gray-100 rounded-md hover:bg-gray-200"
            >
              Previous
            </button>
          )}
          {currentStep < steps.length - 1 && (
            <button
              onClick={handleNext}
              className="px-6 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700"
            >
              Next
            </button>
          )}
          {currentStep === steps.length - 1 && (
            <button
              onClick={handleFinalSubmit}
              disabled={isSubmitting}
              className="px-6 py-2 bg-green-600 text-white rounded-md hover:bg-green-700 disabled:bg-gray-400"
            >
              {isSubmitting ? 'Creating...' : 'Create Connection'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
