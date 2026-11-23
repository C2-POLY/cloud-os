import { AlertTriangle, CheckCircle2, HardDrive, HeartPulse, LifeBuoy, Server, ShieldCheck } from 'lucide-react'
import type { ReactNode } from 'react'
import { formatDate } from '../drive/files.ts'
import { Health } from '../Health.tsx'
import { navigate } from '../router.ts'
import {
  alertText,
  alertsLabel,
  backupFacts,
  freePercent,
  runLabel,
  spaceLabel,
  stateLabel,
  statusPath,
  useBoxStatus,
  useFreshBoxStatus,
  type BackupRun,
  type BoxStatus as Status,
  type DiskSpace,
  type ServiceState,
} from './status.ts'

const time = new Intl.DateTimeFormat('fr-FR', { hour: '2-digit', minute: '2-digit', second: '2-digit' })

/** « État de la box », for the admins: the alerts, the disks, the services, the backup, the SMB writes kept, the core's health. */
export function BoxStatus() {
  const { status, error } = useFreshBoxStatus()
  if (!status) {
    return (
      <div className="box-status">
        {error ? (
          <p className="bad" role="alert">
            L’état de la box est illisible : {error}
          </p>
        ) : (
          <p className="muted">Lecture de l’état…</p>
        )}
      </div>
    )
  }
  return (
    <div className="box-status">
      <Alerts status={status} error={error} />
      <Disks status={status} />
      <Services status={status} />
      <Backup status={status} />
      <Kept status={status} />
      <Section id="st-core" icon={<HeartPulse size={18} aria-hidden />} title="Cœur et base">
        <Health />
      </Section>
    </div>
  )
}

function Section({ id, icon, title, children }: { id: string; icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <section className="status-section" aria-labelledby={id}>
      <h2 id={id}>
        {icon} {title}
      </h2>
      {children}
    </section>
  )
}

function Alerts({ status, error }: { status: Status; error: string | null }) {
  const checked = time.format(new Date(status.checked_at))
  return (
    <section className="status-alerts" aria-label="Alertes">
      {status.alerts.length === 0 ? (
        <p className="status-all-good">
          <CheckCircle2 size={20} aria-hidden /> Tout va bien.
        </p>
      ) : (
        <ul>
          {status.alerts.map((a) => (
            <li key={a.kind + (a.service ?? '')} className="status-alert">
              <AlertTriangle size={18} aria-hidden /> {alertText(a, status)}
            </li>
          ))}
        </ul>
      )}
      <p className="status-checked">
        Vérifié à {checked}, de nouveau toutes les 30 secondes.
        {error && <span className="bad"> La dernière lecture a échoué : {error}</span>}
      </p>
    </section>
  )
}

function Disks({ status }: { status: Status }) {
  const { backup } = status
  const alert = (kind: string) => status.alerts.some((a) => a.kind === kind)
  return (
    <Section id="st-disks" icon={<HardDrive size={18} aria-hidden />} title="Disques">
      <div className="status-disks">
        <DiskRow label="Données" disk={status.data_disk} low={alert('data_disk_low')} missing="Le disque des données n’a pas pu être mesuré." />
        <DiskRow
          label="Sauvegarde"
          disk={backup.on ? status.backup_disk : undefined}
          low={alert('backup_disk_low')}
          missing={backup.on ? 'Pas encore mesuré : chaque sauvegarde le mesure.' : 'La sauvegarde n’est pas en place.'}
          note={backup.on && status.backup_disk?.at ? `Mesuré le ${formatDate(status.backup_disk.at)}` : undefined}
        />
      </div>
    </Section>
  )
}

/** A disk's free space and a bar of what is used; low (the core's alert) turns it red. */
function DiskRow({ label, disk, low, missing, note }: { label: string; disk?: DiskSpace; low: boolean; missing: string; note?: string }) {
  if (!disk) {
    return (
      <div className="status-disk">
        <strong>{label}</strong>
        <span className="muted">{missing}</span>
      </div>
    )
  }
  const free = freePercent(disk)
  const used = 100 - free
  return (
    <div className={low ? 'status-disk low' : 'status-disk'}>
      <strong>{label}</strong>
      <span>
        {spaceLabel(disk)} ({free} %)
      </span>
      <div className="status-bar" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={used} aria-label={`${label} : ${used} % occupé`}>
        <span style={{ width: `${used}%` }} />
      </div>
      {note && <span className="muted">{note}</span>}
    </div>
  )
}

/** « à 10:42:05 » today, « le 21 nov. 2026, 18:00 » before. */
function seenLabel(iso: string): string {
  const at = new Date(iso)
  return at.toDateString() === new Date().toDateString() ? `à ${time.format(at)}` : `le ${formatDate(iso)}`
}

