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
    <div className="flex flex-col min-h-screen bg-neutral-50 dark:bg-neutral-900 transition-colors duration-base">
      <Header />
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
