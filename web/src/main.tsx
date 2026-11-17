import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { Health } from './Health.tsx'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <main>
      <h1>Cloud OS</h1>
      <Health />
    </main>
  </StrictMode>,
)