/** A service's state in a word, with its color. */
function ServiceWord({ state, on }: { state: ServiceState | undefined; on?: string }) {
  const className = state === 'ok' ? 'good' : state === 'down' ? 'bad' : 'muted'
  return <span className={className}>{stateLabel(state, on)}</span>
}

function Services({ status }: { status: Status }) {
  const svc = (name: string) => status.services.find((s) => s.name === name)
  const worker = svc('worker')
  const smb = svc('smb')
  const ts = svc('tailscale')
  return (
    <Section id="st-services" icon={<Server size={18} aria-hidden />} title="Services">
      <dl className="status-facts">
        <dt>Cœur</dt>
        <dd>
          <span className="good">v{status.version}</span>
        </dd>
        <dt>Analyse des fichiers</dt>
        <dd>
          <ServiceWord state={worker?.analysis} on="en marche" />
          {worker?.seen_at && <span className="muted"> · worker vu {seenLabel(worker.seen_at)}</span>}
          {!worker?.seen_at && <span className="muted"> · aucun worker vu</span>}
        </dd>
        <dt>Recherche par le sens</dt>
        <dd>
          <ServiceWord state={worker?.search} />
        </dd>
        <dt>SMB</dt>
        <dd>
          <ServiceWord state={smb?.state} on="en marche" />
        </dd>
        <dt>Tailscale</dt>
        <dd>
          <ServiceWord state={ts?.state} on="en marche" />
          {status.access.public_url && <span className="status-url"> · {status.access.public_url}</span>}
          {ts?.state !== 'off' && (
            <span className="muted"> · liens de partage {status.access.funnel ? 'ouverts sur internet (Funnel)' : 'réservés au tailnet'}</span>
          )}
        </dd>
      </dl>
    </Section>
  )
}

/** The newest run of a kind: where it ended, when, what it saw, and its error. */
function RunRow({ label, run, facts }: { label: string; run?: BackupRun; facts?: string }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>
        {!run ? (
          <span className="muted">jamais</span>
        ) : (
          <>
            <span className={run.state === 'ok' ? 'good' : run.state === 'running' ? undefined : 'bad'}>{runLabel(run.state)}</span>
            <span className="muted"> · {formatDate(run.finished_at ?? run.started_at)}</span>
            {facts && <span className="status-run-facts">{facts}</span>}
            {run.error && <span className="status-run-error">{run.error}</span>}
          </>
        )}
      </dd>
    </>
  )
}

function Backup({ status }: { status: Status }) {
  const { on, last } = status.backup
  const rt = last.restore_test
  const rtFacts = rt?.sample !== undefined ? `${rt.sample} fichiers comparés à l’index, ${rt.mismatches ?? 0} différent${(rt.mismatches ?? 0) > 1 ? 's' : ''}` : undefined
  return (
    <Section id="st-backup" icon={<ShieldCheck size={18} aria-hidden />} title="Sauvegarde">
      {!on && (
        <p className="muted">
          La sauvegarde n’est pas en place. Sur la box : <code>scripts/box backup-init &lt;dossier du disque de sauvegarde&gt;</code>
        </p>
      )}
      <dl className="status-facts">
        <RunRow label="Dernière sauvegarde" run={last.backup} facts={last.backup ? backupFacts(last.backup) : undefined} />
        <RunRow label="Dernier test de restauration" run={rt} facts={rtFacts} />
        <RunRow label="Dernière vérification" run={last.check} />
        {last.copy && <RunRow label="Dernière copie distante" run={last.copy} />}
      </dl>
    </Section>
  )
}

function Kept({ status }: { status: Status }) {
  const { waiting, in_trash, last_at } = status.smb_kept
  return (
    <Section id="st-kept" icon={<LifeBuoy size={18} aria-hidden />} title="Fichiers SMB gardés">
      <p className="muted">Un fichier envoyé par SMB que la box n’a pas pu enregistrer est gardé : il attend la base, puis va dans la corbeille de son propriétaire.</p>
      <dl className="status-facts">
        <dt>En attente de la base</dt>
        <dd className={waiting ? 'bad' : undefined}>{waiting === undefined ? 'illisible' : waiting}</dd>
        <dt>Mis à la corbeille sur 30 jours</dt>
        <dd>
          {in_trash}
          {last_at && <span className="muted"> · le dernier le {formatDate(last_at)}</span>}
        </dd>
      </dl>
    </Section>
  )
}

/** The admin's badge in the menu bar: the number of alerts, which opens the panel; nothing when all is well, nothing for a member. */
export function AlertsBadge({ admin }: { admin: boolean }) {
  const { status } = useBoxStatus(admin)
  const n = status?.alerts.length ?? 0
  if (!admin || n === 0) return null
  const label = alertsLabel(n)
  return (
    <button type="button" className="menubar-alerts" title="Voir l’état de la box" aria-label={`${label} : voir l’état de la box`} onClick={() => navigate(statusPath)}>
      <AlertTriangle size={13} aria-hidden /> {label}
    </button>
  )
}
