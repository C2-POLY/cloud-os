import { useEffect, useState } from 'react'
import { fetchHealth, type Health as HealthBody } from './api.ts'

/** How often the page asks the core again. */
export const REFRESH_MS = 5000

type State =
  | { kind: 'loading' }
  | { kind: 'ok'; health: HealthBody }
  | { kind: 'unreachable'; error: string }

export function Health({ refreshMs = REFRESH_MS }: { refreshMs?: number }) {
  const [state, setState] = useState<State>({ kind: 'loading' })

  useEffect(() => {
    const controller = new AbortController()
    const load = () =>
      fetchHealth(controller.signal)
        .then((health) => setState({ kind: 'ok', health }))
        .catch((err: unknown) => {
          if (!controller.signal.aborted) {
            setState({ kind: 'unreachable', error: String(err) })
          }
        })
    load()
    const timer = setInterval(load, refreshMs)
    return () => {
      controller.abort()
      clearInterval(timer)
    }
  }, [refreshMs])

  if (state.kind === 'loading') {
    return <p className="muted">Lecture de l'état…</p>
  }
  if (state.kind === 'unreachable') {
    return (
      <dl className="health">
        <dt>Cœur</dt>
        <dd className="bad">injoignable</dd>
      </dl>
    )
  }

  const { health } = state
  const workers = health.workers ?? []
  return (
    <dl className="health">
      <dt>Cœur</dt>
      <dd className="good">v{health.version}</dd>
      <dt>Base</dt>
      <dd className={health.db === 'ok' ? 'good' : 'bad'}>
        {health.db === 'ok' ? 'joignable' : 'injoignable'}
      </dd>
      {health.migration !== undefined && (
        <>
          <dt>Migration</dt>
          <dd>{health.migration}</dd>
        </>
      )}
      <dt>Worker</dt>
      {health.db !== 'ok' ? (
        <dd className="muted">inconnu</dd>
      ) : workers.length === 0 ? (
        <dd className="bad">absent</dd>
      ) : (
        <dd className="good">
          {workers.map((w) => (
            <span key={w.id} className="worker">
              {w.id} · vu à {new Date(w.seen_at).toLocaleTimeString('fr-FR')}
            </span>
          ))}
        </dd>
      )}
      {health.tasks && (
        <>
          <dt>Tâches</dt>
          <dd className={health.tasks.failed > 0 ? 'bad' : undefined}>
            {health.tasks.queued} en attente · {health.tasks.running} en cours · {health.tasks.failed} en échec
          </dd>
        </>
      )}
    </dl>
  )
}
