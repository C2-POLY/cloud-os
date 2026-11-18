import { Redirect, Tabs } from 'expo-router'
import { useSession } from '../../lib/session'
import { useTheme } from '../../lib/theme'

/** Signed in: Fichiers and Photos. */
export default function TabsLayout() {
  const { session } = useSession()
  const t = useTheme()
  if (session.state !== 'signed-in') return <Redirect href="/login" />
  return (
    <Tabs screenOptions={{ headerShown: false, tabBarActiveTintColor: t.accent }}>
      <Tabs.Screen name="files" options={{ title: 'Fichiers', tabBarAccessibilityLabel: 'Fichiers' }} />
      <Tabs.Screen name="photos" options={{ title: 'Photos', tabBarAccessibilityLabel: 'Photos' }} />
    </Tabs>
  )
}
