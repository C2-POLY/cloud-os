import { Download, File, FileImage, FileText, FileVideo, Folder, FolderOpen, Search } from 'lucide-react'
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { messageOf } from '../api.ts'
import { downloadURL, formatDate, formatSize, parentOf, searchFiles, thumbURL, type Entry } from '../drive/files.ts'
import { enter } from '../motion/animate.ts'
import { driveURL, navigate } from '../router.ts'
import { folderOf, groupOf, groups } from './search.ts'
import type { PDFDocumentProxy } from 'pdfjs-dist'
import { DocViewer } from '../preview/DocViewer.tsx'
import { PdfPage } from '../preview/PdfPage.tsx'
import { openPdf, previewKind, textHead } from '../preview/preview.ts'

const debounceMs = 150

/**
 * The Demander palette: results while typing, grouped, picked with the arrows,
 * with a preview of the selected one. Enter shows it in Fichiers; Escape or a
 * click beside it closes. Natural-language answers come with the AI index.
 */
export function Palette({ onClose }: { onClose: () => void }) {
  const panel = useRef<HTMLDivElement>(null)
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Entry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState(0)
  const latest = useRef(0)

  useLayoutEffect(() => {
    enter(panel.current, { scale: 0.97, y: -6 })
  }, [])

  // Ask the core 150 ms after the last key; an older answer arriving late is dropped.
  useEffect(() => {
    const words = query.trim()
    const id = ++latest.current
    const timer = window.setTimeout(() => {
      if (!words) {
        setResults(null)
        setError(null)
        return
      }
      searchFiles(words)
        .then((found) => {
          if (id !== latest.current) return
          setResults(found)
          setError(null)
          setSelected(0)
        })
        .catch((err: unknown) => id === latest.current && setError(messageOf(err)))
    }, debounceMs)
    return () => window.clearTimeout(timer)
  }, [query])

  // Shown grouped, so the arrows follow the order on screen.
  const ordered = useMemo(
    () => (results ?? []).slice().sort((a, b) => groups.indexOf(groupOf(a)) - groups.indexOf(groupOf(b))),
    [results],
  )
  const current = ordered[selected]

  function open(e: Entry) {
    navigate(driveURL(folderOf(e)))
    onClose()
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    } else if (e.key === 'ArrowDown' && ordered.length) {
      e.preventDefault()
      setSelected((i) => (i + 1) % ordered.length)
    } else if (e.key === 'ArrowUp' && ordered.length) {
      e.preventDefault()
      setSelected((i) => (i - 1 + ordered.length) % ordered.length)
    } else if (e.key === 'Enter' && current) {
      e.preventDefault()
      open(current)
    }
  }

  return (
    <div className="palette-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={panel} className="palette" role="dialog" aria-modal="true" aria-label="Demander" data-lenis-prevent>
        <label className="palette-input">
          <Search size={22} aria-hidden />
          <span className="visually-hidden">Chercher un fichier ou un dossier</span>
          <input
            autoFocus
            value={query}
            placeholder="Cherche un fichier, un dossier…"
            role="combobox"
            aria-expanded={ordered.length > 0}
            aria-controls="palette-results"
            aria-activedescendant={current ? `palette-${selected}` : undefined}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKey}
          />
          <kbd>Échap</kbd>
        </label>
        <div className="palette-main">
          <div className="palette-list">
            {error && <p className="bad palette-note">{error}</p>}
            {!error && results === null && <p className="muted palette-note">Tape un nom, un mot du chemin : « ete », « bail appart ».</p>}
            {!error && results !== null && ordered.length === 0 && <p className="muted palette-note">Rien trouvé pour « {query.trim()} ».</p>}
            <ul id="palette-results" role="listbox" aria-label="Résultats">
              {ordered.map((e, i) => (
                <li key={e.path} role="presentation">
                  {(i === 0 || groupOf(ordered[i - 1]) !== groupOf(e)) && <div className="palette-group">{groupOf(e)}</div>}
                  <div
                    id={`palette-${i}`}
                    role="option"
                    aria-selected={i === selected}
                    className={i === selected ? 'palette-row on' : 'palette-row'}
                    onMouseMove={() => i !== selected && setSelected(i)}
                    onClick={() => open(e)}
                  >
                    <KindIcon entry={e} />
                    <span className="palette-name">
                      {e.name}
                      <small>{parentOf(e.path) || 'Mon drive'}</small>
                    </span>
                  </div>
                </li>
              ))}
            </ul>
          </div>
          {current && <Preview entry={current} onOpen={() => open(current)} />}
        </div>
      </div>
    </div>
  )
}

