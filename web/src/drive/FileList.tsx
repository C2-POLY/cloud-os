import { ArrowDown, ArrowUp, Download, File, FileImage, FileText, FileVideo, Folder, FolderInput, Music, Pencil, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { driveURL, navigate } from '../router.ts'
import { downloadURL, formatDate, formatSize, sortEntries, type Entry, type SortKey } from './files.ts'

function KindIcon({ entry }: { entry: Entry }) {
  const props = { size: 18, 'aria-hidden': true }
  if (entry.kind === 'dir') return <Folder {...props} className="icon-folder" />
  if (entry.mime.startsWith('image/')) return <FileImage {...props} />
  if (entry.mime.startsWith('video/')) return <FileVideo {...props} />
  if (entry.mime.startsWith('audio/')) return <Music {...props} />
  if (entry.mime.startsWith('text/') || entry.mime === 'application/pdf') return <FileText {...props} />
  return <File {...props} />
}

export function FileList({
  entries,
  onRename,
  onMove,
  onTrash,
}: {
  entries: Entry[]
  onRename: (entry: Entry) => void
  onMove: (entry: Entry) => void
  onTrash: (entry: Entry) => void
}) {
  const [key, setKey] = useState<SortKey>('name')
  const [ascending, setAscending] = useState(true)
  const sorted = sortEntries(entries, key, ascending)

  function header(k: SortKey, label: string, className?: string) {
    const active = key === k
    return (
      <th className={className} aria-sort={active ? (ascending ? 'ascending' : 'descending') : 'none'}>
        <button
          type="button"
          className="sort"
          onClick={() => {
            if (active) setAscending(!ascending)
            else {
              setKey(k)
              setAscending(k === 'name')
            }
          }}
        >
          {label}
          {active && (ascending ? <ArrowUp size={14} aria-hidden /> : <ArrowDown size={14} aria-hidden />)}
        </button>
      </th>
    )
  }

  if (entries.length === 0) {
    return <p className="empty muted">Ce dossier est vide. Déposez des fichiers ici pour les envoyer.</p>
  }

  return (
    <table className="files">
      <thead>
        <tr>
          {header('name', 'Nom')}
          {header('size', 'Taille', 'col-size')}
          {header('modified', 'Modifié', 'col-date')}
          <th className="col-actions">
            <span className="visually-hidden">Actions</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {sorted.map((e) => (
          <tr key={e.path}>
            <td className="name">
              <div className="name-inner">
                <KindIcon entry={e} />
                {e.kind === 'dir' ? (
                  <a
                    href={driveURL(e.path)}
                    onClick={(ev) => {
                      ev.preventDefault()
                      navigate(driveURL(e.path))
                    }}
                  >
                    {e.name}
                  </a>
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
              <span className="row-actions">
                {e.kind === 'file' ? (
                  <a className="icon-button" href={downloadURL(e.path)} download={e.name} title="Télécharger" aria-label={`Télécharger ${e.name}`}>
                    <Download size={16} aria-hidden />
                  </a>
                ) : (
                  <span className="icon-button" aria-hidden />
                )}
                <button type="button" className="icon-button" title="Renommer" aria-label={`Renommer ${e.name}`} onClick={() => onRename(e)}>
                  <Pencil size={16} aria-hidden />
                </button>
                <button type="button" className="icon-button" title="Déplacer vers…" aria-label={`Déplacer ${e.name}`} onClick={() => onMove(e)}>
                  <FolderInput size={16} aria-hidden />
                </button>
                <button type="button" className="icon-button" title="Mettre à la corbeille" aria-label={`Supprimer ${e.name}`} onClick={() => onTrash(e)}>
                  <Trash2 size={16} aria-hidden />
                </button>
              </span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
