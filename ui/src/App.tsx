import { BrowserRouter as Router, Routes, Route } from 'react-router-dom'
import { useEffect } from 'react'
import { validateConfig } from './config/env'
import ErrorBoundary from './components/Common/ErrorBoundary'
import Toast from './components/Common/Toast'
import ConfirmDialog from './components/Common/ConfirmDialog'
import RootLayout from './components/Layout/RootLayout'
import ProtectedRoute from './components/Auth/ProtectedRoute'
import { useUIStore } from './store/uiStore'
import { useAuthStore } from './store/authStore'
import PipelineBuilderPage from './pages/PipelineBuilderPage'
import ConnectionDetail from './pages/ConnectionDetail'
import ConnectionsList from './pages/ConnectionsList'
import TestDataPage from './pages/TestDataPage'
import LoginPage from './pages/LoginPage'
import RegisterPage from './pages/RegisterPage'
import OAuthConnected from './pages/OAuthConnected'
import NotFound from './pages/NotFound'
import ConnectionRequestsPage from './pages/ConnectionRequestsPage'
import TenantConnectionsPage from './pages/TenantConnectionsPage'
import ApiKeyPage from './pages/ApiKeyPage'
import AuditPage from './pages/AuditPage'
import UsersPage from './pages/UsersPage'
import UsagePage from './pages/UsagePage'

function App() {
  const { confirmDialog, hideConfirmDialog } = useUIStore()
  const { checkAuth } = useAuthStore()

  useEffect(() => {
    try {
      validateConfig()
    } catch (error) {
      console.error('Configuration validation failed:', error)
    }
    // Check authentication status on app load
    checkAuth()
  }, [checkAuth])

  return (
    <ErrorBoundary>
      <Router>
        <Routes>
          {/* Public auth routes */}
          <Route path="/login" element={<LoginPage />} />
          <Route path="/register" element={<RegisterPage />} />
          {/* OAuth popup landing — posts the grant id back to the opener */}
          <Route path="/oauth/connected" element={<OAuthConnected />} />
          
          {/* Protected routes - PipelineBuilder is the main application */}
          <Route path="/" element={
            <ProtectedRoute>
              <PipelineBuilderPage />
            </ProtectedRoute>
          } />
          <Route path="/connections/create" element={
            <ProtectedRoute>
              <PipelineBuilderPage />
            </ProtectedRoute>
          } />
          
          {/* Other protected routes with layout */}
          <Route element={<RootLayout />}>
            <Route path="/connections" element={
              <ProtectedRoute>
                <ConnectionsList />
              </ProtectedRoute>
            } />
            <Route path="/connections/:id" element={
              <ProtectedRoute>
                <ConnectionDetail />
              </ProtectedRoute>
            } />
            <Route path="/connections/:id/test-data" element={
              <ProtectedRoute>
                <TestDataPage />
              </ProtectedRoute>
            } />
            <Route path="/settings/connection-requests" element={<ProtectedRoute><ConnectionRequestsPage /></ProtectedRoute>} />
            <Route path="/settings/tenant-connections" element={<ProtectedRoute><TenantConnectionsPage /></ProtectedRoute>} />
            <Route path="/settings/api-key" element={<ProtectedRoute><ApiKeyPage /></ProtectedRoute>} />
            <Route path="/settings/audit" element={<ProtectedRoute><AuditPage /></ProtectedRoute>} />
            <Route path="/settings/users" element={<ProtectedRoute><UsersPage /></ProtectedRoute>} />
            <Route path="/settings/usage" element={<ProtectedRoute><UsagePage /></ProtectedRoute>} />
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
        <Toast />
        <ConfirmDialog config={confirmDialog} onClose={hideConfirmDialog} />
      </Router>
    </ErrorBoundary>
  )
}

export default App
