import { act, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Account } from '../api.ts'
import { Desktop } from '../desktop/Desktop.tsx'
import { Viewer } from '../desktop/Viewer.tsx'
import type { TimelineEntry } from '../desktop/timeline.ts'
import { DocViewer } from '../preview/DocViewer.tsx'
import type { Entry } from '../drive/files.ts'
import { FILE_STORED, failedLabel, summaryLabel, type AnalysisStep, type AnalysisSummary, type FileAnalysis } from './analysis.ts'

const enzo: Account = { name: 'enzo', display_name: 'Enzo', role: 'admin', created_at: '2026-11-17T10:00:00Z' }

/** Answers fetch from "METHOD /path" → a list of replies; the last one repeats, like a state that stays. */
function script(table: Record<string, unknown[]>) {
  const calls: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init?: RequestInit) => {
      const key = `${init?.method ?? 'GET'} ${path}`
      calls.push(key)
      const replies = table[key]
      if (!replies) {
        if (path.startsWith('/api/files/content')) return new Response('# Notes', { status: 200 })
        return new Response(null, { status: 404 })
      }
      const body = replies.length > 1 ? replies.shift() : replies[0]
      return new Response(JSON.stringify(body), { status: 200 })
    }),
  )
  return calls
}

const health = { version: '0.1.0', db: 'ok', migration: 24, tasks: { queued: 0, running: 0, failed: 0 } }
const step = (kind: AnalysisStep['kind'], state: AnalysisStep['state'], more: Partial<AnalysisStep> = {}): AnalysisStep => ({ kind, state, ...more })
const analysis = (state: FileAnalysis['state'], steps: AnalysisStep[]): FileAnalysis => ({ state, steps })

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  window.localStorage.clear()
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const tick = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)))

describe('labels', () => {
  it('says how many files are being analysed, and how many failed', () => {
    const s = (analysing: number, failed: number): AnalysisSummary => ({ analysing, failed })
    expect(summaryLabel(s(0, 0))).toBe('Analyse à jour')
    expect(summaryLabel(s(1, 0))).toBe('Analyse d’un fichier…')
    expect(summaryLabel(s(3, 0))).toBe('Analyse de 3 fichiers…')
    expect(failedLabel(s(0, 0))).toBe('')
    expect(failedLabel(s(0, 1))).toBe('1 fichier non analysé')
    expect(failedLabel(s(0, 2))).toBe('2 fichiers non analysés')
  })
})

describe('the menu bar', () => {
  it('shows the analysis, and follows it without a reload', async () => {
    script({
      'GET /api/health': [health],
      'GET /api/analysis': [{ analysing: 3, failed: 0 }, { analysing: 1, failed: 0 }, { analysing: 0, failed: 0 }],
    })
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    const bar = screen.getByRole('banner')
    expect(await within(bar).findByText('Analyse de 3 fichiers…')).toBeInTheDocument()

    await tick(2_600)
    expect(await within(bar).findByText('Analyse d’un fichier…')).toBeInTheDocument()
    await tick(2_600)
    expect(await within(bar).findByText('Analyse à jour')).toBeInTheDocument()
  })

  it('polls fast while something is pending and slowly otherwise', async () => {
    const calls = script({ 'GET /api/health': [health], 'GET /api/analysis': [{ analysing: 2, failed: 0 }, { analysing: 0, failed: 0 }] })
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    const count = () => calls.filter((c) => c === 'GET /api/analysis').length
    await tick(100)
    expect(count()).toBe(1)
    await tick(2_600) // pending: read again soon
    expect(count()).toBe(2)
    await tick(10_000) // nothing pending: not yet
    expect(count()).toBe(2)
    await tick(6_000)
    expect(count()).toBe(3)
  })

  it('reads at once when a file has just been stored, without waiting for the slow tick', async () => {
    const calls = script({ 'GET /api/health': [health], 'GET /api/analysis': [{ analysing: 0, failed: 0 }, { analysing: 1, failed: 0 }] })
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    const bar = screen.getByRole('banner')
    expect(await within(bar).findByText('Analyse à jour')).toBeInTheDocument()
    act(() => void window.dispatchEvent(new Event(FILE_STORED)))
    expect(await within(bar).findByText('Analyse d’un fichier…')).toBeInTheDocument()
    expect(calls.filter((c) => c === 'GET /api/analysis')).toHaveLength(2)
  })

  it('says how many files could not be analysed', async () => {
    script({ 'GET /api/health': [health], 'GET /api/analysis': [{ analysing: 0, failed: 2 }] })
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    expect(await screen.findByText('2 fichiers non analysés')).toBeInTheDocument()
    expect(screen.getByText('Analyse à jour')).toBeInTheDocument()
  })
})

