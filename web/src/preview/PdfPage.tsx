import type { PDFDocumentProxy } from 'pdfjs-dist'
import { useEffect, useRef } from 'react'

/** One page of a PDF drawn in a canvas, `width` CSS pixels wide, sharp on high-density screens. */
export function PdfPage({ doc, page, width, label }: { doc: PDFDocumentProxy; page: number; width: number; label: string }) {
  const canvas = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    let cancelled = false
    let task: { cancel: () => void } | null = null
    void doc.getPage(page).then((p) => {
      const el = canvas.current
      if (cancelled || !el) return
      const ratio = window.devicePixelRatio || 1
      const base = p.getViewport({ scale: 1 })
      const viewport = p.getViewport({ scale: (width / base.width) * ratio })
      el.width = Math.floor(viewport.width)
      el.height = Math.floor(viewport.height)
      el.style.width = `${width}px`
      el.style.height = `${Math.floor(viewport.height / ratio)}px`
      const render = p.render({ canvas: el, viewport })
      task = render
      render.promise.catch(() => {}) // cancelled by a page change
    })
    return () => {
      cancelled = true
      task?.cancel()
    }
  }, [doc, page, width])

  return <canvas ref={canvas} className="pdf-page" role="img" aria-label={label} />
}
