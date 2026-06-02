import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'

// Dark mode is opt-in only. The app's dark theme is incomplete — many pages
// (settings, the pipeline detail panels, etc.) have no dark styles — so
// auto-enabling it from the OS `prefers-color-scheme: dark` produced a broken,
// half-dark UI with unreadable text. Until the dark theme is finished, only an
// explicit saved theme of 'dark' enables it; OS dark mode no longer does.
if (typeof window !== 'undefined') {
  const isDark = localStorage.getItem('theme') === 'dark'
  if (isDark) {
    document.documentElement.classList.add('dark')
  } else {
    document.documentElement.classList.remove('dark')
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
