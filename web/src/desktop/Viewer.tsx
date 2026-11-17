import { ChevronLeft, ChevronRight, Download, FolderOpen, X } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { api } from '../api.ts'
import { downloadURL, formatSize, parentOf, thumbURL } from '../drive/files.ts'
import { enter } from '../motion/animate.ts'
import { driveURL, navigate } from '../router.ts'
import { formatWallClock, type MediaMeta, type TimelineEntry } from './timeline.ts'

// What browsers draw themselves; anything else (HEIC…) shows its thumbnail.
const drawable = new Set(['image/jpeg', 'image/png', 'image/webp', 'image/gif', 'image/avif'])

/**
 * A photo or video shown big over the desktop, with its metadata. The arrows
 * go to the newer and older ones, Escape closes; near the end of what is
 * loaded, it asks for more.
 */
export function Viewer({
  items,
  index,
  onIndex,
  onClose,
  onNearEnd,
}: {
  items: TimelineEntry[]
  index: number
  onIndex: (i: number) => void
  onClose: () => void
  onNearEnd: () => void
}) {
  const item = items[index]
  const panel = useRef<HTMLDivElement>(null)
  const [meta, setMeta] = useState<{ path: string; value: MediaMeta | null } | null>(null)
  const [broken, setBroken] = useState<string | null>(null)

  useLayoutEffect(() => {
    enter(panel.current, { scale: 0.98 })
  }, [])

  useEffect(() => {
    let current = true
    api<MediaMeta>('GET', `/api/files/meta?path=${encodeURIComponent(item.path)}`)
      .then((value) => current && setMeta({ path: item.path, value }))
      .catch(() => current && setMeta({ path: item.path, value: null }))
    if (index >= items.length - 3) onNearEnd()
    return () => {
      current = false
    }
  }, [item.path, index, items.length, onNearEnd])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
      else if (e.key === 'ArrowLeft' && index > 0) onIndex(index - 1)
      else if (e.key === 'ArrowRight' && index < items.length - 1) onIndex(index + 1)
      else return
      e.preventDefault()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [index, items.length, onIndex, onClose])

  const thumb = thumbURL(item)
  const video = item.mime.startsWith('video/')
  const original = drawable.has(item.mime) && broken !== item.path
  const m = meta?.path === item.path ? meta.value : null
  const camera = [m?.make, m?.model].filter(Boolean).join(' ')

  return createPortal(
    <div className="viewer" role="dialog" aria-modal="true" aria-label={item.name}>
      <div ref={panel} className="viewer-stage">
        {video ? (
          <video key={item.path} src={downloadURL(item.path)} controls autoPlay playsInline />
        ) : original ? (
          <img key={item.path} src={downloadURL(item.path)} alt={item.name} onError={() => setBroken(item.path)} />
        ) : thumb ? (
          <img key={item.path} src={thumb} alt={item.name} />
        ) : (
          <p className="muted">Aperçu indisponible</p>
        )}
        <button type="button" className="viewer-nav prev" aria-label="Plus récente" disabled={index === 0} onClick={() => onIndex(index - 1)}>
          <ChevronLeft size={28} aria-hidden />
        </button>
        <button type="button" className="viewer-nav next" aria-label="Plus ancienne" disabled={index === items.length - 1} onClick={() => onIndex(index + 1)}>
          <ChevronRight size={28} aria-hidden />
        </button>
      </div>
      <aside className="viewer-info" aria-label="Informations">
        <button type="button" className="viewer-close" aria-label="Fermer" onClick={onClose}>
          <X size={18} aria-hidden />
        </button>
        <h2>{item.name}</h2>
        <dl>
          <dt>{item.taken ? 'Prise le' : 'Modifiée le'}</dt>
          <dd>{formatWallClock(item.at)}</dd>
          {camera && (
            <>
              <dt>Appareil</dt>
              <dd>{camera}</dd>
            </>
          )}
          {m?.lens && (
            <>
              <dt>Objectif</dt>
              <dd>{m.lens}</dd>
            </>
          )}
          {m?.width && m?.height && (
            <>
              <dt>Dimensions</dt>
              <dd>
                {m.width} × {m.height}
              </dd>
            </>
          )}
          {m?.latitude != null && m?.longitude != null && (
            <>
              <dt>Lieu</dt>
              <dd>
                {m.latitude.toFixed(5)}, {m.longitude.toFixed(5)}
              </dd>
            </>
          )}
          <dt>Taille</dt>
          <dd>{formatSize(item.size)}</dd>
          <dt>Dossier</dt>
          <dd>{parentOf(item.path) || 'Mon drive'}</dd>
        </dl>
        <div className="viewer-actions">
          <a className="button secondary" href={downloadURL(item.path)} download={item.name}>
            <Download size={16} aria-hidden /> Télécharger
          </a>
          <button
            type="button"
            className="secondary"
            onClick={() => {
              onClose()
              navigate(driveURL(parentOf(item.path)))
            }}
          >
            <FolderOpen size={16} aria-hidden /> Afficher dans Fichiers
          </button>
        </div>
        <p className="muted viewer-count">
          {index + 1} / {items.length}
        </p>
      </aside>
    </div>,
    document.body,
  )
}
