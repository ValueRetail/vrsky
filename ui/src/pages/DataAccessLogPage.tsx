import { useState, useEffect } from 'react'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import * as tenantDataService from '@/services/tenantDataService'
import type { DataAccessLogEntry, PageInfo } from '@/types/models'

export default function DataAccessLogPage() {
  const { currentTenant } = useAuthStore()
  const { addNotification } = useUIStore()
  const [entries, setEntries] = useState<DataAccessLogEntry[]>([])
  const [pageInfo, setPageInfo] = useState<PageInfo | null>(null)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!currentTenant) { setLoading(false); return }
    setLoading(true)
    tenantDataService.getDataAccessLog(currentTenant.id, page)
      .then(data => { setEntries(data.entries); setPageInfo(data.page_info) })
      .catch(() => addNotification({ id: Date.now().toString(), type: 'error', title: 'Error', message: 'Failed to load audit log' }))
      .finally(() => setLoading(false))
  }, [currentTenant, page, addNotification])

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <h1 className="text-2xl font-bold text-neutral-900 dark:text-neutral-100 mb-6">Data Access Log</h1>

      {loading ? (
        <p className="text-neutral-500 dark:text-neutral-400">Loading...</p>
      ) : entries.length === 0 ? (
        <p className="text-neutral-500 dark:text-neutral-400">No data access events recorded yet</p>
      ) : (
        <>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-neutral-200 dark:border-neutral-700">
                  <th className="text-left py-3 px-2 text-neutral-500 dark:text-neutral-400 font-medium">Time</th>
                  <th className="text-left py-3 px-2 text-neutral-500 dark:text-neutral-400 font-medium">Requester</th>
                  <th className="text-left py-3 px-2 text-neutral-500 dark:text-neutral-400 font-medium">Fields</th>
                  <th className="text-right py-3 px-2 text-neutral-500 dark:text-neutral-400 font-medium">Bytes</th>
                  <th className="text-right py-3 px-2 text-neutral-500 dark:text-neutral-400 font-medium">Status</th>
                  <th className="text-left py-3 px-2 text-neutral-500 dark:text-neutral-400 font-medium">IP</th>
                </tr>
              </thead>
              <tbody>
                {entries.map(entry => (
                  <tr key={entry.id} className="border-b border-neutral-100 dark:border-neutral-800">
                    <td className="py-2 px-2 text-neutral-900 dark:text-neutral-100 whitespace-nowrap">
                      {new Date(entry.request_time).toLocaleString()}
                    </td>
                    <td className="py-2 px-2 text-neutral-600 dark:text-neutral-400 font-mono text-xs">
                      {entry.requester_tenant_id.slice(0, 8)}...
                    </td>
                    <td className="py-2 px-2 text-neutral-600 dark:text-neutral-400">
                      {entry.fields_accessed ? entry.fields_accessed.join(', ') : '-'}
                    </td>
                    <td className="py-2 px-2 text-neutral-600 dark:text-neutral-400 text-right">
                      {entry.bytes_received.toLocaleString()}
                    </td>
                    <td className="py-2 px-2 text-right">
                      <span className={`font-medium ${entry.status_code < 300 ? 'text-green-600' : entry.status_code < 500 ? 'text-yellow-600' : 'text-red-600'}`}>
                        {entry.status_code}
                      </span>
                    </td>
                    <td className="py-2 px-2 text-neutral-500 dark:text-neutral-500 font-mono text-xs">
                      {entry.ip_address || '-'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {pageInfo && pageInfo.total_pages > 1 && (
            <div className="flex items-center justify-between mt-4">
              <p className="text-sm text-neutral-500 dark:text-neutral-400">
                Page {pageInfo.page} of {pageInfo.total_pages} ({pageInfo.total} entries)
              </p>
              <div className="flex gap-2">
                <button
                  onClick={() => setPage(p => Math.max(1, p - 1))}
                  disabled={page <= 1}
                  className="px-3 py-1 text-sm border border-neutral-300 dark:border-neutral-600 rounded disabled:opacity-50 hover:bg-neutral-50 dark:hover:bg-neutral-700 transition-colors"
                >
                  Previous
                </button>
                <button
                  onClick={() => setPage(p => p + 1)}
                  disabled={page >= pageInfo.total_pages}
                  className="px-3 py-1 text-sm border border-neutral-300 dark:border-neutral-600 rounded disabled:opacity-50 hover:bg-neutral-50 dark:hover:bg-neutral-700 transition-colors"
                >
                  Next
                </button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
