import { useNavigate } from 'react-router-dom'
import { useUIStore } from '../store/uiStore'
import { connectionService } from '../services/connectionService'
import { isAPIError, getErrorMessage } from '../utils/errors'
import { buildSourceConfig, buildConverterConfig, buildFilterConfig, buildDestinationConfig } from '../utils/typeGuards'
import ConnectionWizard from '../components/ConnectionForm/ConnectionWizard'
import type { ConnectionFormData } from '../types/forms'

export default function CreateConnection() {
  const navigate = useNavigate()
  const { addNotification } = useUIStore()

  const handleSubmit = async (formData: ConnectionFormData) => {
    try {
      // Convert form data structure to API format using standardized builders
      const sourceConfig = buildSourceConfig(formData.source.type, formData.source.config)
      const converterConfig = buildConverterConfig(formData.converter.type, formData.converter.config)
      const filterConfig = buildFilterConfig(formData.filter.type, formData.filter.config)
      const destinationConfig = buildDestinationConfig(formData.destination.type, formData.destination.config)

      const connectionData = {
        name: formData.basicInfo.name,
        description: formData.basicInfo.description,
        source_config: sourceConfig,
        converter_config: converterConfig,
        filter_config: filterConfig,
        destination_config: destinationConfig,
      }

      await connectionService.create(connectionData)
      addNotification({
        type: 'success',
        title: 'Success',
        message: 'Connection created successfully',
      })
      navigate('/')
    } catch (error) {
      const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to create connection'
      addNotification({
        type: 'error',
        title: 'Error',
        message,
      })
    }
  }

  const handleCancel = () => {
    navigate('/')
  }

  return (
    <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 py-8 px-4 sm:px-6 lg:px-8">
      <div className="max-w-4xl mx-auto">
        {/* Header Section */}
        <div className="mb-8 animate-fade-in">
          <button
            onClick={() => navigate('/')}
            className="flex items-center gap-2 text-primary-600 dark:text-primary-400 hover:text-primary-700 dark:hover:text-primary-300 font-medium mb-4 transition-colors duration-base"
          >
            <span>←</span>
            <span>Back to Connections</span>
          </button>
          <div className="space-y-2">
            <h1 className="text-4xl sm:text-5xl font-bold bg-gradient-to-r from-primary-600 to-secondary-600 dark:from-primary-400 dark:to-secondary-400 bg-clip-text text-transparent">
              Create New Connection
            </h1>
            <p className="text-lg text-neutral-600 dark:text-neutral-400">
              Configure a new data pipeline to connect your systems
            </p>
          </div>
        </div>

        {/* Wizard Card */}
        <div className="card-elevated animate-slide-in-up">
          <ConnectionWizard onSubmit={handleSubmit} onCancel={handleCancel} />
        </div>
      </div>
    </div>
  )
}
