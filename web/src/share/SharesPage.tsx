import { File, Folder, Link2Off } from 'lucide-react'
import { useEffect, useState } from 'react'
import { messageOf } from '../api.ts'
import { ConfirmDialog } from '../drive/Dialogs.tsx'
import { formatDate, parentOf } from '../drive/files.ts'
import { listShares, revokeShare, type ShareEntry } from './shares.ts'

function status(s: ShareEntry): string {
  if (s.expired) return `Expiré le ${formatDate(s.expires_at)}`
  if (s.trashed) return 'Dans la corbeille : le lien ne donne rien'
  return `Expire le ${formatDate(s.expires_at)}`
}

/** The signed-in account's links: where each item is, when it expires, and Revoke. */
export function SharesPage() {
  const [items, setItems] = useState<ShareEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [version, setVersion] = useState(0)
  const [confirm, setConfirm] = useState<ShareEntry | null>(null)

  useEffect(() => {
    let current = true
    listShares()
      .then((list) => current && setItems(list))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [version])

  return (
    <section className="shares">
      <div className="drive-head">
        <h1>Partages</h1>
      </div>
      <p className="muted hint-line">Les liens que vous avez créés. Un lien expiré reste affiché 30 jours, puis disparaît.</p>
      {error && <p className="bad">{error}</p>}
      {notice && (
        <p className="good" role="status">
          {notice}
        </p>
      )}
      {items === null && !error && <p className="muted">Chargement…</p>}
      {items?.length === 0 && <p className="empty muted">Aucun lien de partage. Partagez un fichier ou un dossier depuis le drive.</p>}
      {items && items.length > 0 && (
        <table className="files">
          <thead>
            <tr>
              <th>Élément</th>
              <th className="col-date">Créé</th>
              <th className="col-actions">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((s) => (
              <tr key={s.id} className={s.expired || s.trashed ? 'inactive' : undefined}>
                <td className="name">
                  <div className="name-inner">
                    {s.kind === 'dir' ? <Folder size={18} aria-hidden className="icon-folder" /> : <File size={18} aria-hidden />}
                    <span>{s.name}</span>
                    {s.has_password && <span className="badge">Mot de passe</span>}
                    <span className="origin muted">
                      Dans {parentOf(s.path) || 'Mon drive'} · {status(s)}
                    </span>
                  </div>
                </td>
                <td className="col-date muted">{formatDate(s.created_at)}</td>
                <td className="col-actions">
                  <span className="row-actions always">
                    <button type="button" className="icon-button" title="Révoquer" aria-label={`Révoquer le lien vers ${s.name}`} onClick={() => setConfirm(s)}>
                      <Link2Off size={16} aria-hidden />
                    </button>
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {confirm && (
        <ConfirmDialog
          title="Révoquer le lien ?"
          message={`Le lien vers « ${confirm.name} » ne donnera plus rien, à personne. Le ${confirm.kind === 'dir' ? 'dossier' : 'fichier'} lui-même reste dans votre drive.`}
          confirmLabel="Révoquer"
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            await revokeShare(confirm.id)
            setNotice(`Lien vers « ${confirm.name} » révoqué.`)
            setVersion((v) => v + 1)
          }}
        />
      )}
    </section>
  )
}
