import { ReactLenis } from 'lenis/react'
import 'lenis/dist/lenis.css'
import type { ReactNode } from 'react'
import { useReducedMotion } from './reducedMotion.ts'

/**
 * Smooth page scrolling with Lenis, mounted once at the root. With reduced
 * motion the page keeps the browser's own scrolling. Elements that scroll on
 * their own (a dialog, the upload panel) carry data-lenis-prevent.
 */
export function SmoothScroll({ children }: { children: ReactNode }) {
  const reduced = useReducedMotion()
  if (reduced) return <>{children}</>
  return (
    <ReactLenis root options={{ autoRaf: true, lerp: 0.12 }}>
      {children}
    </ReactLenis>
  )
}
