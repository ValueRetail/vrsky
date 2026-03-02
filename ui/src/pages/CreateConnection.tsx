import { useNavigate } from 'react-router-dom'
import { useUIStore } from '../store/uiStore'
import { connectionService } from '../services/connectionService'
import { isAPIError, getErrorMessage } from '../utils/errors'
import ConnectionWizard from '../components/ConnectionForm/ConnectionWizard'
import type { ConnectionFormData } from '../types/forms'

export default function CreateConnection() {
  const navigate = useNavigate()
  const { addNotification } = useUIStore()

  const handleSubmit = async (formData: ConnectionFormData) => {
    try {
      // Convert form data structure to API format
      // Extract configs from form structure
      const sourceConfig = {
        type: formData.source.type,
        ...(formData.source.config as any),
      }

      const converterConfig = {
        type: formData.converter.type,
        ...(formData.converter.config as any),
      }

      const filterConfig = {
        type: formData.filter.type,
        ...(formData.filter.config as any),
      }

      const destinationConfig = {
        type: formData.destination.type,
        ...(formData.destination.config as any),
      }

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
    <div className="max-w-4xl mx-auto px-4 py-8">
      <div className="mb-8">
        <button
          onClick={() => navigate('/')}
          className="text-blue-600 hover:text-blue-700 mb-2"
        >
          ← Back
        </button>
        <h1 className="text-3xl font-bold text-gray-900">Create New Connection</h1>
        <p className="text-gray-600 mt-1">Configure a new data pipeline connection</p>
      </div>

      <ConnectionWizard onSubmit={handleSubmit} onCancel={handleCancel} />
    </div>
  )
}
