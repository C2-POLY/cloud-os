import { File, Folder, RotateCcw, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { messageOf } from '../api.ts'
import { ConfirmDialog } from './Dialogs.tsx'
import { emptyTrash, formatDate, formatSize, inDays, listTrash, parentOf, purgeTrash, restoreTrash, trashReasons, type TrashEntry } from './files.ts'

type Confirm = { kind: 'purge'; item: TrashEntry } | { kind: 'empty' } | null

/** The trash: restore an item, erase one for good, or empty it all. */
export function TrashPage() {
  const [items, setItems] = useState<TrashEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [version, setVersion] = useState(0)
  const [confirm, setConfirm] = useState<Confirm>(null)
  const reload = () => setVersion((v) => v + 1)

  useEffect(() => {
    let current = true
    listTrash()
      .then((list) => current && setItems(list))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [version])

  return (
    <section className="trash">
      <div className="drive-head">
        <h1>Corbeille</h1>
        {items && items.length > 0 && (
          <div className="toolbar">
            <button type="button" className="secondary" onClick={() => setConfirm({ kind: 'empty' })}>
              <Trash2 size={16} aria-hidden /> Vider la corbeille
            </button>
          </div>
        )}
      </div>
      <p className="muted hint-line">Les éléments supprimés restent ici 30 jours, puis sont effacés pour de bon.</p>
      {error && <p className="bad">{error}</p>}
      {notice && (
        <p className="good" role="status">
          {notice}
        </p>
      )}
      {items === null && !error && <p className="muted">Chargement…</p>}
      {items?.length === 0 && <p className="empty muted">La corbeille est vide.</p>}
      {items && items.length > 0 && (
        <table className="files">
          <thead>
            <tr>
              <th>Nom</th>
              <th className="col-size">Taille</th>
              <th className="col-date">Supprimé</th>
              <th className="col-actions">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((it) => (
              <tr key={it.id}>
                <td className="name">
                  <div className="name-inner">
                    {it.kind === 'dir' ? <Folder size={18} aria-hidden className="icon-folder" /> : <File size={18} aria-hidden />}
                    <span>{it.name}</span>
                    <span className="meta muted">
                      {formatSize(it.size)} · {it.reason ? 'gardé' : 'supprimé'} le {formatDate(it.deleted_at)}
                    </span>
                    <span className="origin muted">
                      Était dans {parentOf(it.path) || 'Mon drive'} · effacé {inDays(it.expires_at)}
                    </span>
                    {it.reason && <span className="origin kept">{trashReasons[it.reason]}</span>}
                  </div>
                </td>
                <td className="col-size muted">{formatSize(it.size)}</td>
                <td className="col-date muted">{formatDate(it.deleted_at)}</td>
                <td className="col-actions">
                  <span className="row-actions always">
                    <button
                      type="button"
                      className="icon-button"
                      title="Restaurer"
                      aria-label={`Restaurer ${it.name}`}
                      onClick={async () => {
                        try {
                          const restored = await restoreTrash(it.id)
                          setNotice(`« ${it.name} » restauré dans ${parentOf(restored.path) || 'Mon drive'}${restored.name !== it.name ? ` sous le nom « ${restored.name} »` : ''}.`)
                          reload()
                        } catch (err) {
                          setError(messageOf(err))
                        }
                      }}
                    >
                      <RotateCcw size={16} aria-hidden />
                    </button>
                    <button
                      type="button"
                      className="icon-button"
                      title="Supprimer définitivement"
                      aria-label={`Supprimer définitivement ${it.name}`}
                      onClick={() => setConfirm({ kind: 'purge', item: it })}
                    >
                      <Trash2 size={16} aria-hidden />
                    </button>
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {confirm?.kind === 'purge' && (
        <ConfirmDialog
          title="Supprimer définitivement ?"
          message={`« ${confirm.item.name} »${confirm.item.kind === 'dir' ? ' et tout son contenu' : ''} sera effacé de la box. Cette action ne peut pas être annulée.`}
          confirmLabel="Supprimer définitivement"
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            await purgeTrash(confirm.item.id)
            setNotice(`« ${confirm.item.name} » effacé.`)
            reload()
          }}
        />
      )}
      {confirm?.kind === 'empty' && items && (
        <ConfirmDialog
          title="Vider la corbeille ?"
          message={`${items.length > 1 ? `Les ${items.length} éléments` : 'L’élément'} de la corbeille (${formatSize(items.reduce((n, it) => n + it.size, 0))}) ${items.length > 1 ? 'seront effacés' : 'sera effacé'} de la box. Cette action ne peut pas être annulée.`}
          confirmLabel="Vider la corbeille"
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            const { purged } = await emptyTrash()
            setNotice(`${purged} élément${purged > 1 ? 's' : ''} effacé${purged > 1 ? 's' : ''}.`)
            reload()
          }}
        />
      )}
    </section>
  )
}
