import { ChevronLeft, Download, Folder, RotateCcw } from 'lucide-react'
import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { messageOf } from '../api.ts'
import { enter, leave } from '../motion/animate.ts'
import { formatDate, formatDateSeconds, formatSize, join, listFolder, listVersions, parentOf, restoreVersion, versionURL, type Entry, type VersionEntry } from './files.ts'

/** Closes the modal around it, after its closing animation. */
const DismissContext = createContext<() => void>(() => {})

/**
 * A modal panel; Escape, the backdrop or a DismissButton closes it. It opens and
 * closes with a short animation, then calls onClose once. A dialog that closes
 * itself after a successful action calls onClose directly, without waiting.
 * It is drawn on the document's body: inside a desktop window, whose glass
 * (backdrop-filter) holds its fixed children, it would be shut in the window.
 */
export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const backdrop = useRef<HTMLDivElement>(null)
  const panel = useRef<HTMLDivElement>(null)
  const closing = useRef(false)

  useLayoutEffect(() => {
    enter(backdrop.current)
    enter(panel.current, { scale: 0.96 })
  }, [])

  const dismiss = useCallback(() => {
    if (closing.current) return
    closing.current = true
    void Promise.all([leave(backdrop.current), leave(panel.current, { scale: 0.96 })]).then(onClose)
  }, [onClose])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && dismiss()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [dismiss])
  return createPortal(
    <div ref={backdrop} className="backdrop" onMouseDown={(e) => e.target === e.currentTarget && dismiss()}>
      <div ref={panel} className="modal" role="dialog" aria-modal="true" aria-label={title} data-lenis-prevent>
        <h2>{title}</h2>
        <DismissContext.Provider value={dismiss}>{children}</DismissContext.Provider>
      </div>
    </div>,
    document.body,
  )
}

/** The Cancel or Close button of a modal: it closes it like Escape does. */
export function DismissButton({ children, autoFocus }: { children: ReactNode; autoFocus?: boolean }) {
  const dismiss = useContext(DismissContext)
  return (
    <button type="button" className="secondary" onClick={dismiss} autoFocus={autoFocus}>
      {children}
    </button>
  )
}

