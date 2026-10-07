import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@fontsource-variable/inter/wght.css' // self-hosted: no request to a font CDN, so the server's strict CSP holds
import './index.css'
import App from './App.tsx'
import { reloadForNewVersion } from '@/lib/staleBuild'
import { initTheme, watchSystemTheme } from '@/lib/theme'

initTheme() // normally a no-op: public/theme-init.js already applied it before first paint
watchSystemTheme() // keeps the theme in sync with OS-level light/dark changes while the preference is 'system'

// Vite says so when a page's files cannot be fetched (a new deploy removed them): load the new version instead of failing, once - unless a
// dialog is showing a secret that is shown once, when the error is left to the nearest ErrorBoundary, which says so and offers "Reload now".
window.addEventListener('vite:preloadError', (e) => {
  if (reloadForNewVersion()) e.preventDefault()
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
