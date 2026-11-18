import { Image } from 'expo-image'
import { useEffect, useState } from 'react'
import { FlatList, Modal, Pressable, StyleSheet, Text, useWindowDimensions, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'
import type { MediaMeta, TimelineEntry } from '../../../shared/api-types'
import { formatSize, parentOf, wallClock } from '../lib/format'
import { useApi } from '../lib/session'

// What iOS draws itself, HEIC included; anything else shows its thumbnail.
const drawable = new Set(['image/jpeg', 'image/png', 'image/webp', 'image/gif', 'image/heic', 'image/heif'])

/** The photos full screen: swipe between them, with the metadata of the one shown. */
export function Viewer({
  items,
  index,
  onIndex,
  onClose,
  onNearEnd,
}: {
  items: TimelineEntry[]
  index: number
  onIndex: (i: number) => void
  onClose: () => void
  onNearEnd: () => void
}) {
  const api = useApi()
  const { width, height } = useWindowDimensions()
  const item = items[index]
  const [meta, setMeta] = useState<{ path: string; value: MediaMeta | null } | null>(null)

  useEffect(() => {
    let current = true
    api
      .meta(item.path)
      .then((value) => current && setMeta({ path: item.path, value }))
      .catch(() => current && setMeta({ path: item.path, value: null }))
    if (index >= items.length - 4) onNearEnd()
    return () => {
      current = false
    }
  }, [api, item.path, index, items.length, onNearEnd])

  const m = meta?.path === item.path ? meta.value : null
  const camera = [m?.make, m?.model].filter(Boolean).join(' ')

  return (
    <Modal visible animationType="fade" onRequestClose={onClose} supportedOrientations={['portrait', 'landscape']}>
      <View style={styles.screen} accessibilityViewIsModal>
        <FlatList
          data={items}
          horizontal
          pagingEnabled
          initialScrollIndex={index}
          getItemLayout={(_, i) => ({ length: width, offset: width * i, index: i })}
          keyExtractor={(e) => e.path}
          showsHorizontalScrollIndicator={false}
          onMomentumScrollEnd={(ev) => onIndex(Math.round(ev.nativeEvent.contentOffset.x / width))}
          renderItem={({ item: e }) => (
            <Image
              accessibilityLabel={e.name}
              source={{ uri: drawable.has(e.mime) ? api.contentURL(e.path) : (api.thumbURL(e) ?? undefined), headers: api.headers }}
              placeholder={{ uri: api.thumbURL(e) ?? undefined, headers: api.headers }}
              style={{ width, height: height * 0.68 }}
              contentFit="contain"
            />
          )}
        />
        <SafeAreaView edges={['bottom']} style={styles.info}>
          <Text style={styles.name} numberOfLines={1}>
            {item.name}
          </Text>
          <Text style={styles.line}>
            {item.taken ? 'Prise le ' : 'Modifiée le '}
            {wallClock(item.at)}
          </Text>
          {camera ? <Text style={styles.line}>{camera}</Text> : null}
          {m?.width && m?.height ? (
            <Text style={styles.line}>
              {m.width} × {m.height} · {formatSize(item.size)}
            </Text>
          ) : (
            <Text style={styles.line}>{formatSize(item.size)}</Text>
          )}
          {m?.latitude != null && m?.longitude != null ? (
            <Text style={styles.line}>
              {m.latitude.toFixed(5)}, {m.longitude.toFixed(5)}
            </Text>
          ) : null}
          <Text style={styles.dim}>
            {parentOf(item.path) || 'Mon drive'} · {index + 1} / {items.length}
          </Text>
        </SafeAreaView>
        <Pressable accessibilityRole="button" accessibilityLabel="Fermer" onPress={onClose} style={styles.close} hitSlop={12}>
          <Text style={styles.closeText}>Fermer</Text>
        </Pressable>
      </View>
    </Modal>
  )
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: '#000', justifyContent: 'center' },
  info: { paddingHorizontal: 20, paddingTop: 12, gap: 3 },
  name: { color: '#fff', fontSize: 17, fontWeight: '600' },
  line: { color: 'rgba(255,255,255,0.85)', fontSize: 14 },
  dim: { color: 'rgba(255,255,255,0.55)', fontSize: 13, marginTop: 4 },
  close: { position: 'absolute', top: 58, right: 18, paddingHorizontal: 14, paddingVertical: 8, borderRadius: 18, backgroundColor: 'rgba(255,255,255,0.18)' },
  closeText: { color: '#fff', fontSize: 15, fontWeight: '600' },
})