/** Asks for a name, for a new folder or a rename. */
export function NameDialog({
  title,
  initial = '',
  submitLabel,
  onSubmit,
  onClose,
}: {
  title: string
  initial?: string
  submitLabel: string
  onSubmit: (name: string) => Promise<void>
  onClose: () => void
}) {
  const [name, setName] = useState(initial)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const el = input.current
    if (!el) return
    el.focus()
    const dot = initial.lastIndexOf('.')
    el.setSelectionRange(0, dot > 0 ? dot : initial.length) // select the name, not the extension
  }, [initial])

  async function submit(e: FormEvent) {
    e.preventDefault()
    const clean = name.trim()
    if (!clean || clean.includes('/')) {
      setError('Un nom ne peut pas être vide ni contenir « / ».')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await onSubmit(clean)
      onClose()
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={title} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <label>
          Nom
          <input ref={input} value={name} onChange={(e) => setName(e.target.value)} required />
        </label>
        {error && (
          <p className="bad" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-buttons">
          <DismissButton>
            Annuler
          </DismissButton>
          <button type="submit" disabled={busy}>
            {submitLabel}
          </button>
        </div>
      </form>
    </Modal>
  )
}

/** Browses the folders to pick where an entry goes. */
export function MoveDialog({
  entry,
  onMove,
  onClose,
}: {
  entry: Entry
  onMove: (to: string) => Promise<void>
  onClose: () => void
}) {
  const [folder, setFolder] = useState(parentOf(entry.path))
  const [folders, setFolders] = useState<Entry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let current = true
    listFolder(folder)
      .then((list) => current && setFolders(list.filter((e) => e.kind === 'dir' && e.path !== entry.path)))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [folder, entry.path])

  const here = folder === parentOf(entry.path)
  async function move() {
    setBusy(true)
    setError(null)
    try {
      await onMove(join(folder, entry.name))
      onClose()
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={`Déplacer « ${entry.name} »`} onClose={onClose}>
      <p className="picker-path">
        {folder ? (
          <button type="button" className="icon-button" aria-label="Dossier parent" onClick={() => setFolder(parentOf(folder))}>
            <ChevronLeft size={16} aria-hidden />
          </button>
        ) : null}
        <strong>{folder ? folder.split('/').pop() : 'Mon drive'}</strong>
      </p>
      <ul className="picker">
        {folders === null && <li className="muted">Chargement…</li>}
        {folders?.length === 0 && <li className="muted">Aucun sous-dossier.</li>}
        {folders?.map((f) => (
          <li key={f.path}>
            <button type="button" onClick={() => setFolder(f.path)}>
              <Folder size={16} aria-hidden className="icon-folder" /> {f.name}
            </button>
          </li>
        ))}
      </ul>
      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-buttons">
        <DismissButton>
          Annuler
        </DismissButton>
        <button type="button" onClick={move} disabled={busy || here}>
          Déplacer ici
        </button>
      </div>
    </Modal>
  )
}

/** Asks before an action that cannot be undone. */
export function ConfirmDialog({
  title,
  message,
  confirmLabel,
  onConfirm,
  onClose,
}: {
  title: string
  message: string
  confirmLabel: string
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  return (
    <Modal title={title} onClose={onClose}>
      <p>{message}</p>
      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-buttons">
        <DismissButton autoFocus>
          Annuler
        </DismissButton>
        <button
          type="button"
          className="danger"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            setError(null)
            try {
              await onConfirm()
              onClose()
            } catch (err) {
              setError(messageOf(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          {confirmLabel}
        </button>
      </div>
    </Modal>
  )
}

/** The versions of a file: download one, or make it the current content again. */
export function VersionsDialog({ entry, onRestored, onClose }: { entry: Entry; onRestored: () => void; onClose: () => void }) {
  const [versions, setVersions] = useState<VersionEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [version, setVersion] = useState(0)

  useEffect(() => {
    let current = true
    listVersions(entry.path)
      .then((list) => current && setVersions(list))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [entry.path, version])

  return (
    <Modal title={`Versions de « ${entry.name} »`} onClose={onClose}>
      <p className="muted">
        Version actuelle : {formatSize(entry.size)}, modifiée le {formatDate(entry.modified_at)}.
      </p>
      {versions?.length === 0 && <p className="muted">Aucune version précédente : ce fichier n'a jamais été remplacé.</p>}
      {versions && versions.length > 0 && (
        <ul className="picker versions">
          {versions.map((v) => (
            <li key={v.id}>
              <span className="version-text">
                <strong>{formatDateSeconds(v.modified_at)}</strong>
                <span className="muted">
                  {formatSize(v.size)} · remplacée le {formatDateSeconds(v.replaced_at)}
                </span>
              </span>
              <span className="row-actions always">
                <a className="icon-button" href={versionURL(v.id, entry.name)} download={entry.name} aria-label={`Télécharger la version du ${formatDateSeconds(v.modified_at)}`} title="Télécharger">
                  <Download size={16} aria-hidden />
                </a>
                <button
                  type="button"
                  className="icon-button"
                  title="Restaurer"
                  aria-label={`Restaurer la version du ${formatDateSeconds(v.modified_at)}`}
                  onClick={async () => {
                    try {
                      await restoreVersion(v.id)
                      setNotice('Version restaurée. Le contenu remplacé est gardé comme version.')
                      setVersion((n) => n + 1)
                      onRestored()
                    } catch (err) {
                      setError(messageOf(err))
                    }
                  }}
                >
                  <RotateCcw size={16} aria-hidden />
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}
      {notice && (
        <p className="good" role="status">
          {notice}
        </p>
      )}
      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-buttons">
        <DismissButton>
          Fermer
        </DismissButton>
      </div>
    </Modal>
  )
}
