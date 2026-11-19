// Types of the core's HTTP API, shared by the web front (web/) and the mobile
// app (mobile/). Types only: both import them with `import type`, so no
// bundler ever has to load this file. Keep them in step with
// core/internal/httpapi.

/** An account, as the core's `auth.Account` writes it. */
export interface Account {
  name: string
  display_name: string
  role: 'admin' | 'member'
  disabled_at?: string
  created_at: string
}

/** One file or folder, as the core's `httpapi.Entry` writes it. */
export interface Entry {
  path: string
  name: string
  kind: 'file' | 'dir'
  size: number
  mime: string
  sha256?: string
  modified_at: string
}

/** A piece of an extract: a word found (hit) or the text around it, as the core's `storage.SnippetPart` writes it. */
export interface SnippetPart {
  text: string
  hit?: boolean
}

/** A file whose text holds the words searched, with an extract, as the core's `httpapi.ContentHit` writes it (GET /api/search/content). */
export interface ContentHit extends Entry {
  snippet: SnippetPart[]
}

/**
 * A photo or video found by what it shows, as the core's `httpapi.PhotoHit`
 * writes it: GET /api/search/photos?q=…, best first. 503 when the workers
 * cannot encode the query.
 */
export interface PhotoHit extends Entry {
  score: number // cosine similarity to the query; compare ranks, not scores across searches
}

/**
 * A document found by meaning, as the core's `httpapi.SemanticHit` writes it:
 * GET /api/search/semantic?q=…&limit=…, best first. 503 when the workers
 * cannot encode the query.
 */
export interface SemanticHit extends Entry {
  score: number // cosine similarity of the best window of its text, 1 at most
  start: number // that window, in characters of the document's text
  end: number
  excerpt: string // the window's first characters
}

/** How a file of Demander's list was found: its name, the words of its text (as typed, a word begun, a word near one with a typo), its meaning, what its photo shows. */
export type FoundBy = 'name' | 'text' | 'prefix' | 'fuzzy' | 'meaning' | 'image'

/**
 * A file of Demander's one list, as the core's `httpapi.SearchHit` writes it
 * (GET /api/search/all): found by its name, the words of its text, the
 * meaning of its text or what its photo shows.
 */
export interface SearchHit extends Entry {
  score: number // orders the list of one query; does not compare across queries
  found_by: FoundBy[]
  snippet?: SnippetPart[] // the words found in its text
  excerpt?: string // else its passage nearest in meaning
}

/** GET /api/search/all?q=…&limit=…[&type=docs|photos][&from=…&to=…&tz=…], best first. */
export interface SearchAnswer {
  results: SearchHit[]
  partial: boolean // the search by meaning did not answer: names and words only
}

/**
 * The categories of documents (#73), in the order the app Documents shows
 * them: those of the core's `doccat.Categories` and of the workers.
 */
export type CategoryId = 'factures' | 'contrats' | 'impots' | 'sante' | 'banque' | 'travail' | 'administratif' | 'autre'

/** A document with its category, as the core's `httpapi.DocumentEntry` writes it. */
export interface DocumentEntry extends Entry {
  category: CategoryId // the member's choice, else the automatic one
  auto: CategoryId | null // the automatic one; null until the workers classify it
  corrected: boolean // the member chose the category
}

export interface DocumentCursor {
  at: string // modified_at of the last document, sent back as before_at
  path: string // sent back as before_path
}

/** GET /api/documents?category=…, the most recently modified first. */
export interface DocumentPage {
  items: DocumentEntry[]
  next: DocumentCursor | null
}

/** GET /api/documents/categories: every category, in order, and the documents not classified yet. */
export interface CategoriesSummary {
  categories: { id: CategoryId; count: number }[]
  pending: number
}

/** A file on the timeline, as the core's `httpapi.TimelineEntry` writes it. */
export interface TimelineEntry extends Entry {
  at: string // wall-clock date, "2024-07-14T18:03:22", no zone
  taken: boolean // at is when the photo was taken
  duration_ms?: number // a video's length, once the workers read it
}

export interface TimelineCursor {
  at: string
  path: string
}

/** GET /api/timeline. */
export interface TimelinePage {
  items: TimelineEntry[]
  next: TimelineCursor | null
}

/** GET /api/timeline/months. */
export interface MonthCount {
  month: string // "2026-11"
  count: number
}

/** GET /api/files/meta (docs/architecture/metadata.md). */
export interface MediaMeta {
  taken_at: string | null
  taken_offset: string | null
  latitude: number | null
  longitude: number | null
  make: string | null
  model: string | null
  lens: string | null
  width: number | null
  height: number | null
  duration_ms: number | null // a video's length
}

/** POST /api/login and POST /api/setup. */
export interface SignedIn {
  account: Account
}

/** An album, as GET /api/albums lists it (#39): it only refers to files. */
export interface Album {
  id: number
  name: string
  created_at: string
  count: number // live files in it
  cover: Entry | null // its newest file, for the thumbnail
}

/** The tasks of the analysis, as the core's `analysis.Kinds` names them (docs/architecture/tasks.md). */
export type AnalysisKind = 'thumb' | 'meta' | 'text' | 'ocr' | 'photovec' | 'textemb' | 'doccat'

/** Where a step stands: waiting (queued, or for the step before it), running, done, failed for good, skipped (nothing to do) or idle (not queued). */
export type AnalysisState = 'waiting' | 'running' | 'done' | 'failed' | 'skipped' | 'idle'

/** One step of the analysis of a file, as the core's `analysis.Step` writes it. */
export interface AnalysisStep {
  kind: AnalysisKind
  state: AnalysisState
  error?: string // the last error: why it failed, or why it is tried again
  attempts?: number
  max_attempts?: number
}

/** GET /api/analysis/file?path=…, as the core's `analysis.FileStatus` writes it (#82). `none`: no step applies to this type of file. */
export interface FileAnalysis {
  state: 'none' | 'pending' | 'ready' | 'failed'
  steps: AnalysisStep[]
}

/** GET /api/analysis: the account's files being analysed, and those whose analysis failed (#82). */
export interface AnalysisSummary {
  analysing: number
  failed: number
}

/** GET /api/me/smb-password (#91): whether the account has an SMB password, never the password itself. */
export interface SMBPasswordStatus {
  exists: boolean
  created_at: string | null
  last_used_at: string | null // the last time an SMB client signed in with it
}

/** POST /api/me/smb-password: a new SMB password, the only time it is shown. */
export interface SMBPasswordCreated extends SMBPasswordStatus {
  password: string
}
