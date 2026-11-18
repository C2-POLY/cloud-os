import { Image } from 'expo-image'
import { Directory, File, Paths } from 'expo-file-system'
import { router } from 'expo-router'
import * as Sharing from 'expo-sharing'
import { SymbolView } from 'expo-symbols'
import { useCallback, useEffect, useState } from 'react'
import { ActivityIndicator, Alert, FlatList, Pressable, RefreshControl, StyleSheet, Text, View } from 'react-native'
import type { Entry } from '../../../shared/api-types'
import { ApiError } from '../lib/api'
import { formatSize, sortEntries } from '../lib/format'
import { useApi } from '../lib/session'
import { useTheme } from '../lib/theme'

const thumbnailed = new Set(['image/jpeg', 'image/png', 'image/webp', 'image/heic', 'image/heif'])

/** One folder of the drive: its folders, then its files; a file opens in the system's preview. */
export function Folder({ path }: { path: string }) {
  const api = useApi()
  const t = useTheme()
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const [opening, setOpening] = useState<string | null>(null)

  const load = useCallback(() => {
    return api
      .list(path)
      .then((list) => {
        setEntries(sortEntries(list))
        setError(null)
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
  }, [api, path])

  useEffect(() => {
    void load()
  }, [load])

  async function open(e: Entry) {
    if (e.kind === 'dir') {
      router.push(`/files/${e.path.split('/').map(encodeURIComponent).join('/')}`)
      return
    }
    setOpening(e.path)
    try {
      // A fresh copy in the cache, named like the file, so the preview shows the right name and type.
      const dir = new Directory(Paths.cache, 'cloudos', e.sha256?.slice(0, 16) ?? 'tmp')
      if (!dir.exists) dir.create({ intermediates: true })
      const file = await File.downloadFileAsync(api.contentURL(e.path), new File(dir, e.name), { headers: api.headers, idempotent: true })
      if (await File.canPreview(file)) await File.preview(file, { title: e.name })
      else await Sharing.shareAsync(file.uri)
    } catch (err) {
      Alert.alert('Impossible d’ouvrir le fichier', err instanceof ApiError || err instanceof Error ? err.message : String(err))
    } finally {
      setOpening(null)
    }
  }

  if (entries === null && !error) return <ActivityIndicator style={{ marginTop: 40 }} />
  return (
    <FlatList
      contentInsetAdjustmentBehavior="automatic"
      data={entries ?? []}
      keyExtractor={(e) => e.path}
      refreshControl={
        <RefreshControl
          refreshing={refreshing}
          onRefresh={async () => {
            setRefreshing(true)
            await load()
            setRefreshing(false)
          }}
        />
      }
      ListHeaderComponent={error ? <Text style={[styles.note, { color: t.bad }]}>{error}</Text> : undefined}
      ListEmptyComponent={!error ? <Text style={[styles.note, { color: t.muted }]}>Ce dossier est vide.</Text> : undefined}
      ItemSeparatorComponent={() => <View style={[styles.sep, { backgroundColor: t.line }]} />}
      renderItem={({ item: e }) => {
        const thumb = e.kind === 'file' && thumbnailed.has(e.mime) ? api.thumbURL(e) : null
        return (
          <Pressable accessibilityRole="button" accessibilityLabel={e.name} onPress={() => open(e)} style={({ pressed }) => [styles.row, pressed && { backgroundColor: t.line }]}>
            {thumb ? (
              <Image source={{ uri: thumb, headers: api.headers }} style={styles.thumb} contentFit="cover" />
            ) : (
              <View style={styles.icon}>
                <SymbolView name={e.kind === 'dir' ? 'folder.fill' : e.mime.startsWith('image/') ? 'photo' : e.mime.startsWith('video/') ? 'video' : 'doc'} tintColor={e.kind === 'dir' ? t.folder : t.muted} size={26} />
              </View>
            )}
            <View style={styles.text}>
              <Text style={[styles.name, { color: t.fg }]} numberOfLines={2}>
                {e.name}
              </Text>
              {e.kind === 'file' && <Text style={[styles.meta, { color: t.muted }]}>{formatSize(e.size)}</Text>}
            </View>
            {opening === e.path ? <ActivityIndicator /> : e.kind === 'dir' ? <SymbolView name="chevron.right" tintColor={t.muted} size={14} /> : null}
          </Pressable>
        )
      }}
    />
  )
}

const styles = StyleSheet.create({
  row: { flexDirection: 'row', alignItems: 'center', gap: 12, paddingHorizontal: 16, paddingVertical: 10, minHeight: 56 },
  thumb: { width: 40, height: 40, borderRadius: 6 },
  icon: { width: 40, height: 40, alignItems: 'center', justifyContent: 'center' },
  text: { flex: 1 },
  name: { fontSize: 16 },
  meta: { fontSize: 13, marginTop: 2 },
  sep: { height: StyleSheet.hairlineWidth, marginLeft: 68 },
  note: { padding: 20, fontSize: 15 },
})
