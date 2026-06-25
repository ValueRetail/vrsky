import { useEffect, useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useUIStore } from '../../store/uiStore'
import { useConnectionsStore } from '../../store/connectionsStore'
import { useAuthStore } from '../../store/authStore'
import * as authService from '../../services/authService'
import TenantSelector from '../Tenants/TenantSelector'

export default function Header() {
  const navigate = useNavigate()
  const { toggleSidebar, showConfirmDialog, hideConfirmDialog } = useUIStore()
  const { connections = [] } = useConnectionsStore()
  const { user, isAuthenticated, logout } = useAuthStore()
  const [showUserMenu, setShowUserMenu] = useState(false)
  const userMenuRef = useRef<HTMLDivElement>(null)

  // Close user menu when clicking outside
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (userMenuRef.current && !userMenuRef.current.contains(event.target as globalThis.Node)) {
        setShowUserMenu(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  const handleLogout = async () => {
    setShowUserMenu(false)
    await logout()
    navigate('/login')
  }

  return (
    <header className="bg-white dark:bg-neutral-800 border-b border-neutral-200 dark:border-neutral-700 shadow-xs sticky top-0 z-40 transition-colors duration-base">
      <div className="max-w-7xl mx-auto px-4 py-3">
        <div className="flex items-center justify-between">
          {/* Left: Logo and Menu Toggle */}
          <div className="flex items-center gap-3">
            <button
              onClick={() => toggleSidebar()}
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
              }}
              title="Toggle sidebar"
              aria-label="Toggle navigation sidebar"
            >
              ☰
            </button>

            <div className="flex items-center gap-3">
              <div className="flex flex-col">
                <h1 className="text-lg font-bold text-neutral-900 dark:text-neutral-50">VRSky</h1>
                <p className="text-xs text-neutral-500 dark:text-neutral-400">Integration Platform</p>
              </div>
            </div>
          </div>

          {/* Center: Tenant Selector */}
          {isAuthenticated && <TenantSelector />}

          {/* Right: Connections Count + User Menu */}
          <div className="flex items-center gap-3">
            {/* Connections Counter */}
            <div className="hidden sm:flex items-center gap-2 px-3 py-2 bg-neutral-100 dark:bg-neutral-700 rounded-lg">
              <div className="w-2 h-2 bg-primary-500 rounded-full"></div>
              <span className="text-sm font-semibold text-neutral-900 dark:text-neutral-50">
                {(connections || []).length}
              </span>
              <span className="text-xs text-neutral-600 dark:text-neutral-400">connections</span>
            </div>

            {/* User Menu */}
            <div className="relative" ref={userMenuRef}>
              <button
                onClick={() => setShowUserMenu(!showUserMenu)}
                className="flex items-center gap-2 px-3 py-2 border border-neutral-200 dark:border-neutral-600 hover:bg-neutral-100 dark:hover:bg-neutral-700 rounded-lg transition-colors"
                title={isAuthenticated ? `Logged in as ${user?.email}` : 'Menu'}
                aria-label="Open user menu"
                aria-expanded={showUserMenu}
              >
                <span style={{ fontSize: '18px' }}>⚙️</span>
                {isAuthenticated && user && (
                  <span className="hidden sm:inline text-sm text-neutral-700 dark:text-neutral-300 max-w-28 truncate">
                    {user.full_name || user.email}
                  </span>
                )}
              </button>

              {/* Dropdown Menu */}
              {showUserMenu && (
                <div className="absolute right-0 mt-2 w-56 bg-white dark:bg-neutral-800 border border-neutral-200 dark:border-neutral-700 rounded-lg shadow-lg z-50">
                  {isAuthenticated && user ? (
                    <>
                      {/* User Info */}
                      <div className="px-4 py-3 border-b border-neutral-200 dark:border-neutral-700">
                        <p className="text-sm font-medium text-neutral-900 dark:text-neutral-100 truncate">
                          {user.full_name || 'User'}
                        </p>
                        <p className="text-xs text-neutral-500 dark:text-neutral-400 truncate">
                          {user.email}
                        </p>
                      </div>
                      {/* Account actions only — page navigation lives in the
                          sidebar, so the gear menu stays focused on the account. */}
                      <div className="p-2">
                        {/* Delete Account */}
                        <button
                          onClick={() => {
                            setShowUserMenu(false)
                            showConfirmDialog({
                              title: 'Delete Account',
                              message: 'This will permanently delete your account and all associated data. This action cannot be undone.',
                              confirmLabel: 'Delete Account',
                              destructive: true,
                              onConfirm: async () => {
                                hideConfirmDialog()
                                try {
                                  await authService.deleteAccount()
                                  await logout()
                                  navigate('/login')
                                } catch {
                                  // Token already cleared by deleteAccount on success
                                }
                              },
                            })
                          }}
                          className="w-full flex items-center gap-2 px-3 py-2 text-sm text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 rounded-md transition-colors"
                        >
                          <span>Delete Account</span>
                        </button>
                        {/* Logout */}
                        <button
                          onClick={handleLogout}
                          className="w-full flex items-center gap-2 px-3 py-2 text-sm text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 rounded-md transition-colors"
                        >
                          <span>🚪</span>
                          <span>Logout</span>
                        </button>
                      </div>
                    </>
                  ) : (
                    <div className="p-2">
                      {/* Login */}
                      <button
                        onClick={() => {
                          setShowUserMenu(false)
                          navigate('/login')
                        }}
                        className="w-full flex items-center gap-2 px-3 py-2 text-sm text-neutral-700 dark:text-neutral-300 hover:bg-neutral-100 dark:hover:bg-neutral-700 rounded-md transition-colors"
                      >
                        <span>🔑</span>
                        <span>Login</span>
                      </button>
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
        </div>
      </div>
    </header>
  )
}
