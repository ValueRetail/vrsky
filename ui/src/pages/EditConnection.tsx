import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useUIStore } from '../store/uiStore'
import { connectionService } from '../services/connectionService'
import { isAPIError, getErrorMessage } from '../utils/errors'
import { buildSourceConfig, buildConverterConfig, buildFilterConfig, buildDestinationConfig, toSourceFormData, toConverterFormData, toFilterFormData, toDestinationFormData } from '../utils/typeGuards'
import ConnectionWizard from '../components/ConnectionForm/ConnectionWizard'
import type { Connection } from '../types/models'
import type { ConnectionFormData } from '../types/forms'

export default function EditConnection() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [connection, setConnection] = useState<Connection | null>(null)
  const [loading, setLoading] = useState(true)
  const { addNotification } = useUIStore()

  useEffect(() => {
    if (!id) {
      navigate('/404')
      return
    }

    const loadConnection = async () => {
      try {
        setLoading(true)
        const data = await connectionService.get(id)
        setConnection(data)
      } catch (error) {
        const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to load connection'
        addNotification({
          type: 'error',
          title: 'Error',
          message,
        })
        navigate('/')
      } finally {
        setLoading(false)
      }
    }

    loadConnection()
  }, [id, navigate, addNotification])

  const handleSubmit = async (formData: ConnectionFormData) => {
    if (!connection) return

    try {
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

      await connectionService.update(connection.id, connectionData)
      addNotification({
        type: 'success',
        title: 'Success',
        message: 'Connection updated successfully',
      })
      navigate(`/connections/${connection.id}`)
    } catch (error) {
      const message = isAPIError(error) ? getErrorMessage(error) : 'Failed to update connection'
      addNotification({
        type: 'error',
        title: 'Error',
        message,
      })
    }
  }

  const handleCancel = () => {
    if (connection) {
      navigate(`/connections/${connection.id}`)
    } else {
      navigate('/')
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center min-h-screen">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-blue-600"></div>
      </div>
    )
  }

  if (!connection) {
    return (
      <div className="max-w-4xl mx-auto px-4 py-8">
        <div className="text-center">
          <h1 className="text-2xl font-bold text-gray-900">Connection not found</h1>
          <button
            onClick={() => navigate('/')}
            className="mt-4 px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700"
          >
            Back to Dashboard
          </button>
        </div>
      </div>
    )
  }

  // Convert API data to form data structure
  const initialFormData: ConnectionFormData = {
    basicInfo: {
      name: connection.name,
      description: connection.description,
    },
    source: {
      type: connection.source_config.type,
      config: toSourceFormData(connection.source_config),
    },
    converter: {
      type: connection.converter_config.type,
      config: toConverterFormData(connection.converter_config),
    },
    filter: {
      type: connection.filter_config.type,
      config: toFilterFormData(connection.filter_config),
    },
    destination: {
      type: connection.destination_config.type,
      config: toDestinationFormData(connection.destination_config),
    },
  }

  return (
    <div className="max-w-4xl mx-auto px-4 py-8">
      <div className="mb-8">
        <button
          onClick={() => navigate(`/connections/${connection.id}`)}
          className="text-blue-600 hover:text-blue-700 mb-2"
        >
          ← Back
        </button>
        <h1 className="text-3xl font-bold text-gray-900">Edit Connection</h1>
        <p className="text-gray-600 mt-1">{connection.name}</p>
      </div>

      <ConnectionWizard
        initialData={initialFormData}
        onSubmit={handleSubmit}
        onCancel={handleCancel}
      />
    </div>
  )
}
