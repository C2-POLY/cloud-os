import { Image } from 'expo-image'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ActivityIndicator, Pressable, SectionList, StyleSheet, Text, useWindowDimensions, View } from 'react-native'
import type { TimelineCursor, TimelineEntry, TimelinePage } from '../../../shared/api-types'
import { byMonth, monthLabel } from '../lib/format'
import { useApi } from '../lib/session'
import { useTheme } from '../lib/theme'
import { Viewer } from './Viewer'

const columns = 4
const gap = 2

/** Photos and videos, newest first, in a grid by month, a page at a time; a tap opens the viewer. */
export function PhotoGrid({ fetchPage, empty }: { fetchPage: (cursor: TimelineCursor | null) => Promise<TimelinePage>; empty: string }) {
  const api = useApi()
  const t = useTheme()
  const { width } = useWindowDimensions()
  const [items, setItems] = useState<TimelineEntry[]>([])
  const [next, setNext] = useState<TimelineCursor | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [viewing, setViewing] = useState<number | null>(null)
  const busy = useRef(false)

  const load = useCallback(
    (cursor: TimelineCursor | null) => {
      if (busy.current) return
      busy.current = true
      fetchPage(cursor)
        .then((page) => {
          setItems((prev) => (cursor ? [...prev, ...page.items] : page.items))
          setNext(page.next)
          setError(null)
        })
        .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
        .finally(() => {
          busy.current = false
          setLoading(false)
        })
    },
    [fetchPage],
  )

  useEffect(() => load(null), [load])

  const size = Math.floor((width - gap * (columns - 1)) / columns)
  // A SectionList of rows of `columns` tiles: one section per month.
  const sections = useMemo(
    () =>
      byMonth(items).map((m) => {
        const rows: TimelineEntry[][] = []
        for (let i = 0; i < m.items.length; i += columns) rows.push(m.items.slice(i, i + columns))
        return { title: monthLabel(m.month), key: m.month, data: rows }
      }),
    [items],
  )
  const index = useMemo(() => new Map(items.map((it, i) => [it.path, i])), [items])

  return (
    <>
      {loading && <ActivityIndicator style={{ marginTop: 40 }} />}
      {error && <Text style={[styles.note, { color: t.bad }]}>{error}</Text>}
      {!loading && !error && items.length === 0 && <Text style={[styles.note, { color: t.muted }]}>{empty}</Text>}
      <SectionList
        sections={sections}
        keyExtractor={(row) => row[0].path}
        stickySectionHeadersEnabled
        onEndReachedThreshold={1.5}
        onEndReached={() => next && load(next)}
        renderSectionHeader={({ section }) => (
          <Text style={[styles.month, { color: t.fg, backgroundColor: t.bg }]} accessibilityRole="header">
            {section.title}
          </Text>
        )}
        renderItem={({ item: row }) => (
          <View style={[styles.row, { gap, marginBottom: gap }]}>
            {row.map((e) => (
              <Pressable key={e.path} accessibilityRole="button" accessibilityLabel={e.name} onPress={() => setViewing(index.get(e.path) ?? 0)}>
                <Image source={{ uri: api.thumbURL(e) ?? undefined, headers: api.headers }} style={{ width: size, height: size, backgroundColor: t.line }} contentFit="cover" transition={120} />
              </Pressable>
            ))}
          </View>
        )}
      />
      {viewing !== null && items[viewing] && <Viewer items={items} index={viewing} onIndex={setViewing} onClose={() => setViewing(null)} onNearEnd={() => next && load(next)} />}
    </>
  )
}

const styles = StyleSheet.create({
  month: { fontSize: 20, fontWeight: '600', paddingHorizontal: 16, paddingVertical: 10 },
  row: { flexDirection: 'row' },
  note: { padding: 20, fontSize: 15 },
})
