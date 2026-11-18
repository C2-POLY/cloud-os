import { parentOf, type ContentHit, type Entry, type SnippetPart } from '../drive/files.ts'

/** The groups of the results, in the order they are shown. */
export const groups = ['Dossiers', 'Photos et vidéos', 'Documents', 'Dans le texte'] as const
export type Group = (typeof groups)[number]

/** The group of an entry found by its name. */
export function groupOf(e: Entry): Group {
  if (e.kind === 'dir') return 'Dossiers'
  if (e.mime.startsWith('image/') || e.mime.startsWith('video/')) return 'Photos et vidéos'
  return 'Documents'
}

/** A line of the palette: an entry, its group, and the extract of its text when the words are in it. */
export interface Row {
  entry: Entry
  group: Group
  snippet?: SnippetPart[]
}

/**
 * The lines of the palette, in the order shown: the entries found by name,
 * grouped, with the extract of those whose text holds the words too; then, in
 * « Dans le texte », the files found only by their text, in the core's order.
 */
export function rowsOf(names: Entry[], contents: ContentHit[]): Row[] {
  const extracts = new Map(contents.map((h) => [h.path, h.snippet]))
  const named: Row[] = names
    .map((e) => ({ entry: e, group: groupOf(e), snippet: extracts.get(e.path) }))
    .sort((a, b) => groups.indexOf(a.group) - groups.indexOf(b.group))
  const seen = new Set(names.map((e) => e.path))
  const inText: Row[] = contents.filter((h) => !seen.has(h.path)).map((h) => ({ entry: h, group: 'Dans le texte', snippet: h.snippet }))
  return [...named, ...inText]
}

/** The folder that shows an entry in Fichiers: itself for a folder, its parent for a file. */
export function folderOf(e: Entry): string {
  return e.kind === 'dir' ? e.path : parentOf(e.path)
}
