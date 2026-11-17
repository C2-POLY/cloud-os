import { Pause, Play, X } from 'lucide-react'
import { formatSize } from './files.ts'
import { uploadQueue, useUploads, type UploadItem, type UploadQueue, type UploadState } from './uploads.ts'

const labels: Record<UploadState, string> = {
  waiting: 'En attente',
  sending: 'Envoi',
  paused: 'En pause',
  retrying: 'Connexion perdue, nouvel essai…',
  done: 'Envoyé',
  failed: 'Échec',
  cancelled: 'Annulé',
}

function Row({ item, queue }: { item: UploadItem; queue: UploadQueue }) {
  const pct = item.size === 0 ? 100 : Math.floor((item.sent / item.size) * 100)
  const active = item.state === 'sending' || item.state === 'retrying' || item.state === 'waiting'
  return (
    <li className={`upload upload-${item.state}`}>
      <div className="upload-line">
        <span className="upload-name" title={item.path}>
          {item.name}
        </span>
        <span className="upload-actions">
          {active && item.state !== 'waiting' && (
            <button type="button" className="icon-button" aria-label={`Mettre en pause ${item.name}`} onClick={() => queue.pause(item.id)}>
              <Pause size={14} aria-hidden />
            </button>
          )}
          {item.state === 'paused' && (
            <button type="button" className="icon-button" aria-label={`Reprendre ${item.name}`} onClick={() => queue.resume(item.id)}>
              <Play size={14} aria-hidden />
            </button>
          )}
          {(active || item.state === 'paused') && (
            <button type="button" className="icon-button" aria-label={`Annuler ${item.name}`} onClick={() => queue.cancel(item.id)}>
              <X size={14} aria-hidden />
            </button>
          )}
        </span>
      </div>
      <progress max={100} value={pct} aria-label={`Progression de ${item.name}`} />
      <div className="upload-status muted">
        {item.error ?? labels[item.state]}
        {item.state !== 'failed' && item.state !== 'cancelled' && ` · ${formatSize(item.sent)} sur ${formatSize(item.size)}`}
      </div>
    </li>
  )
}

/** The list of uploads, at the bottom of the drive. */
export function UploadPanel({ queue = uploadQueue }: { queue?: UploadQueue }) {
  const items = useUploads(queue)
  if (items.length === 0) return null
  const over = items.every((it) => ['done', 'failed', 'cancelled'].includes(it.state))
  const running = items.filter((it) => !['done', 'failed', 'cancelled'].includes(it.state)).length
  return (
    <section className="uploads" aria-label="Envois">
      <header>
        <strong>{running > 0 ? `${running} envoi${running > 1 ? 's' : ''} en cours` : 'Envois terminés'}</strong>
        {over && (
          <button type="button" className="link" onClick={() => queue.clear()}>
            Effacer
          </button>
        )}
      </header>
      <ul>
        {items.map((it) => (
          <Row key={it.id} item={it} queue={queue} />
        ))}
      </ul>
    </section>
  )
}
