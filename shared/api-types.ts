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