function KindIcon({ entry }: { entry: Entry }) {
  const props = { size: 18, 'aria-hidden': true }
  if (entry.kind === 'dir') return <Folder {...props} className="icon-folder" />
  if (entry.mime.startsWith('image/')) return <FileImage {...props} />
  if (entry.mime.startsWith('video/')) return <FileVideo {...props} />
  if (entry.mime.startsWith('text/') || entry.mime === 'application/pdf') return <FileText {...props} />
  return <File {...props} />
}

function Preview({ entry, onOpen }: { entry: Entry; onOpen: () => void }) {
  const thumb = thumbURL(entry)
  const kind = previewKind(entry)
  const [broken, setBroken] = useState<string | null>(null)
  const [viewing, setViewing] = useState(false)
  const visual =
    kind === 'pdf' ? (
      <PdfThumb entry={entry} />
    ) : kind === 'text' ? (
      <TextHead entry={entry} />
    ) : thumb && broken !== thumb ? (
      <img src={thumb} alt="" onError={() => setBroken(thumb)} />
    ) : (
      <KindIcon entry={entry} />
    )
  return (
    <aside className="palette-preview" aria-label={`Aperçu de ${entry.name}`}>
      {kind ? (
        <button type="button" className="palette-visual" onClick={() => setViewing(true)} aria-label={`Ouvrir l'aperçu de ${entry.name}`} title="Ouvrir l'aperçu">
          {visual}
        </button>
      ) : (
        <div className="palette-visual">{visual}</div>
      )}
      <h3>{entry.name}</h3>
      <dl>
        <dt>Dossier</dt>
        <dd>{parentOf(entry.path) || 'Mon drive'}</dd>
        {entry.kind === 'file' && (
          <>
            <dt>Taille</dt>
            <dd>{formatSize(entry.size)}</dd>
          </>
        )}
        <dt>Modifié</dt>
        <dd>{formatDate(entry.modified_at)}</dd>
      </dl>
      <div className="palette-actions">
        <button type="button" onClick={onOpen}>
          <FolderOpen size={16} aria-hidden /> Afficher dans Fichiers
        </button>
        {entry.kind === 'file' && (
          <a className="button secondary" href={downloadURL(entry.path)} download={entry.name}>
            <Download size={16} aria-hidden /> Télécharger
          </a>
        )}
      </div>
      {viewing && <DocViewer entry={entry} onClose={() => setViewing(false)} />}
    </aside>
  )
}

/** The first page of a PDF, small. */
function PdfThumb({ entry }: { entry: Entry }) {
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let current = true
    let opened: PDFDocumentProxy | null = null
    openPdf(entry.path)
      .then((d) => {
        opened = d
        if (current) setDoc(d)
        else void d.loadingTask.destroy()
      })
      .catch(() => current && setFailed(true))
    return () => {
      current = false
      void opened?.loadingTask.destroy()
    }
  }, [entry.path])
  if (failed) return <KindIcon entry={entry} />
  return doc ? <PdfPage doc={doc} page={1} width={220} label={`Première page de ${entry.name}`} /> : <KindIcon entry={entry} />
}

/** The first lines of a text, as plain text. */
function TextHead({ entry }: { entry: Entry }) {
  const [head, setHead] = useState<string | null>(null)
  useEffect(() => {
    let current = true
    textHead(entry.path, 2048)
      .then((t) => current && setHead(t.text))
      .catch(() => current && setHead(null))
    return () => {
      current = false
    }
  }, [entry.path])
  return head === null ? <KindIcon entry={entry} /> : <pre className="palette-text">{head}</pre>
}
