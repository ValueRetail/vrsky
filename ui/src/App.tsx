import { BrowserRouter as Router, Routes, Route } from 'react-router-dom'
import { useEffect } from 'react'
import { validateConfig } from './config/env'
import ErrorBoundary from './components/Common/ErrorBoundary'
import Toast from './components/Common/Toast'
import ConfirmDialog from './components/Common/ConfirmDialog'
import RootLayout from './components/Layout/RootLayout'
import { useUIStore } from './store/uiStore'
import Dashboard from './pages/Dashboard'
import PipelineBuilder from './pages/PipelineBuilder'
import ConnectionDetail from './pages/ConnectionDetail'
import ConnectionsList from './pages/ConnectionsList'
import TestDataPage from './pages/TestDataPage'
import NotFound from './pages/NotFound'

function App() {
  const { confirmDialog, hideConfirmDialog } = useUIStore()

  useEffect(() => {
    try {
      validateConfig()
    } catch (error) {
      console.error('Configuration validation failed:', error)
    }
  }, [])

  return (
    <ErrorBoundary>
      <Router>
        <Routes>
          {/* Full-screen pipeline builder route */}
          <Route path="/connections/create" element={<PipelineBuilder />} />
          
          {/* All other routes with layout */}
          <Route element={<RootLayout />}>
            <Route path="/" element={<Dashboard />} />
            <Route path="/connections" element={<ConnectionsList />} />
            <Route path="/connections/:id" element={<ConnectionDetail />} />
            <Route path="/connections/:id/test-data" element={<TestDataPage />} />
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
