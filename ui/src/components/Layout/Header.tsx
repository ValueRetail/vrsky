import { useEffect, useState } from 'react'
import { useUIStore } from '../../store/uiStore'
import { useConnectionsStore } from '../../store/connectionsStore'

export default function Header() {
  const { toggleSidebar } = useUIStore()
  const { connections = [] } = useConnectionsStore()
  const runningCount = (connections || []).filter((c) => c.status === 'running').length
  
  const [isDark, setIsDark] = useState(() => {
    if (typeof window === 'undefined') return false
    return document.documentElement.classList.contains('dark')
  })

  useEffect(() => {
    const observer = new MutationObserver(() => {
      setIsDark(document.documentElement.classList.contains('dark'))
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => observer.disconnect()
  }, [])

  const toggleDarkMode = () => {
    try {
      const html = document.documentElement
      console.log('Current dark class:', html.classList.contains('dark'))
      if (html.classList.contains('dark')) {
        html.classList.remove('dark')
        try {
          localStorage.setItem('theme', 'light')
        } catch (e) {
          console.warn('localStorage not available:', e)
        }
        console.log('Removed dark class')
        setIsDark(false)
      } else {
        html.classList.add('dark')
        try {
          localStorage.setItem('theme', 'dark')
        } catch (e) {
          console.warn('localStorage not available:', e)
        }
        console.log('Added dark class')
        setIsDark(true)
      }
      console.log('Dark class after toggle:', html.classList.contains('dark'))
    } catch (e) {
      console.error('Error toggling dark mode:', e)
    }
  }

  return (
    <header className="bg-white dark:bg-neutral-800 border-b border-neutral-200 dark:border-neutral-700 shadow-xs sticky top-0 z-40 transition-colors duration-base">
      <div className="max-w-7xl mx-auto px-6 py-4">
        <div className="flex items-center justify-between">
          {/* Left: Logo and Menu Toggle */}
          <div className="flex items-center gap-4">
            <button
              onClick={() => {
                console.log('Menu button clicked, calling toggleSidebar')
                toggleSidebar()
                console.log('toggleSidebar called')
              }}
              style={{
                padding: '0.5rem',
                backgroundColor: 'transparent',
                border: 'none',
                cursor: 'pointer',
                fontSize: '1.25rem',
                borderRadius: '0.5rem',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                zIndex: 10,
                position: 'relative',
              }}
              title="Toggle sidebar"
              aria-label="Toggle navigation sidebar"
            >
              ☰
            </button>

            <div className="hidden sm:flex items-center gap-3">
              <div className="flex flex-col">
                <h1 className="text-xl font-bold text-neutral-900 dark:text-neutral-50">VRSky</h1>
                <p className="text-xs text-neutral-500 dark:text-neutral-400">Integration Platform</p>
              </div>
            </div>
          </div>

          {/* Right: Status and Info */}
          <div className="flex items-center gap-4 sm:gap-6">
            {/* Connections Counter */}
            <div className="hidden xs:flex items-center gap-2 px-3 py-2 bg-neutral-100 dark:bg-neutral-700 rounded-lg">
              <div className="w-2 h-2 bg-primary-500 rounded-full"></div>
              <span className="text-sm font-semibold text-neutral-900 dark:text-neutral-50">
                {(connections || []).length}
              </span>
              <span className="text-xs text-neutral-600 dark:text-neutral-400">connections</span>
            </div>

            {/* Status Indicator */}
            <div className="flex items-center gap-2 px-3 py-2 bg-success-50 dark:bg-success-900/20 border border-success-200 dark:border-success-700/50 rounded-full">
              <div className="w-2 h-2 bg-success-500 rounded-full animate-pulse-gentle"></div>
              <span className="text-xs font-semibold text-success-700 dark:text-success-300">
                {runningCount > 0 ? 'Live' : 'Idle'}
              </span>
            </div>

            {/* Dark Mode Toggle */}
            <button
              onClick={() => {
                console.log('Dark mode button clicked')
                toggleDarkMode()
              }}
              style={{
                padding: '0.5rem',
                backgroundColor: 'transparent',
                border: 'none',
                cursor: 'pointer',
                fontSize: '1.25rem',
                borderRadius: '0.5rem',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                zIndex: 10,
                position: 'relative',
              }}
              title={isDark ? 'Switch to light mode' : 'Switch to dark mode'}
              aria-label="Toggle dark mode"
            >
              {isDark ? '☀️' : '🌙'}
            </button>

            {/* User Menu (Placeholder for future) */}
            <button
              className="p-2 hover:bg-neutral-100 dark:hover:bg-neutral-700 rounded-lg transition-colors duration-fast"
              title="User menu"
              aria-label="Open user menu"
            >
              👤
            </button>
          </div>
        </div>
      </div>
    </header>
  )
}
