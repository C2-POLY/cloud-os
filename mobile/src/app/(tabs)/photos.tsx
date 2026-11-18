import { SymbolView } from 'expo-symbols'
import { useCallback, useState } from 'react'
import { Pressable, StyleSheet, Text, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'
import type { Album, TimelineCursor } from '../../../../shared/api-types'
import { AlbumList } from '../../components/AlbumList'
import { PhotoGrid } from '../../components/PhotoGrid'
import { useApi } from '../../lib/session'
import { useTheme } from '../../lib/theme'

type View_ = 'all' | 'albums'

/** Every photo and video by month, or the albums and one album's photos (#39). */
export default function Photos() {
  const api = useApi()
  const t = useTheme()
  const [view, setView] = useState<View_>('all')
  const [album, setAlbum] = useState<Album | null>(null)

  const timeline = useCallback((cursor: TimelineCursor | null) => api.timeline(cursor), [api])
  const albumItems = useCallback((cursor: TimelineCursor | null) => api.albumItems(album?.id ?? 0, cursor), [api, album])

  if (album) {
    return (
      <SafeAreaView edges={['top']} style={{ flex: 1, backgroundColor: t.bg }}>
        <Pressable accessibilityRole="button" accessibilityLabel="Albums" onPress={() => setAlbum(null)} style={styles.back} hitSlop={12}>
          <SymbolView name={{ ios: 'chevron.left', android: 'chevron_left' }} tintColor={t.accent} size={18} />
          <Text style={{ color: t.accent, fontSize: 17 }}>Albums</Text>
        </Pressable>
        <Text style={[styles.large, { color: t.fg }]} numberOfLines={1}>
          {album.name}
        </Text>
        <PhotoGrid key={album.id} fetchPage={albumItems} empty="Cet album est vide." />
      </SafeAreaView>
    )
  }

  const segment = (v: View_, label: string) => (
    <Pressable
      accessibilityRole="tab"
      accessibilityState={{ selected: view === v }}
      onPress={() => setView(v)}
      style={[styles.segment, view === v && { backgroundColor: t.surface }]}
    >
      <Text style={{ color: t.fg, fontWeight: view === v ? '600' : '400' }}>{label}</Text>
    </Pressable>
  )
  return (
    <SafeAreaView edges={['top']} style={{ flex: 1, backgroundColor: t.bg }}>
      <Text style={[styles.large, { color: t.fg }]}>Photos</Text>
      <View style={[styles.segments, { backgroundColor: t.line }]} accessibilityRole="tablist">
        {segment('all', 'Tout')}
        {segment('albums', 'Albums')}
      </View>
      {view === 'all' ? <PhotoGrid fetchPage={timeline} empty="Aucune photo pour l’instant." /> : <AlbumList onOpen={setAlbum} />}
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  large: { fontSize: 34, fontWeight: '700', paddingHorizontal: 16, paddingTop: 8, paddingBottom: 6 },
  back: { flexDirection: 'row', alignItems: 'center', gap: 4, paddingHorizontal: 12, paddingTop: 6 },
  segments: { flexDirection: 'row', marginHorizontal: 16, marginBottom: 8, borderRadius: 9, padding: 2 },
  segment: { flex: 1, alignItems: 'center', paddingVertical: 6, borderRadius: 7 },
})
