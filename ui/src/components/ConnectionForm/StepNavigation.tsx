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
                onClick={() => onSelectStep(index)}
                disabled={index > currentStep}
                className={`flex items-center justify-center w-10 h-10 rounded-full font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${
                  index === currentStep
                    ? 'bg-blue-600 text-white'
                    : index < currentStep
                      ? 'bg-green-600 text-white'
                      : 'bg-gray-200 text-gray-700'
                }`}
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
