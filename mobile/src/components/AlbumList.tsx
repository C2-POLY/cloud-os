import { Image } from 'expo-image'
import { SymbolView } from 'expo-symbols'
import { useCallback, useEffect, useState } from 'react'
import { ActivityIndicator, FlatList, Pressable, RefreshControl, StyleSheet, Text, View } from 'react-native'
import type { Album } from '../../../shared/api-types'
import { useApi } from '../lib/session'
import { useTheme } from '../lib/theme'

const count = (n: number) => `${n} élément${n > 1 ? 's' : ''}`

/** The account's albums, with their cover and count; a tap opens one. */
export function AlbumList({ onOpen }: { onOpen: (album: Album) => void }) {
  const api = useApi()
  const t = useTheme()
  const [albums, setAlbums] = useState<Album[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const load = useCallback(
    () =>
      api
        .albums()
        .then((a) => {
          setAlbums(a)
          setError(null)
        })
        .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err))),
    [api],
  )
  useEffect(() => {
    void load()
  }, [load])

  if (!albums && !error) return <ActivityIndicator style={{ marginTop: 40 }} />
  return (
    <FlatList
      data={albums ?? []}
      keyExtractor={(a) => String(a.id)}
      contentContainerStyle={{ paddingBottom: 24 }}
      refreshControl={
        <RefreshControl
          refreshing={refreshing}
          onRefresh={() => {
            setRefreshing(true)
            void load().finally(() => setRefreshing(false))
          }}
        />
      }
      ListHeaderComponent={error ? <Text style={[styles.note, { color: t.bad }]}>{error}</Text> : undefined}
      ListEmptyComponent={error ? undefined : <Text style={[styles.note, { color: t.muted }]}>Aucun album. Crée-en un depuis Photos sur le web.</Text>}
      renderItem={({ item: a }) => {
        const cover = a.cover ? api.thumbURL(a.cover) : null
        return (
          <Pressable accessibilityRole="button" accessibilityLabel={`${a.name}, ${count(a.count)}`} onPress={() => onOpen(a)} style={styles.row}>
            {cover ? (
              <Image source={{ uri: cover, headers: api.headers }} style={[styles.cover, { backgroundColor: t.line }]} contentFit="cover" />
            ) : (
              <View style={[styles.cover, styles.empty, { backgroundColor: t.line }]}>
                <SymbolView name={{ ios: 'photo.on.rectangle', android: 'photo_library' }} tintColor={t.muted} size={24} />
              </View>
            )}
            <View style={styles.text}>
              <Text style={[styles.name, { color: t.fg }]} numberOfLines={1}>
                {a.name}
              </Text>
              <Text style={{ color: t.muted, fontSize: 14 }}>{count(a.count)}</Text>
            </View>
            <SymbolView name={{ ios: 'chevron.right', android: 'chevron_right' }} tintColor={t.muted} size={14} />
          </Pressable>
        )
      }}
    />
  )
}

const styles = StyleSheet.create({
  row: { flexDirection: 'row', alignItems: 'center', gap: 14, paddingHorizontal: 16, paddingVertical: 8 },
  cover: { width: 64, height: 64, borderRadius: 8 },
  empty: { alignItems: 'center', justifyContent: 'center' },
  text: { flex: 1, gap: 2 },
  name: { fontSize: 17, fontWeight: '600' },
  note: { padding: 20, fontSize: 15 },
})
