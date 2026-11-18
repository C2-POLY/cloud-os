export type { Account } from '../../shared/api-types.ts'
/** Body of GET /api/health, as the core's `httpapi.Health` writes it. */
export interface Health {
  version: string
  db: 'ok' | 'down'
  migration?: number
  workers?: { id: string; seen_at: string }[]
  tasks?: { queued: number; running: number; failed: number }
}

/** Reads the core's health; a 503 still carries a body that says the database is down. */
export async function fetchHealth(signal?: AbortSignal): Promise<Health> {
  const res = await fetch('/api/health', { signal })
  if (!res.ok && res.status !== 503) {
    throw new Error(`HTTP ${res.status}`)
  }
  return (await res.json()) as Health
}

/** An error answered by the core, with its HTTP status and message. */
export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

/** Calls the core's JSON API; any non-2xx answer becomes an ApiError. */
export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) {
    let message = `HTTP ${res.status}`
    try {
      message = ((await res.json()) as { error?: string }).error ?? message
    } catch {
      // keep the status
    }
    throw new ApiError(res.status, message)
  }
  return (res.status === 204 ? undefined : await res.json()) as T
}

/** French wording of the core's known errors; anything else is shown as sent. */
const messages: Record<string, string> = {
  'wrong name or password': 'Identifiant ou mot de passe incorrect.',
  'too many failed attempts, try again in a minute': 'Trop d’essais. Réessayez dans une minute.',
  'the box is already set up': 'La box est déjà configurée.',
  'an account already has this name': 'Un compte porte déjà cet identifiant.',
  'the last active admin cannot be demoted or disabled':
    'Le dernier administrateur actif ne peut être ni rétrogradé ni désactivé.',
  'not signed in': 'Session expirée, reconnectez-vous.',
  'admins only': 'Réservé aux administrateurs.',
  'no such link': 'Ce lien n’existe pas, a expiré ou a été révoqué.',
  'password required': 'Ce lien est protégé par un mot de passe.',
  'wrong password': 'Mot de passe incorrect.',
}

/** The message to show the user for an error from api(). */
export function messageOf(err: unknown): string {
  if (err instanceof ApiError) {
    if (messages[err.message]) return messages[err.message]
    if (err.message.startsWith('invalid link: the expiry')) return 'Choisissez une date d’expiration dans le futur.'
    if (err.message.startsWith('invalid link: the password')) return 'Le mot de passe du lien doit avoir de 6 à 256 caractères.'
    if (err.message.startsWith('the password must have')) return 'Le mot de passe doit avoir de 10 à 256 caractères.'
    if (err.message.startsWith('invalid account')) {
      return 'Identifiant invalide : minuscules, chiffres, - ou _, 64 caractères au plus.'
    }
    return err.message
  }
  return 'Le cœur ne répond pas.'
}
