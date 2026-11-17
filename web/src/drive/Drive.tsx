import { FolderPlus, Upload } from 'lucide-react'
import { useEffect, useRef, useState, type DragEvent } from 'react'
import { ApiError, messageOf } from '../api.ts'
import { driveURL, navigate } from '../router.ts'
import { Breadcrumb } from './Breadcrumb.tsx'
import { MoveDialog, NameDialog, VersionsDialog } from './Dialogs.tsx'
import { FileList } from './FileList.tsx'
import { createFolder, join, listFolder, moveEntry, parentOf, restoreTrash, trashEntry, type Entry, type TrashEntry } from './files.ts'
import { ShareDialog } from '../share/ShareDialog.tsx'
import { UploadPanel } from './UploadPanel.tsx'
import { uploadQueue, type UploadQueue } from './uploads.ts'

type Listing = { kind: 'loading' } | { kind: 'ok'; folder: string; entries: Entry[] } | { kind: 'missing' } | { kind: 'error'; message: string }

type Dialog =
  | { kind: 'mkdir' }
  | { kind: 'rename'; entry: Entry }
  | { kind: 'move'; entry: Entry }
  | { kind: 'versions'; entry: Entry }
  | { kind: 'share'; entry: Entry }
  | null

/** The drive page for one folder: listing, uploads, and the folder's actions. */
export function Drive({ folder, queue = uploadQueue }: { folder: string; queue?: UploadQueue }) {
  const [listing, setListing] = useState<Listing>({ kind: 'loading' })
  const [version, setVersion] = useState(0)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [dragging, setDragging] = useState(false)
  const [trashed, setTrashed] = useState<TrashEntry | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const picker = useRef<HTMLInputElement>(null)
  const reload = () => setVersion((v) => v + 1)

  useEffect(() => {
    let current = true
    listFolder(folder)
      .then((entries) => current && setListing({ kind: 'ok', folder, entries }))
      .catch(
        (err: unknown) =>
          current &&
          setListing(err instanceof ApiError && err.status === 404 ? { kind: 'missing' } : { kind: 'error', message: messageOf(err) }),
      )
    return () => {
      current = false
    }
  }, [folder, version])

  // Refresh the listing when an upload into this folder is stored.
  useEffect(
    () =>
      queue.onStored((stored) => {
        if (stored === folder) setVersion((v) => v + 1)
      }),
    [queue, folder],
  )

  function drop(e: DragEvent) {
    e.preventDefault()
    setDragging(false)
    if (e.dataTransfer.files.length > 0) queue.add(e.dataTransfer.files, folder)
  }

  return (
    <section
      className={`drive${dragging ? ' dragging' : ''}`}
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes('Files')) {
          e.preventDefault()
          setDragging(true)
        }
      }}
      onDragLeave={(e) => e.currentTarget === e.target && setDragging(false)}
      onDrop={drop}
    >
      <div className="drive-head">
        <Breadcrumb folder={folder} />
        <div className="toolbar">
          <button type="button" className="secondary" onClick={() => setDialog({ kind: 'mkdir' })}>
            <FolderPlus size={16} aria-hidden /> Nouveau dossier
          </button>
          <button type="button" onClick={() => picker.current?.click()}>
            <Upload size={16} aria-hidden /> Envoyer
          </button>
          <input
            ref={picker}
            type="file"
            multiple
            hidden
            data-testid="file-picker"
            onChange={(e) => {
              if (e.target.files) queue.add(e.target.files, folder)
              e.target.value = ''
            }}
          />
        </div>
      </div>

      {listing.kind === 'loading' && <p className="muted">Chargement…</p>}
      {listing.kind === 'error' && <p className="bad">{listing.message}</p>}
      {listing.kind === 'missing' && (
        <p className="muted">
          Ce dossier n'existe pas ou plus.{' '}
          <a
            href={driveURL('')}
            onClick={(e) => {
              e.preventDefault()
              navigate(driveURL(''))
            }}
          >
            Revenir à Mon drive
          </a>
        </p>
      )}
      {listing.kind === 'ok' && (
        <FileList
          folder={listing.folder}
          entries={listing.entries}
          onRename={(entry) => setDialog({ kind: 'rename', entry })}
          onMove={(entry) => setDialog({ kind: 'move', entry })}
          onVersions={(entry) => setDialog({ kind: 'versions', entry })}
          onShare={(entry) => setDialog({ kind: 'share', entry })}
          onTrash={async (entry) => {
            try {
              setTrashed(await trashEntry(entry.path))
              setNotice(null)
              reload()
            } catch (err) {
              setNotice(messageOf(err))
            }
          }}
        />
      )}
      {(trashed || notice) && (
        <div className="toast" role="status">
          {trashed ? <>« {trashed.name} » mis à la corbeille.</> : notice}
          {trashed && (
            <button
              type="button"
              className="link"
              onClick={async () => {
                const item = trashed
                setTrashed(null)
                try {
                  await restoreTrash(item.id)
                  reload()
                } catch (err) {
                  setNotice(messageOf(err))
                }
              }}
            >
              Annuler
            </button>
          )}
          <button type="button" className="icon-button" aria-label="Fermer" onClick={() => { setTrashed(null); setNotice(null) }}>
            ×
          </button>
        </div>
      )}
      {dragging && <div className="drop-hint">Déposez pour envoyer dans « {folder ? folder.split('/').pop() : 'Mon drive'} »</div>}

      <UploadPanel queue={queue} />

      {dialog?.kind === 'mkdir' && (
        <NameDialog
          title="Nouveau dossier"
          submitLabel="Créer"
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            await createFolder(join(folder, name))
            reload()
          }}
        />
      )}
      {dialog?.kind === 'rename' && (
        <NameDialog
          title={`Renommer « ${dialog.entry.name} »`}
          initial={dialog.entry.name}
          submitLabel="Renommer"
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            if (name !== dialog.entry.name) await moveEntry(dialog.entry.path, join(parentOf(dialog.entry.path), name))
            reload()
          }}
        />
      )}
      {dialog?.kind === 'versions' && <VersionsDialog entry={dialog.entry} onRestored={reload} onClose={() => setDialog(null)} />}
      {dialog?.kind === 'share' && <ShareDialog entry={dialog.entry} onClose={() => setDialog(null)} />}
      {dialog?.kind === 'move' && (
        <MoveDialog
          entry={dialog.entry}
          onClose={() => setDialog(null)}
          onMove={async (to) => {
            await moveEntry(dialog.entry.path, to)
            reload()
          }}
        />
      )}
    </section>
  )
}
