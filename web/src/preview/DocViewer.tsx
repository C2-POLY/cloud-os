import { ChevronLeft, ChevronRight, Download, X } from 'lucide-react'
import type { PDFDocumentProxy } from 'pdfjs-dist'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { AnalysisSteps } from '../analysis/AnalysisSteps.tsx'
import { messageOf } from '../api.ts'
import { downloadURL, formatSize, type Entry } from '../drive/files.ts'
import { enter } from '../motion/animate.ts'
import { PdfPage } from './PdfPage.tsx'
import { openPdf, previewKind, textHead } from './preview.ts'

/** How much of a text the viewer reads. */
export const TEXT_BYTES = 64 * 1024

/**
 * A document full screen: a PDF page by page, an image, or the start of a
 * text as plain text. Escape, the cross or the backdrop close it; the arrows
 * turn the pages of a PDF.
 */
export function DocViewer({ entry, onClose }: { entry: Entry; onClose: () => void }) {
  const kind = previewKind(entry)
  const panel = useRef<HTMLDivElement>(null)
  const stage = useRef<HTMLDivElement>(null)
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null)
  const [page, setPage] = useState(1)
  const [text, setText] = useState<{ text: string; more: boolean } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [width, setWidth] = useState(800)

  useLayoutEffect(() => {
    enter(panel.current, { scale: 0.98 })
    const el = stage.current
    // The stage's padding (2 × 16 px) and room for its vertical scrollbar.
    if (el) setWidth(Math.max(240, Math.min(1000, el.clientWidth - 32 - 16)))
  }, [])

  useEffect(() => {
    let current = true
    let opened: PDFDocumentProxy | null = null
    if (kind === 'pdf')
      openPdf(entry.path)
        .then((d) => {
          opened = d
          if (current) setDoc(d)
          else void d.loadingTask.destroy()
        })
        .catch((err: unknown) => current && setError(messageOf(err)))
    if (kind === 'text')
      textHead(entry.path, TEXT_BYTES)
        .then((t) => current && setText(t))
        .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
      void opened?.loadingTask.destroy()
    }
  }, [entry.path, kind])

  const pages = doc?.numPages ?? 1
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
      else if (e.key === 'ArrowLeft' && page > 1) setPage(page - 1)
      else if (e.key === 'ArrowRight' && page < pages) setPage(page + 1)
      else return
      e.preventDefault()
      e.stopPropagation() // a viewer opened from Demander closes alone, not Demander with it
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [onClose, page, pages])

  return createPortal(
    <div className="doc-viewer" role="dialog" aria-modal="true" aria-label={entry.name} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={panel} className="doc-panel">
        <header className="doc-head">
          <h2>{entry.name}</h2>
          <span className="muted">{formatSize(entry.size)}</span>
          {doc && (
            <span className="doc-pages">
              <button type="button" className="icon-button" aria-label="Page précédente" disabled={page === 1} onClick={() => setPage(page - 1)}>
                <ChevronLeft size={18} aria-hidden />
              </button>
              <span aria-live="polite">
                Page {page} / {pages}
              </span>
              <button type="button" className="icon-button" aria-label="Page suivante" disabled={page === pages} onClick={() => setPage(page + 1)}>
                <ChevronRight size={18} aria-hidden />
              </button>
            </span>
          )}
          <a className="icon-button" href={downloadURL(entry.path)} download={entry.name} aria-label={`Télécharger ${entry.name}`} title="Télécharger">
            <Download size={18} aria-hidden />
          </a>
          <button type="button" className="icon-button" aria-label="Fermer" onClick={onClose}>
            <X size={18} aria-hidden />
          </button>
        </header>
        <AnalysisSteps path={entry.path} />
        <div ref={stage} className="doc-stage" data-lenis-prevent>
          {error && <p className="bad">{error}</p>}
          {!error && kind === 'pdf' && doc && <PdfPage doc={doc} page={page} width={width} label={`Page ${page} de ${entry.name}`} />}
          {!error && kind === 'image' && <img src={downloadURL(entry.path)} alt={entry.name} />}
          {!error && kind === 'text' && text && (
            <>
              <pre className="doc-text">{text.text}</pre>
              {text.more && <p className="muted">La suite du fichier n'est pas affichée : téléchargez-le pour tout lire.</p>}
            </>
          )}
          {!error && kind === null && <p className="muted">Pas d'aperçu pour ce type de fichier.</p>}
        </div>
      </div>
    </div>,
    document.body,
  )
}
