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
      <div className="flex items-center justify-center min-h-screen bg-neutral-50 dark:bg-neutral-950">
        <div className="space-y-4 text-center">
          <div className="flex justify-center">
            <div className="animate-spin rounded-full h-16 w-16 border-4 border-primary-200 dark:border-primary-900 border-t-primary-600 dark:border-t-primary-400"></div>
          </div>
          <p className="text-neutral-600 dark:text-neutral-400 font-medium">Loading connection...</p>
        </div>
      </div>
    )
  }

  if (!connection) {
    return (
      <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 flex items-center justify-center px-4">
        <div className="card-elevated text-center py-12 max-w-md">
          <div className="text-5xl mb-4">⚠️</div>
          <h1 className="text-2xl font-bold text-neutral-900 dark:text-neutral-50 mb-2">Connection not found</h1>
          <p className="text-neutral-600 dark:text-neutral-400 mb-6">The connection you're looking for doesn't exist or has been deleted.</p>
          <button
            onClick={() => navigate('/')}
            className="btn-primary"
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
    <div className="min-h-screen bg-neutral-50 dark:bg-neutral-950 py-8 px-4 sm:px-6 lg:px-8">
      <div className="max-w-4xl mx-auto">
        {/* Header Section */}
        <div className="mb-8 animate-fade-in">
          <button
            onClick={() => navigate(`/connections/${connection.id}`)}
            className="flex items-center gap-2 text-primary-600 dark:text-primary-400 hover:text-primary-700 dark:hover:text-primary-300 font-medium mb-4 transition-colors duration-base"
          >
            <span>←</span>
            <span>Back to Connection</span>
          </button>
          <div className="space-y-2">
            <h1 className="text-4xl sm:text-5xl font-bold bg-gradient-to-r from-primary-600 to-secondary-600 dark:from-primary-400 dark:to-secondary-400 bg-clip-text text-transparent">
              Edit Connection
            </h1>
            <p className="text-lg text-neutral-600 dark:text-neutral-400">
              Modify {connection.name}
            </p>
          </div>
        </div>

        {/* Wizard Card */}
        <div className="card-elevated animate-slide-in-up">
          <ConnectionWizard
            initialData={initialFormData}
            onSubmit={handleSubmit}
            onCancel={handleCancel}
          />
        </div>
      </div>
    </div>
  )
}
