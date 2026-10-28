/**
 * Root Layout
 * Main application shell with header, sidebar, and content area
 */

import { useEffect } from 'react'
import { Outlet } from 'react-router-dom'
import { useUIStore } from '../../store/uiStore'
import { useAuthStore } from '../../store/authStore'
import { useConnectionsStore } from '../../store/connectionsStore'
import Header from './Header'
import Sidebar from './Sidebar'
import Footer from './Footer'
import PlanBanner from './PlanBanner'

export default function RootLayout() {
  const { sidebarOpen } = useUIStore()
  const currentTenantId = useAuthStore((s) => s.currentTenant?.id)
  const loadConnections = useConnectionsStore((s) => s.load)
  const clearConnections = useConnectionsStore((s) => s.clear)

  // The header badge and the sidebar counts read the connections store. Fill
  // it for the current workspace on every page under the layout; before #450
  // only the unrouted Dashboard did, so the badge always said 0.
  // The previous workspace's (or user's) counts are dropped first, never
  // shown while the new ones load.
  useEffect(() => {
    clearConnections()
    if (currentTenantId) void loadConnections()
  }, [currentTenantId, loadConnections, clearConnections])

  return (
    <div className="flex flex-col min-h-screen bg-neutral-50 dark:bg-neutral-900 transition-colors duration-base">
      <Header />
      <PlanBanner />
      <div className="flex flex-1 overflow-hidden">
        <div
          className={`transition-all duration-300 ease-smooth-in-out ${
            sidebarOpen ? 'w-64' : 'w-0'
          } overflow-hidden`}
        >
          <Sidebar />
        </div>
        <main className="flex-1 overflow-auto">
          <div className="container mx-auto px-6 py-8 max-w-7xl">
            <Outlet />
          </div>
        </main>
      </div>
      <Footer />
    </div>
  )
}