describe('the steps of a document', () => {
  const notes: Entry = { path: 'Documents/notes.md', name: 'notes.md', kind: 'file', size: 7, mime: 'text/markdown', sha256: 'ab'.repeat(32), modified_at: '2026-11-17T10:00:00Z' }
  const key = 'GET /api/analysis/file?path=Documents%2Fnotes.md'

  it('goes from waiting to done by itself', async () => {
    script({
      [key]: [
        analysis('pending', [step('text', 'running'), step('textemb', 'waiting'), step('doccat', 'waiting')]),
        analysis('pending', [step('text', 'done'), step('textemb', 'running'), step('doccat', 'waiting')]),
        analysis('ready', [step('text', 'done'), step('textemb', 'done'), step('doccat', 'done')]),
      ],
    })
    render(<DocViewer entry={notes} onClose={() => {}} />)
    const panel = await screen.findByRole('region', { name: 'Analyse du fichier' })
    expect(within(panel).getByText('Analyse en cours…')).toBeInTheDocument()
    const line = (name: string) => within(panel).getByText(name).closest('li')!
    expect(line('Texte lu')).toHaveTextContent('en cours')
    expect(line('Sens du texte indexé')).toHaveTextContent('en attente')

    await tick(2_600)
    expect(await within(panel).findByText('Texte lu')).toBeInTheDocument()
    expect(line('Texte lu')).toHaveTextContent('fait')
    expect(line('Sens du texte indexé')).toHaveTextContent('en cours')

    await tick(2_600)
    expect(await within(panel).findByText('Analyse terminée')).toBeInTheDocument()
    expect(line('Catégorie du document')).toHaveTextContent('fait')
  })

  it('says it failed, and why', async () => {
    script({
      [key]: [analysis('failed', [step('text', 'failed', { error: 'PdfReadError: EOF marker not found', attempts: 5, max_attempts: 5 }), step('textemb', 'idle'), step('doccat', 'idle')])],
    })
    render(<DocViewer entry={notes} onClose={() => {}} />)
    const panel = await screen.findByRole('region', { name: 'Analyse du fichier' })
    expect(within(panel).getByRole('alert')).toHaveTextContent('L’analyse a échoué : ce fichier ne sera pas retrouvé par son contenu.')
    const failed = within(panel).getByText('Texte lu').closest('li')!
    expect(failed).toHaveTextContent('échec')
    expect(failed).toHaveTextContent('Raison : PdfReadError: EOF marker not found')
    expect(within(panel).getByText('Sens du texte indexé').closest('li')).toHaveTextContent('pas lancé')
  })

  it('says a step is tried again, with its last error', async () => {
    script({ [key]: [analysis('pending', [step('text', 'waiting', { error: 'RuntimeError: no copy of the content could be read', attempts: 2, max_attempts: 5 })])] })
    render(<DocViewer entry={notes} onClose={() => {}} />)
    const panel = await screen.findByRole('region', { name: 'Analyse du fichier' })
    const line = within(panel).getByText('Texte lu').closest('li')!
    expect(line).toHaveTextContent('nouvel essai à venir (2/5)')
    expect(line).toHaveTextContent('Dernière erreur : RuntimeError')
  })

  it('shows nothing for a file that is not analysed', async () => {
    const calls = script({ [key]: [analysis('none', [])] })
    render(<DocViewer entry={notes} onClose={() => {}} />)
    await tick(100)
    expect(screen.queryByRole('region', { name: 'Analyse du fichier' })).toBeNull()
    await tick(60_000) // and it does not ask again
    expect(calls.filter((c) => c === key)).toHaveLength(1)
  })
})

describe('the steps of a photo', () => {
  const photo: TimelineEntry = { path: 'Photos/plage.jpg', name: 'plage.jpg', kind: 'file', size: 2048, mime: 'image/jpeg', sha256: 'ab'.repeat(32), modified_at: '2026-11-17T10:00:00Z', at: '2024-07-14T18:03:22', taken: true }

  it('lists the photo steps: thumbnail, date and place, text, what it shows', async () => {
    script({
      'GET /api/analysis/file?path=Photos%2Fplage.jpg': [
        analysis('pending', [step('thumb', 'done'), step('meta', 'done'), step('ocr', 'running'), step('photovec', 'waiting')]),
      ],
    })
    render(<Viewer items={[photo]} index={0} onIndex={() => {}} onClose={() => {}} onNearEnd={() => {}} />)
    const panel = await screen.findByRole('region', { name: 'Analyse du fichier' })
    expect(panel).toHaveClass('dark')
    expect(within(panel).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Miniaturefait',
      'Date, lieu et appareilfait',
      'Texte reconnu (OCR)en cours',
      'Contenu de l’image indexéen attente',
    ])
  })

  it('says a photo failed', async () => {
    script({
      'GET /api/analysis/file?path=Photos%2Fplage.jpg': [
        analysis('failed', [step('thumb', 'done'), step('meta', 'done'), step('ocr', 'done'), step('photovec', 'failed', { error: 'Permanent: image too large', attempts: 1, max_attempts: 5 })]),
      ],
    })
    render(<Viewer items={[photo]} index={0} onIndex={() => {}} onClose={() => {}} onNearEnd={() => {}} />)
    const panel = await screen.findByRole('region', { name: 'Analyse du fichier' })
    expect(within(panel).getByRole('alert')).toBeInTheDocument()
    expect(within(panel).getByText('Contenu de l’image indexé').closest('li')).toHaveTextContent('Raison : Permanent: image too large')
  })
})
