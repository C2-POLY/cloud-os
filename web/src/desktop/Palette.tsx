import { Download, File, FileImage, FileText, FileVideo, Folder, FolderOpen, Search } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import { messageOf } from '../api.ts'
import { downloadURL, formatDate, formatSize, parentOf, searchAll, thumbURL, type Entry, type SearchHit, type SnippetPart } from '../drive/files.ts'
import { enter } from '../motion/animate.ts'
import { driveURL, navigate } from '../router.ts'
import { filtersOf, folderOf, foundByText, kinds, periods, type Kind, type Period } from './search.ts'
import type { PDFDocumentProxy } from 'pdfjs-dist'
import { DocViewer } from '../preview/DocViewer.tsx'
import { PdfPage } from '../preview/PdfPage.tsx'
import { openPdf, previewKind, textHead } from '../preview/preview.ts'

const debounceMs = 150

/** The core's list for one query, tagged with the query it answers. */
type Answer = { id: number; results: SearchHit[]; partial: boolean; error?: string }

/**
 * The Demander palette: one list while typing, the core's order, documents
 * and photos together, found by their name, their words, their meaning or
 * what they show; narrowed by type and by date; picked with the arrows, with
 * a preview of the selected one. Enter shows it in Fichiers; Escape or a
 * click beside it closes.
 */
export function Palette({ onClose }: { onClose: () => void }) {
  const panel = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const [query, setQuery] = useState('')
  const [kind, setKind] = useState<Kind>('')
  const [period, setPeriod] = useState<Period>('')
  const [answer, setAnswer] = useState<Answer | null>(null)
  const [selected, setSelected] = useState(0)
  const latest = useRef(0)

  useLayoutEffect(() => {
    enter(panel.current, { scale: 0.97, y: -6 })
  }, [])

  // Ask the core 150 ms after the last key or filter change. An older answer
  // arriving late is dropped.
  useEffect(() => {
    const words = query.trim()
    const id = ++latest.current
    const timer = window.setTimeout(() => {
      if (!words) {
        setAnswer(null)
        return
      }
      searchAll(words, filtersOf(kind, period, new Date()))
        .then((a) => {
          if (id !== latest.current) return
          setAnswer({ id, results: a.results, partial: a.partial })
          setSelected(0)
        })
        .catch((err: unknown) => id === latest.current && setAnswer({ id, results: [], partial: false, error: messageOf(err) }))
    }, debounceMs)
    return () => window.clearTimeout(timer)
  }, [query, kind, period])

  const results = answer?.results ?? []
  const current = results[selected] as SearchHit | undefined
  const filtered = kind !== '' || period !== ''

  function open(e: Entry) {
    navigate(driveURL(folderOf(e)))
    onClose()
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    } else if (e.key === 'ArrowDown' && results.length) {
      e.preventDefault()
      setSelected((i) => (i + 1) % results.length)
    } else if (e.key === 'ArrowUp' && results.length) {
      e.preventDefault()
      setSelected((i) => (i - 1 + results.length) % results.length)
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
          <span className="visually-hidden">Chercher un fichier, un mot dans un document ou ce que montre une photo</span>
          <input
            ref={input}
            autoFocus
            value={query}
            placeholder="Cherche un fichier, un mot, une photo…"
            role="combobox"
            aria-expanded={results.length > 0}
            aria-controls="palette-results"
            aria-activedescendant={current ? `palette-${selected}` : undefined}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKey}
          />
          <kbd>Échap</kbd>
        </label>
        <div className="palette-filters" onKeyDown={(e) => e.key === 'Escape' && !e.defaultPrevented && onClose()}>
          <div className="palette-kinds" role="group" aria-label="Type">
            {kinds.map((k) => (
              <button
                key={k.value}
                type="button"
                aria-pressed={kind === k.value}
                onClick={() => {
                  setKind(k.value)
                  input.current?.focus()
                }}
              >
                {k.label}
              </button>
            ))}
          </div>
          <select aria-label="Période" value={period} onChange={(e) => setPeriod(e.target.value as Period)}>
            {periods.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </div>
        <div className="palette-main">
          <div className="palette-list">
            {answer?.error && <p className="bad palette-note">{answer.error}</p>}
            {answer === null && (
              <p className="muted palette-note">Tape un nom, des mots ou ce que montre une photo : « bail appart », « facture edf », « chien à la plage ».</p>
            )}
            {answer && !answer.error && results.length === 0 && (
              <p className="muted palette-note">
                Rien trouvé pour « {query.trim()} »{filtered ? ' avec ces filtres' : ''}.
              </p>
            )}
            {answer?.partial && <p className="muted palette-note">La recherche par le sens ne répond pas pour l'instant : résultats par nom et par mots seulement.</p>}
            <ul id="palette-results" role="listbox" aria-label="Résultats">
              {results.map((hit, i) => (
                <li key={hit.path} role="presentation">
                  <div
                    id={`palette-${i}`}
                    role="option"
                    aria-selected={i === selected}
                    className={i === selected ? 'palette-row on' : 'palette-row'}
                    onMouseMove={() => i !== selected && setSelected(i)}
                    onClick={() => open(hit)}
                  >
                    <RowVisual entry={hit} />
                    <span className="palette-name">
                      {hit.name}
                      <small>{parentOf(hit.path) || 'Mon drive'}</small>
                      <Extract hit={hit} />
                    </span>
                  </div>
                </li>
              ))}
            </ul>
          </div>
          {current && <Preview hit={current} onOpen={() => open(current)} />}
        </div>
      </div>
    </div>
  )
}

/** The thumbnail of a photo or a video in its line, else the icon of its kind. */
function RowVisual({ entry }: { entry: Entry }) {
  const thumb = thumbURL(entry)
  const [broken, setBroken] = useState<string | null>(null)
  if (!thumb || broken === thumb) return <KindIcon entry={entry} />
  return <img className="palette-thumb" src={thumb} alt="" loading="lazy" onError={() => setBroken(thumb)} />
}

/** The words found in a file's text, else its passage nearest in meaning. */
function Extract({ hit }: { hit: SearchHit }) {
  if (hit.snippet?.length) return <Snippet parts={hit.snippet} />
  if (hit.excerpt) return <span className="palette-snippet">{hit.excerpt}</span>
  return null
}

function KindIcon({ entry }: { entry: Entry }) {
  const props = { size: 18, 'aria-hidden': true }
  if (entry.kind === 'dir') return <Folder {...props} className="icon-folder" />
  if (entry.mime.startsWith('image/')) return <FileImage {...props} />
  if (entry.mime.startsWith('video/')) return <FileVideo {...props} />
  if (entry.mime.startsWith('text/') || entry.mime === 'application/pdf') return <FileText {...props} />
  return <File {...props} />
}

/** An extract of a document's text, the words found marked. Plain text nodes: nothing from the document is read as HTML. */
function Snippet({ parts }: { parts: SnippetPart[] }) {
  return (
    <span className="palette-snippet">
      {parts.map((p, i) => (p.hit ? <mark key={i}>{p.text}</mark> : p.text))}
    </span>
  )
}

function Preview({ hit, onOpen }: { hit: SearchHit; onOpen: () => void }) {
  const entry = hit
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
      {(hit.snippet?.length || hit.excerpt) && (
        <blockquote className="palette-extract" aria-label="Extrait">
          <Extract hit={hit} />
        </blockquote>
      )}
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
        {hit.found_by.length > 0 && (
          <>
            <dt>Trouvé par</dt>
            <dd>{foundByText(hit.found_by)}</dd>
          </>
        )}
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
