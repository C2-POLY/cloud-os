import { ChevronRight, Cloud, Download, File, FileArchive, Folder, Lock } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { ApiError, messageOf } from '../api.ts'
import { formatDate, formatSize, join, sortEntries, type Entry } from '../drive/files.ts'
import { guestContentURL, guestInfo, guestList, guestUnlock, guestZipURL, type GuestItem } from './shares.ts'

type State =
  | { kind: 'loading' }
  | { kind: 'missing' }
  | { kind: 'password' }
  | { kind: 'ok'; item: GuestItem }
  | { kind: 'error'; message: string }

/** The page a guest opens from a link: no account, read and download only. */
export function SharePage({ token }: { token: string }) {
  const [state, setState] = useState<State>({ kind: 'loading' })
  const [version, setVersion] = useState(0)

  useEffect(() => {
    let current = true
    guestInfo(token)
      .then((item) => current && setState({ kind: 'ok', item }))
      .catch((err: unknown) => {
        if (!current) return
        if (err instanceof ApiError && err.status === 404) setState({ kind: 'missing' })
        else if (err instanceof ApiError && err.status === 403) setState({ kind: 'password' })
        else setState({ kind: 'error', message: messageOf(err) })
      })
    return () => {
      current = false
    }
  }, [token, version])

  return (
    <div className="guest">
      <header className="topbar">
        <span className="brand">
          <Cloud size={20} aria-hidden /> <span>Cloud OS</span>
        </span>
        <span className="muted guest-tag">Partage</span>
      </header>
      <main className="page">
        {state.kind === 'loading' && <p className="muted">Chargement…</p>}
        {state.kind === 'error' && <p className="bad">{state.message}</p>}
        {state.kind === 'missing' && (
          <div className="guest-card">
            <h1>Lien indisponible</h1>
            <p className="muted">Ce lien n’existe pas, a expiré ou a été révoqué. Demandez un nouveau lien à la personne qui vous l’a envoyé.</p>
          </div>
        )}
        {state.kind === 'password' && <PasswordForm token={token} onUnlocked={() => setVersion((v) => v + 1)} />}
        {state.kind === 'ok' && state.item.kind === 'file' && <SharedFile token={token} item={state.item} />}
        {state.kind === 'ok' && state.item.kind === 'dir' && <SharedFolder token={token} item={state.item} />}
      </main>
    </div>
  )
}

function PasswordForm({ token, onUnlocked }: { token: string; onUnlocked: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await guestUnlock(token, password)
      onUnlocked()
    } catch (err) {
      setError(messageOf(err))
      setBusy(false)
    }
  }

  return (
    <div className="guest-card">
      <h1>
        <Lock size={20} aria-hidden /> Lien protégé
      </h1>
      <p className="muted">Saisissez le mot de passe que la personne qui a partagé ce lien vous a donné.</p>
      <form className="form" onSubmit={submit}>
        <label>
          Mot de passe
          <input type="password" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} required autoFocus />
        </label>
        {error && (
          <p className="bad" role="alert">
            {error}
          </p>
        )}
        <button type="submit" disabled={busy}>
          Ouvrir
        </button>
      </form>
    </div>
  )
}

function Expiry({ item }: { item: GuestItem }) {
  return <p className="muted hint-line">Lien valable jusqu’au {formatDate(item.expires_at)}.</p>
}

function SharedFile({ token, item }: { token: string; item: GuestItem }) {
  return (
    <div className="guest-card">
      <h1 className="guest-name">
        <File size={22} aria-hidden /> {item.name}
      </h1>
      <p className="muted">
        {formatSize(item.size)} · modifié le {formatDate(item.modified_at)}
      </p>
      <p>
        <a className="button" href={guestContentURL(token, '')} download={item.name}>
          <Download size={16} aria-hidden /> Télécharger
        </a>
      </p>
      <Expiry item={item} />
    </div>
  )
}

function SharedFolder({ token, item }: { token: string; item: GuestItem }) {
  const [folder, setShown] = useState('')
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const setFolder = (f: string) => {
    setEntries(null)
    setError(null)
    setShown(f)
  }

  useEffect(() => {
    let current = true
    guestList(token, folder)
      .then((list) => current && setEntries(list))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [token, folder])

  const parts = folder ? folder.split('/') : []
  const zipName = (parts.length ? parts[parts.length - 1] : item.name) + '.zip'

  return (
    <section>
      <div className="drive-head">
        <nav className="breadcrumb" aria-label="Emplacement">
          <button type="button" className="crumb link" aria-current={folder === '' ? 'page' : undefined} onClick={() => setFolder('')}>
            <Folder size={16} aria-hidden className="icon-folder" /> {item.name}
          </button>
          {parts.map((p, i) => (
            <span key={i} className="crumb">
              <ChevronRight size={14} aria-hidden className="muted" />
              <button
                type="button"
                className="link"
                aria-current={i === parts.length - 1 ? 'page' : undefined}
                onClick={() => setFolder(parts.slice(0, i + 1).join('/'))}
              >
                {p}
              </button>
            </span>
          ))}
        </nav>
        {entries && entries.length > 0 && (
          <div className="toolbar">
            <a className="button" href={guestZipURL(token, folder)} download={zipName}>
              <FileArchive size={16} aria-hidden /> Tout télécharger (zip)
            </a>
          </div>
        )}
      </div>
      <Expiry item={item} />
      {error && <p className="bad">{error}</p>}
      {entries === null && !error && <p className="muted">Chargement…</p>}
      {entries?.length === 0 && <p className="empty muted">Ce dossier est vide.</p>}
      {entries && entries.length > 0 && (
        <table className="files">
          <tbody>
            {sortEntries(entries, 'name', true).map((e) => (
              <tr key={e.path}>
                <td className="name">
                  <div className="name-inner">
                    {e.kind === 'dir' ? <Folder size={18} aria-hidden className="icon-folder" /> : <File size={18} aria-hidden />}
                    {e.kind === 'dir' ? (
                      <button type="button" className="link" onClick={() => setFolder(join(folder, e.name))}>
                        {e.name}
                      </button>
                    ) : (
                      <span>{e.name}</span>
                    )}
                    <span className="meta muted">
                      {e.kind === 'file' ? `${formatSize(e.size)} · ` : ''}
                      {formatDate(e.modified_at)}
                    </span>
                  </div>
                </td>
                <td className="col-size muted">{e.kind === 'file' ? formatSize(e.size) : '—'}</td>
                <td className="col-date muted">{formatDate(e.modified_at)}</td>
                <td className="col-actions">
                  <span className="row-actions always">
                    {e.kind === 'file' && (
                      <a className="icon-button" href={guestContentURL(token, e.path)} download={e.name} title="Télécharger" aria-label={`Télécharger ${e.name}`}>
                        <Download size={16} aria-hidden />
                      </a>
                    )}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}
