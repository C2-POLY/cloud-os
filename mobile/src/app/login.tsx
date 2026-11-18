import { Redirect } from 'expo-router'
import { useState } from 'react'
import { ActivityIndicator, KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, TextInput } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'
import { ApiError, normalizeBox } from '../lib/api'
import { useSession } from '../lib/session'
import { useTheme } from '../lib/theme'

/** Sign in to a box: its address on the home network, then the account. */
export default function Login() {
  const { session, signIn } = useSession()
  const t = useTheme()
  const [box, setBox] = useState(session.state === 'signed-out' ? session.box : '')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (session.state === 'signed-in') return <Redirect href="/files" />

  async function submit() {
    const address = normalizeBox(box)
    if (!address || !name || !password) {
      setError('Indique l’adresse de la box, ton identifiant et ton mot de passe.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await signIn(address, name.trim(), password)
    } catch (err) {
      setError(err instanceof ApiError && err.status === 401 ? 'Identifiant ou mot de passe incorrect.' : err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const input = [styles.input, { color: t.fg, backgroundColor: t.surface, borderColor: t.line }]
  return (
    <SafeAreaView style={[styles.screen, { backgroundColor: t.bg }]}>
      <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined} style={styles.form}>
        <Text style={[styles.title, { color: t.fg }]}>Cloud OS</Text>
        <Text style={[styles.lead, { color: t.muted }]}>Connecte-toi à la box de la maison.</Text>
        <Text style={[styles.label, { color: t.muted }]}>Adresse de la box</Text>
        <TextInput
          accessibilityLabel="Adresse de la box"
          style={input}
          value={box}
          onChangeText={setBox}
          placeholder="192.168.1.20:8480"
          placeholderTextColor={t.muted}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
        />
        <Text style={[styles.label, { color: t.muted }]}>Identifiant</Text>
        <TextInput accessibilityLabel="Identifiant" style={input} value={name} onChangeText={setName} autoCapitalize="none" autoCorrect={false} textContentType="username" />
        <Text style={[styles.label, { color: t.muted }]}>Mot de passe</Text>
        <TextInput accessibilityLabel="Mot de passe" style={input} value={password} onChangeText={setPassword} secureTextEntry textContentType="password" onSubmitEditing={submit} />
        {error && (
          <Text accessibilityRole="alert" style={[styles.error, { color: t.bad }]}>
            {error}
          </Text>
        )}
        <Pressable accessibilityRole="button" onPress={submit} disabled={busy} style={[styles.button, { backgroundColor: t.accent, opacity: busy ? 0.6 : 1 }]}>
          {busy ? <ActivityIndicator color="#fff" /> : <Text style={styles.buttonText}>Se connecter</Text>}
        </Pressable>
      </KeyboardAvoidingView>
    </SafeAreaView>
  )
}

const styles = StyleSheet.create({
  screen: { flex: 1 },
  form: { flex: 1, justifyContent: 'center', paddingHorizontal: 24, gap: 6 },
  title: { fontSize: 34, fontWeight: '700', marginBottom: 2 },
  lead: { fontSize: 16, marginBottom: 18 },
  label: { fontSize: 13, marginTop: 10 },
  input: { height: 46, borderWidth: StyleSheet.hairlineWidth, borderRadius: 10, paddingHorizontal: 12, fontSize: 16 },
  error: { marginTop: 12, fontSize: 14 },
  button: { marginTop: 22, height: 48, borderRadius: 12, alignItems: 'center', justifyContent: 'center' },
  buttonText: { color: '#fff', fontSize: 17, fontWeight: '600' },
})
