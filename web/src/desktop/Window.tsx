import { Maximize2, Minimize2, X } from 'lucide-react'
import { useLayoutEffect, useRef, useState, type CSSProperties, type PointerEvent, type ReactNode } from 'react'
import { enter, leave } from '../motion/animate.ts'
import type { AppInfo } from './apps.ts'
import { clampPosition, type Geometry } from './windows.ts'

/**
 * A glass window of the desktop. It moves by its title bar, grows to the whole
 * desktop on a double click (or its button), and comes forward on any click.
 * On a phone it fills the screen and only the front one shows (CSS).
 */
export function Window({
  app,
  front,
  z,
  geometry,
  onGeometry,
  onFocus,
  onClose,
  children,
}: {
  app: AppInfo
  front: boolean
  z: number
  geometry: Geometry
  onGeometry: (g: Geometry) => void
  onFocus: () => void
  onClose: () => void
  children: ReactNode
}) {
  const el = useRef<HTMLElement>(null)
  // While dragging, the place lives here; it is saved once the pointer is up.
  const [drag, setDrag] = useState<{ x: number; y: number } | null>(null)
  const start = useRef<{ px: number; py: number; x: number; y: number } | null>(null)

  useLayoutEffect(() => {
    enter(el.current, { scale: 0.97, y: 8 })
  }, [])

  const x = drag?.x ?? geometry.x
  const y = drag?.y ?? geometry.y

  function down(e: PointerEvent<HTMLElement>) {
    if (geometry.max || e.button !== 0 || (e.target as HTMLElement).closest('button')) return
    start.current = { px: e.clientX, py: e.clientY, x, y }
    e.currentTarget.setPointerCapture(e.pointerId)
  }

  function move(e: PointerEvent<HTMLElement>) {
    const s = start.current
    if (!s) return
    setDrag(clampPosition(s.x + e.clientX - s.px, s.y + e.clientY - s.py, geometry.w))
  }

  function up() {
    if (start.current && drag) onGeometry({ ...geometry, ...drag })
    start.current = null
    setDrag(null)
  }

  function toggleMax() {
    onGeometry({ ...geometry, max: !geometry.max })
  }

  const Icon = app.icon
  const style = {
    '--x': `${x}px`,
    '--y': `${y}px`,
    '--w': `${geometry.w}px`,
    '--h': `${geometry.h}px`,
    zIndex: z,
  } as CSSProperties

  return (
    <section
      ref={el}
      className={['window', front && 'front', geometry.max && 'max', drag && 'dragging'].filter(Boolean).join(' ')}
      style={style}
      aria-label={app.title}
      onPointerDownCapture={front ? undefined : onFocus}
    >
      <header
        className="window-bar"
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={up}
        onDoubleClick={(e) => {
          if (!(e.target as HTMLElement).closest('button')) toggleMax()
        }}
      >
        <button
          type="button"
          className="window-button"
          aria-label={`Fermer ${app.title}`}
          title="Fermer"
          onClick={() => void leave(el.current, { scale: 0.97 }).then(onClose)}
        >
          <X size={14} aria-hidden />
        </button>
        <span className="window-title">
          <Icon size={15} aria-hidden /> {app.title}
        </span>
        <button
          type="button"
          className="window-button window-max"
          aria-label={geometry.max ? `Réduire ${app.title}` : `Agrandir ${app.title}`}
          title={geometry.max ? 'Réduire' : 'Agrandir'}
          onClick={toggleMax}
        >
          {geometry.max ? <Minimize2 size={14} aria-hidden /> : <Maximize2 size={14} aria-hidden />}
        </button>
      </header>
      <div className="window-body" data-lenis-prevent>
        {children}
      </div>
    </section>
  )
}
