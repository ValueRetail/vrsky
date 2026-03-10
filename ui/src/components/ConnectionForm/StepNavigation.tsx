interface Step {
  title: string
  description: string
}

interface StepNavigationProps {
  currentStep: number
  steps: Step[]
  onSelectStep: (step: number) => void
}

export default function StepNavigation({ currentStep, steps, onSelectStep }: StepNavigationProps) {
  return (
    <div>
      <div className="flex items-center justify-between mb-8">
        {steps.map((step, index) => (
          <div key={index} className="flex-1">
            <div className="flex items-center">
              <button
                onClick={() => {
                  console.log('Clicking step', index)
                  onSelectStep(index)
                }}
                disabled={index > currentStep}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  width: '40px',
                  height: '40px',
                  borderRadius: '50%',
                  fontWeight: 'bold',
                  fontSize: '16px',
                  border: 'none',
                  cursor: index > currentStep ? 'not-allowed' : 'pointer',
                  backgroundColor: index === currentStep ? '#2563eb' : index < currentStep ? '#16a34a' : '#d1d5db',
                  color: index > currentStep ? '#9ca3af' : index === currentStep ? '#fff' : index < currentStep ? '#fff' : '#374151',
                  opacity: index > currentStep ? 0.5 : 1,
                  transition: 'all 150ms ease-in-out',
                  zIndex: 10,
                  position: 'relative',
                }}
              >
                {index < currentStep ? '✓' : index + 1}
              </button>
              {index < steps.length - 1 && (
                <div
                  className={`flex-1 h-1 mx-2 ${
                    index < currentStep ? 'bg-green-600' : 'bg-gray-200'
                  }`}
                />
              )}
            </div>
            <div className="mt-2 text-center">
              <p className="font-medium text-sm text-gray-900">{step.title}</p>
              <p className="text-xs text-gray-600">{step.description}</p>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
