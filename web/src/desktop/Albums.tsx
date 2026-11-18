import { Images, Pencil, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { messageOf } from '../api.ts'
import { ConfirmDialog, DismissButton, Modal, NameDialog } from '../drive/Dialogs.tsx'
import { thumbURL } from '../drive/files.ts'
import { navigate } from '../router.ts'
import { albumsPath, albumURL, createAlbum, deleteAlbum, getAlbum, listAlbums, renameAlbum, type Album } from './albums.ts'
import { Timeline } from './Timeline.tsx'

const count = (n: number) => `${n} élément${n > 1 ? 's' : ''}`

function Cover({ album }: { album: Album }) {
  const url = album.cover ? thumbURL(album.cover) : null
  const [broken, setBroken] = useState(false)
  if (url && !broken) return <img src={url} alt="" loading="lazy" onError={() => setBroken(true)} />
  return (
    <span className="album-empty">
      <Images size={28} aria-hidden />
    </span>
  )
}

/** Photos › Albums: every album with its cover, and a new one. */
export function AlbumsPage() {
  const [albums, setAlbums] = useState<Album[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)

  useEffect(() => {
    listAlbums()
      .then(setAlbums)
      .catch((err: unknown) => setError(messageOf(err)))
  }, [])

  return (
    <div className="albums">
      <div className="toolbar albums-bar">
        <button type="button" onClick={() => setCreating(true)}>
          <Plus size={16} aria-hidden /> Nouvel album
        </button>
      </div>
      {error && <p className="bad">{error}</p>}
      {albums && albums.length === 0 && <p className="muted">Aucun album. Crée-en un, puis ajoutes-y des photos depuis Photos avec « Sélectionner ».</p>}
      {albums && albums.length > 0 && (
        <ul className="album-grid">
          {albums.map((a) => (
            <li key={a.id}>
              <a
                href={albumURL(a.id)}
                onClick={(e) => {
                  e.preventDefault()
                  navigate(albumURL(a.id))
                }}
              >
                <span className="album-cover">
                  <Cover album={a} />
                </span>
                <span className="album-name">{a.name}</span>
                <span className="muted">{count(a.count)}</span>
              </a>
            </li>
          ))}
        </ul>
      )}
      {creating && (
        <NameDialog
          title="Nouvel album"
          submitLabel="Créer"
          onSubmit={async (name) => {
            const a = await createAlbum(name)
            navigate(albumURL(a.id))
          }}
          onClose={() => setCreating(false)}
        />
      )}
    </div>
  )
}

/** One album: its name, rename and delete, and its photos as in Photos. */
export function AlbumPage({ id }: { id: number }) {
  const [album, setAlbum] = useState<Album | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [renaming, setRenaming] = useState(false)
  const [deleting, setDeleting] = useState(false)

  useEffect(() => {
    getAlbum(id)
      .then(setAlbum)
      .catch((err: unknown) => setError(messageOf(err)))
  }, [id])

  if (error) return <p className="bad">{error}</p>
  if (!album) return null
  return (
    <div className="album">
      <div className="toolbar album-head">
        <h2>{album.name}</h2>
        <span className="muted">{count(album.count)}</span>
        <button type="button" className="secondary" onClick={() => setRenaming(true)}>
          <Pencil size={14} aria-hidden /> Renommer
        </button>
        <button type="button" className="secondary" onClick={() => setDeleting(true)}>
          <Trash2 size={14} aria-hidden /> Supprimer l'album
        </button>
      </div>
      <Timeline album={id} onChange={() => void getAlbum(id).then(setAlbum).catch(() => {})} />
      {renaming && (
        <NameDialog
          title="Renommer l'album"
          initial={album.name}
          submitLabel="Renommer"
          onSubmit={async (name) => setAlbum(await renameAlbum(id, name))}
          onClose={() => setRenaming(false)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Supprimer l'album ?"
          message={`L'album « ${album.name} » disparaît. Ses photos restent dans Photos et dans Fichiers.`}
          confirmLabel="Supprimer l'album"
          onConfirm={async () => {
            await deleteAlbum(id)
            navigate(albumsPath)
          }}
          onClose={() => setDeleting(false)}
        />
      )}
    </div>
  )
}

/** Picks the album to add the selected photos to, or makes a new one. */
export function AlbumPicker({ count: n, onPick, onClose }: { count: number; onPick: (id: number, name: string) => Promise<void>; onClose: () => void }) {
  const [albums, setAlbums] = useState<Album[] | null>(null)
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    listAlbums()
      .then(setAlbums)
      .catch((err: unknown) => setError(messageOf(err)))
  }, [])

  const pick = useCallback(
    async (id: number, albumName: string) => {
      setBusy(true)
      setError(null)
      try {
        await onPick(id, albumName)
      } catch (err) {
        setError(messageOf(err))
        setBusy(false)
      }
    },
    [onPick],
  )

  async function create(e: FormEvent) {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    try {
      const a = await createAlbum(name.trim())
      await pick(a.id, a.name)
    } catch (err) {
      setError(messageOf(err))
      setBusy(false)
    }
  }

  return (
    <Modal title={`Ajouter ${n} élément${n > 1 ? 's' : ''} à un album`} onClose={onClose}>
      {albums && albums.length > 0 && (
        <ul className="album-pick">
          {albums.map((a) => (
            <li key={a.id}>
              <button type="button" disabled={busy} onClick={() => void pick(a.id, a.name)}>
                <span className="album-cover small">
                  <Cover album={a} />
                </span>
                <span>{a.name}</span>
                <span className="muted">{count(a.count)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <form className="form" onSubmit={create}>
        <label>
          Nouvel album
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Bretagne 2024" />
        </label>
        {error && (
          <p className="bad" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-buttons">
          <DismissButton>Annuler</DismissButton>
          <button type="submit" disabled={busy || !name.trim()}>
            Créer et ajouter
          </button>
        </div>
      </form>
    </Modal>
  )
}
