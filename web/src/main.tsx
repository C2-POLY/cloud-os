import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { App } from './App.tsx'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <main>
      <h1>Cloud OS</h1>
      <App />
    </main>
  </StrictMode>,
)
