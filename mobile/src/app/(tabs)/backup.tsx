import { useFocusEffect } from 'expo-router'
import { useCallback, useEffect, useState } from 'react'
import { ActivityIndicator, Pressable, ScrollView, StyleSheet, Switch, Text, TextInput, View } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'
import { backupEnabled, backupStatus, phoneFolder, setBackupEnabled, setPhoneFolder } from '../../backup/phone'
import type { Summary } from '../../backup/run'
import { askPhotoAccess, backupNow, scheduleBackup } from '../../backup/task'
import { useTheme } from '../../lib/theme'

type Status = Awaited<ReturnType<typeof backupStatus>>

const when = new Intl.DateTimeFormat('fr-FR', { weekday: 'long', day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit' })

/** The camera roll's backup to the box: on or off, the phone's folder, what is left, the last success. */
export default function Backup() {
  const t = useTheme()
  const [enabled, setEnabled] = useState(false)
  const [folder, setFolder] = useState('')
  const [status, setStatus] = useState<Status | null>(null)
  const [progress, setProgress] = useState<Summary | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      setStatus(await backupStatus())
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  useEffect(() => {
    void (async () => {
      setEnabled(await backupEnabled())
      setFolder(await phoneFolder())
    })()
  }, [])
  useFocusEffect(
    useCallback(() => {
      void refresh()
    }, [refresh]),
  )

  const run = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      const s = await backupNow(Infinity, setProgress)
      if (s.error) setError(s.error)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
      setProgress(null)
      await refresh()
    }
  }, [refresh])

  const toggle = useCallback(
    async (on: boolean) => {
      setError(null)
      if (on && !(await askPhotoAccess())) {
        setError("Autorise l'accès aux photos dans Réglages pour sauvegarder.")
        return
      }
      setEnabled(on)
      await setBackupEnabled(on)
      await scheduleBackup(on)
      if (on) await run()
    },
    [run],
  )

  const remaining = progress ? progress.remaining : status?.remaining
  return (
    <SafeAreaView edges={['top']} style={{ flex: 1, backgroundColor: t.bg }}>
      <ScrollView contentContainerStyle={styles.page}>
        <Text style={[styles.large, { color: t.fg }]} accessibilityRole="header">
          Sauvegarde
        </Text>

        <View style={[styles.card, { backgroundColor: t.surface }]}>
          <View style={styles.row}>
            <Text style={[styles.label, { color: t.fg }]}>Sauvegarde automatique</Text>
            <Switch value={enabled} onValueChange={toggle} accessibilityLabel="Sauvegarde automatique" />
          </View>
          <View style={[styles.sep, { backgroundColor: t.line }]} />
          <Text style={[styles.small, { color: t.muted }]}>Dossier sur la box</Text>
          <View style={styles.folder}>
            <Text style={[styles.label, { color: t.muted }]}>Photos/</Text>
            <TextInput
              value={folder}
              onChangeText={setFolder}
              onEndEditing={() => void setPhoneFolder(folder).then(phoneFolder).then(setFolder)}
              style={[styles.label, styles.input, { color: t.fg }]}
              accessibilityLabel="Nom du dossier de ce téléphone"
              autoCorrect={false}
            />
          </View>
        </View>

        <View style={[styles.card, { backgroundColor: t.surface }]}>
          <View style={styles.row}>
            <Text style={[styles.label, { color: t.fg }]}>Reste à envoyer</Text>
            <Text style={[styles.label, { color: t.fg }]}>{remaining == null ? '—' : remaining.toLocaleString('fr-FR')}</Text>
          </View>
          <View style={[styles.sep, { backgroundColor: t.line }]} />
          <View style={styles.row}>
            <Text style={[styles.label, { color: t.fg }]}>Dernière sauvegarde complète</Text>
          </View>
          <Text style={[styles.small, { color: t.muted }]}>{status?.lastSuccess ? when.format(new Date(status.lastSuccess)) : 'Jamais'}</Text>
          {progress && (
            <Text style={[styles.small, { color: t.muted }]}>
              {progress.sent} envoyée{progress.sent > 1 ? 's' : ''}, {progress.alreadyThere} déjà sur la box
            </Text>
          )}
        </View>

        {(error ?? status?.lastError) && <Text style={[styles.note, { color: t.bad }]}>{error ?? status?.lastError}</Text>}

        <Pressable
          accessibilityRole="button"
          disabled={busy}
          onPress={() => void run()}
          style={[styles.button, { backgroundColor: t.accent, opacity: busy ? 0.6 : 1 }]}
        >
          {busy ? <ActivityIndicator color="#fff" /> : <Text style={styles.buttonText}>Sauvegarder maintenant</Text>}
        </Pressable>

        <Text style={[styles.note, { color: t.muted }]}>
          Les photos et vidéos du téléphone partent vers la box sans jamais être supprimées ni modifiées. Ce qui est déjà sur la box n’est pas renvoyé. En arrière-plan, le système décide quand l’app peut avancer : une première sauvegarde va plus vite app ouverte, à la maison.
        </Text>
      </ScrollView>
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  page: { paddingBottom: 32 },
  large: { fontSize: 34, fontWeight: '700', paddingHorizontal: 16, paddingTop: 8, paddingBottom: 12 },
  card: { marginHorizontal: 16, marginBottom: 16, borderRadius: 12, paddingHorizontal: 16, paddingVertical: 6 },
  row: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', minHeight: 44 },
  sep: { height: StyleSheet.hairlineWidth, marginVertical: 4 },
  label: { fontSize: 17 },
  small: { fontSize: 14, paddingBottom: 8 },
  folder: { flexDirection: 'row', alignItems: 'center', minHeight: 40 },
  input: { flex: 1, paddingVertical: 6 },
  note: { paddingHorizontal: 20, paddingBottom: 16, fontSize: 14, lineHeight: 20 },
  button: { marginHorizontal: 16, marginBottom: 16, height: 50, borderRadius: 12, alignItems: 'center', justifyContent: 'center' },
  buttonText: { color: '#fff', fontSize: 17, fontWeight: '600' },
})
