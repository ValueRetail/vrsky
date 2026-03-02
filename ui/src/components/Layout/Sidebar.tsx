/**
 * Sidebar Component
 * Left navigation menu
 */

import { Link, useLocation } from 'react-router-dom'
import { useConnectionsStore } from '../../store/connectionsStore'

export default function Sidebar() {
  const { pathname } = useLocation()
  const { connections } = useConnectionsStore()
  const runningCount = connections.filter((c: typeof connections[0]) => c.status === 'running').length

  const isActive = (path: string) => pathname === path || pathname.startsWith(path + '/')

  return (
    <aside className="w-64 bg-white border-r border-gray-200 p-6 flex flex-col">
      <nav className="space-y-2 flex-1">
        <Link
          to="/"
          className={`block px-4 py-2 rounded-lg font-medium transition-colors ${
            isActive('/')
              ? 'bg-blue-50 text-blue-600'
              : 'text-gray-700 hover:bg-gray-50'
          }`}
        >
          Dashboard
        </Link>

        <Link
          to="/connections"
          className={`block px-4 py-2 rounded-lg font-medium transition-colors ${
            isActive('/connections')
              ? 'bg-blue-50 text-blue-600'
              : 'text-gray-700 hover:bg-gray-50'
          }`}
        >
          Connections
          {connections.length > 0 && (
            <span className="ml-2 inline-block bg-gray-200 text-gray-700 text-xs px-2 py-0.5 rounded-full">
              {connections.length}
            </span>
          )}
        </Link>

        {runningCount > 0 && (
          <div className="px-4 py-2 text-xs font-medium text-green-700 bg-green-50 border border-green-200 rounded-lg">
            {runningCount} running
          </div>
        )}
      </nav>

      <div className="pt-6 border-t border-gray-200 space-y-2">
        <Link
          to="/connections/create"
          className="block px-4 py-2 bg-blue-500 text-white rounded-lg font-medium hover:bg-blue-600 transition-colors text-center"
        >
          + New Connection
        </Link>
      </div>
    </aside>
  )
}
