import { FileText, FolderOpen } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { messageOf } from '../api.ts'
import { formatDate, parentOf } from '../drive/files.ts'
import { DocViewer } from '../preview/DocViewer.tsx'
import { previewKind } from '../preview/preview.ts'
import { driveURL, navigate } from '../router.ts'
import {
  categories,
  categoryOf,
  categoryURL,
  fetchCategories,
  fetchDocuments,
  setCategory,
  type CategoriesSummary,
  type CategoryId,
  type DocumentCursor,
  type DocumentEntry,
} from './documents.ts'

/** A correction just made, which the message can undo. */
type Moved = { entry: DocumentEntry; previous: CategoryId | null }

/**
 * The app Documents (#73): the account's documents by category, classified by
 * the box from their content, and a menu on each to correct its category.
 * category is the one the address shows; undefined shows the first category
 * that has documents.
 */
export function DocumentsPage({ category }: { category?: CategoryId }) {
  const [summary, setSummary] = useState<CategoriesSummary | null>(null)
  const [items, setItems] = useState<DocumentEntry[] | null>(null)
  const [next, setNext] = useState<DocumentCursor | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [moved, setMoved] = useState<Moved | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [previewing, setPreviewing] = useState<DocumentEntry | null>(null)
  const [version, setVersion] = useState(0)
  const nav = useRef<HTMLElement>(null)

  // Without a category in the address, the counts pick the first one with
  // documents, once: emptying it by corrections does not jump to another.
  const [first, setFirst] = useState<CategoryId | null>(null)
  if (first === null && summary) setFirst(summary.categories.find((c) => c.count > 0)?.id ?? 'factures')
  const shown: CategoryId | null = category ?? first

  useEffect(() => {
    let current = true
    fetchCategories()
      .then((s) => current && setSummary(s))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [version])

  useEffect(() => {
    if (shown === null) return
    let current = true
    fetchDocuments(shown, null)
      .then((page) => {
        if (!current) return
        setItems(page.items)
        setNext(page.next)
      })
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [shown, version])

  // On a phone the categories are a row that scrolls: bring the open one into view.
  useEffect(() => {
    const row = nav.current
    const on = row?.querySelector('a.on')
    if (!row || !on || row.scrollWidth <= row.clientWidth) return
    row.scrollLeft += on.getBoundingClientRect().left - row.getBoundingClientRect().left - 16
  }, [shown, summary])

  // A new category starts from an empty list, not from the last one's.
  const [listed, setListed] = useState(shown)
  if (listed !== shown) {
    setListed(shown)
    setItems(null)
    setNext(null)
  }

  async function more() {
    if (!shown || !next) return
    setLoading(true)
    try {
      const page = await fetchDocuments(shown, next)
      setItems((list) => [...(list ?? []), ...page.items])
      setNext(page.next)
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setLoading(false)
    }
  }

  async function choose(doc: DocumentEntry, value: CategoryId | null) {
    try {
      const entry = await setCategory(doc.path, value)
      setNotice(null)
      if (entry.category === shown) {
        setItems((list) => list?.map((d) => (d.path === entry.path ? entry : d)) ?? null)
        setMoved(null)
      } else {
        setItems((list) => list?.filter((d) => d.path !== entry.path) ?? null)
        setMoved({ entry, previous: doc.corrected ? doc.category : null })
      }
      setSummary(await fetchCategories())
    } catch (err) {
      setMoved(null)
      setNotice(messageOf(err))
    }
  }

  async function undo(m: Moved) {
    setMoved(null)
    try {
      await setCategory(m.entry.path, m.previous)
      setVersion((v) => v + 1)
    } catch (err) {
      setNotice(messageOf(err))
    }
  }

  const count = (id: CategoryId) => summary?.categories.find((c) => c.id === id)?.count ?? 0
  const pending = summary?.pending ?? 0

  return (
    <section className="documents">
      <div className="drive-head">
        <h1>Documents</h1>
      </div>
      <p className="muted hint-line">
        Classés par la box d’après leur contenu, sans rien envoyer ailleurs. Une catégorie se corrige sur chaque document.
        {pending > 0 && ` ${pending} document${pending > 1 ? 's' : ''} en cours de classement.`}
      </p>
      {error && <p className="bad">{error}</p>}

      <div className="documents-body">
        <nav ref={nav} className="doc-categories" aria-label="Catégories">
          {categories.map((c) => (
            <a
              key={c.id}
              href={categoryURL(c.id)}
              className={c.id === shown ? 'on' : undefined}
              aria-current={c.id === shown ? 'page' : undefined}
              onClick={(e) => {
                e.preventDefault()
                navigate(categoryURL(c.id))
              }}
            >
              <c.icon size={16} aria-hidden />
              <span className="doc-category-label">{c.label}</span>
              <span className="doc-category-count">{summary ? count(c.id) : ''}</span>
            </a>
          ))}
        </nav>

        <div className="doc-list">
          {shown && <h2>{categoryOf(shown).label}</h2>}
          {items === null && !error && <p className="muted">Chargement…</p>}
          {items?.length === 0 && shown && <p className="empty muted">Aucun document dans {categoryOf(shown).label}.</p>}
          {items && items.length > 0 && (
            <table className="files">
              <thead>
                <tr>
                  <th>Nom</th>
                  <th className="col-date">Modifié</th>
                  <th className="col-actions">Catégorie</th>
                </tr>
              </thead>
              <tbody>
                {items.map((d) => (
                  <DocumentRow key={d.path} doc={d} onPreview={() => setPreviewing(d)} onChoose={(v) => choose(d, v)} />
                ))}
              </tbody>
            </table>
          )}
          {items && next && (
            <div className="timeline-end">
              {loading ? (
                <span className="muted">Chargement…</span>
              ) : (
                <button type="button" className="secondary more" onClick={more}>
                  Afficher plus
                </button>
              )}
            </div>
          )}
        </div>
      </div>

      {(moved || notice) && (
        <div className="toast" role="status">
          {moved ? (
            <>
              « {moved.entry.name} » classé dans {categoryOf(moved.entry.category).label}.
            </>
          ) : (
            notice
          )}
          {moved && (
            <button type="button" className="link" onClick={() => undo(moved)}>
              Annuler
            </button>
          )}
          <button
            type="button"
            className="icon-button"
            aria-label="Fermer"
            onClick={() => {
              setMoved(null)
              setNotice(null)
            }}
          >
            ×
          </button>
        </div>
      )}

      {previewing && <DocViewer entry={previewing} onClose={() => setPreviewing(null)} />}
    </section>
  )
}

/** One document: its name (opens the viewer, or its folder), its folder and date, and its category. */
function DocumentRow({ doc, onPreview, onChoose }: { doc: DocumentEntry; onPreview: () => void; onChoose: (c: CategoryId | null) => void }) {
  const folder = parentOf(doc.path)
  // The category first: on a phone the menu is narrow, and cuts the end.
  const auto = doc.auto ? `${categoryOf(doc.auto).label} (automatique)` : 'En attente (automatique)'
  return (
    <tr>
      <td className="name">
        <div className="name-inner">
          <FileText size={18} aria-hidden />
          {previewKind(doc) ? (
            <button type="button" className="link name-link" onClick={onPreview}>
              {doc.name}
            </button>
          ) : (
            <a
              href={driveURL(folder)}
              onClick={(e) => {
                e.preventDefault()
                navigate(driveURL(folder))
              }}
            >
              {doc.name}
            </a>
          )}
          <span className="meta muted">{formatDate(doc.modified_at)}</span>
          <span className="origin muted">Dans {folder || 'Mon drive'}</span>
        </div>
      </td>
      <td className="col-date muted">{formatDate(doc.modified_at)}</td>
      <td className="col-actions">
        <span className="row-actions always doc-actions">
          <select
            className="category-select"
            aria-label={`Catégorie de ${doc.name}`}
            value={doc.corrected ? doc.category : ''}
            onChange={(e) => onChoose((e.target.value || null) as CategoryId | null)}
          >
            <option value="">{auto}</option>
            {categories.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
          <a
            className="icon-button"
            href={driveURL(folder)}
            title="Afficher dans Fichiers"
            aria-label={`Afficher ${doc.name} dans Fichiers`}
            onClick={(e) => {
              e.preventDefault()
              navigate(driveURL(folder))
            }}
          >
            <FolderOpen size={16} aria-hidden />
          </a>
        </span>
      </td>
    </tr>
  )
}
