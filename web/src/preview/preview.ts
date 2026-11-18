import type { PDFDocumentProxy } from 'pdfjs-dist'
import { downloadURL, type Entry } from '../drive/files.ts'

/** What the viewer can show of a file. */
export type PreviewKind = 'pdf' | 'image' | 'text'

// Images every browser draws (HEIC is not one: it keeps its thumbnail).
const drawable = new Set(['image/jpeg', 'image/png', 'image/webp', 'image/gif', 'image/avif'])
const textTypes = new Set(['application/json', 'application/xml', 'application/x-yaml', 'application/yaml', 'application/javascript', 'application/x-sh'])
const textExtensions = /\.(txt|md|markdown|csv|tsv|log|json|ya?ml|xml|ini|conf|toml|sh|py|go|js|ts|tsx|css|html?|sql)$/i

export function previewKind(entry: Entry): PreviewKind | null {
  if (entry.kind !== 'file') return null
  if (entry.mime === 'application/pdf') return 'pdf'
  if (drawable.has(entry.mime)) return 'image'
  if (entry.mime.startsWith('text/') || textTypes.has(entry.mime) || textExtensions.test(entry.name)) return 'text'
  return null
}

/** The first bytes of a text file, read by an HTTP range, and whether there is more. */
export async function textHead(path: string, bytes: number): Promise<{ text: string; more: boolean }> {
  const res = await fetch(downloadURL(path), { credentials: 'same-origin', headers: { Range: `bytes=0-${bytes - 1}` } })
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  const buf = new Uint8Array(await res.arrayBuffer())
  const total = Number(res.headers.get('Content-Range')?.split('/')[1] ?? buf.length)
  // A cut in the middle of a UTF-8 character becomes a replacement mark, never an error.
  return { text: new TextDecoder('utf-8', { fatal: false }).decode(buf), more: total > buf.length }
}

let configured = false

/**
 * Opens a PDF of the drive with pdf.js, loaded only now. pdf.js reads the
 * file by ranges from the box; it never runs a PDF's own scripts (its display
 * layer has none) and XFA forms stay off.
 */
export async function openPdf(path: string): Promise<PDFDocumentProxy> {
  const [pdfjs, worker] = await Promise.all([import('pdfjs-dist'), import('pdfjs-dist/build/pdf.worker.min.mjs?url')])
  if (!configured) {
    pdfjs.GlobalWorkerOptions.workerSrc = worker.default
    configured = true
  }
  return pdfjs.getDocument({ url: downloadURL(path), withCredentials: true, enableXfa: false, disableAutoFetch: true }).promise
}
