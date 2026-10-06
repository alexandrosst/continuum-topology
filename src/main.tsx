import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@fontsource-variable/inter/wght.css' // self-hosted: no request to a font CDN, so the server's strict CSP holds
import './index.css'
import App from './App.tsx'
import { initTheme, watchSystemTheme } from '@/lib/theme'

initTheme() // normally a no-op: public/theme-init.js already applied it before first paint
watchSystemTheme() // keeps the theme in sync with OS-level light/dark changes while the preference is 'system'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
