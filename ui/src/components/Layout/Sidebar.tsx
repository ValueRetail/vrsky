/**
 * Sidebar Component
 * Left navigation menu
 */

import { Link, useLocation } from 'react-router-dom'
import { useConnectionsStore } from '../../store/connectionsStore'

export default function Sidebar() {
  const { pathname } = useLocation()
  const { connections = [] } = useConnectionsStore()
  const runningCount = (connections || []).filter((c) => c.status === 'running').length
  const stoppedCount = (connections || []).filter((c) => c.status === 'stopped').length
  const errorCount = (connections || []).filter((c) => c.status === 'error').length

  const isActive = (path: string) => pathname === path || pathname.startsWith(path + '/')

  return (
    <aside className="w-64 bg-white dark:bg-neutral-800 border-r border-neutral-200 dark:border-neutral-700 flex flex-col transition-colors duration-base">
      {/* Navigation Menu */}
      <nav className="flex-1 px-4 py-6 space-y-1 overflow-y-auto">
        {/* Dashboard Link */}
        <Link
          to="/"
          className={`flex items-center gap-3 px-4 py-2 rounded-lg font-semibold text-sm transition-all duration-fast ${
            isActive('/')
              ? 'bg-primary-100 dark:bg-primary-900/30 text-primary-700 dark:text-primary-300'
              : 'text-neutral-700 dark:text-neutral-300 hover:bg-neutral-100 dark:hover:bg-neutral-700/50'
          }`}
        >
          <span>📊 Dashboard</span>
        </Link>

        {/* Connections Link */}
        <Link
          to="/connections"
          className={`flex items-center justify-between px-4 py-2 rounded-lg font-semibold text-sm transition-all duration-fast ${
            isActive('/connections')
              ? 'bg-primary-100 dark:bg-primary-900/30 text-primary-700 dark:text-primary-300'
              : 'text-neutral-700 dark:text-neutral-300 hover:bg-neutral-100 dark:hover:bg-neutral-700/50'
          }`}
        >
          <div className="flex items-center gap-3">
            <span>⚡ Connections</span>
          </div>
          {(connections || []).length > 0 && (
            <span className="px-2 py-0.5 bg-neutral-200 dark:bg-neutral-600 text-xs font-bold text-neutral-700 dark:text-neutral-300 rounded-full">
              {(connections || []).length}
            </span>
          )}
        </Link>

        {/* Status Cards */}
        {(connections || []).length > 0 && (
          <div className="mt-6 pt-4 border-t border-neutral-200 dark:border-neutral-700 space-y-2">
            {/* Running */}
            {runningCount > 0 && (
              <div className="px-4 py-3 bg-success-50 dark:bg-success-900/20 border border-success-200 dark:border-success-700/50 rounded-lg">
                <div className="flex items-center gap-2 mb-1">
                  <div className="w-2 h-2 bg-success-500 rounded-full animate-pulse-gentle"></div>
                  <span className="text-xs font-semibold text-success-700 dark:text-success-300">Running</span>
                </div>
                <span className="text-lg font-bold text-success-700 dark:text-success-300 block">{runningCount}</span>
              </div>
            )}

            {/* Stopped */}
            {stoppedCount > 0 && (
              <div className="px-4 py-3 bg-warning-50 dark:bg-warning-900/20 border border-warning-200 dark:border-warning-700/50 rounded-lg">
                <span className="text-xs font-semibold text-warning-700 dark:text-warning-300 block">Stopped</span>
                <span className="text-lg font-bold text-warning-700 dark:text-warning-300 block">{stoppedCount}</span>
              </div>
            )}

            {/* Errors */}
            {errorCount > 0 && (
              <div className="px-4 py-3 bg-danger-50 dark:bg-danger-900/20 border border-danger-200 dark:border-danger-700/50 rounded-lg">
                <span className="text-xs font-semibold text-danger-700 dark:text-danger-300 block">Errors</span>
                <span className="text-lg font-bold text-danger-700 dark:text-danger-300 block">{errorCount}</span>
              </div>
            )}
          </div>
        )}
      </nav>

      {/* Footer: Create Button */}
      <div className="p-4 border-t border-neutral-200 dark:border-neutral-700">
        <Link
          to="/connections/create"
          className="btn-primary w-full inline-flex items-center justify-center gap-2 h-10"
        >
          <span className="text-sm">➕ New Connection</span>
        </Link>
      </div>
    </aside>
  )
}
