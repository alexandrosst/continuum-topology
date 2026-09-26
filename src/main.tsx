import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@fontsource-variable/inter/wght.css' // self-hosted: no request to a font CDN, so the server's strict CSP holds
import './index.css'
import App from './App.tsx'
import { watchSystemTheme } from '@/lib/theme'

watchSystemTheme() // keeps the theme in sync with OS-level light/dark changes while the preference is 'system'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
