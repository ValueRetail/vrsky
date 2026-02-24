/**
 * Root Layout
 * Main application shell with header, sidebar, and content area
 */

import { Outlet } from 'react-router-dom'
import { useUIStore } from '../../store/uiStore'
import Header from './Header'
import Sidebar from './Sidebar'
import Footer from './Footer'

export default function RootLayout() {
  const { sidebarOpen } = useUIStore()

  return (
    <div className="flex flex-col min-h-screen bg-gray-50">
      <Header />
      <div className="flex flex-1 overflow-hidden">
        {sidebarOpen && <Sidebar />}
        <main className="flex-1 overflow-auto">
          <div className="container mx-auto p-6">
            <Outlet />
          </div>
        </main>
      </div>
      <Footer />
    </div>
  )
}
