import { SymbolView } from 'expo-symbols'
import { Redirect, Tabs } from 'expo-router'
import { useSession } from '../../lib/session'
import { useTheme } from '../../lib/theme'

/** Signed in: Fichiers and Photos. */
export default function TabsLayout() {
  const { session } = useSession()
  const t = useTheme()
  if (session.state !== 'signed-in') return <Redirect href="/login" />
  return (
    <Tabs
      screenOptions={{
        headerShown: false,
        tabBarActiveTintColor: t.accent,
        tabBarInactiveTintColor: t.muted,
        tabBarStyle: { backgroundColor: t.bg, borderTopColor: t.line },
      }}
    >
      <Tabs.Screen
        name="files"
        options={{
          title: 'Fichiers',
          tabBarAccessibilityLabel: 'Fichiers',
          tabBarIcon: ({ color, size }) => <SymbolView name={{ ios: 'folder.fill', android: 'folder' }} tintColor={color} size={size} />,
        }}
      />
      <Tabs.Screen
        name="photos"
        options={{
          title: 'Photos',
          tabBarAccessibilityLabel: 'Photos',
          tabBarIcon: ({ color, size }) => <SymbolView name={{ ios: 'photo.on.rectangle', android: 'photo_library' }} tintColor={color} size={size} />,
        }}
      />
      <Tabs.Screen
        name="backup"
        options={{
          title: 'Sauvegarde',
          tabBarAccessibilityLabel: 'Sauvegarde',
          tabBarIcon: ({ color, size }) => <SymbolView name={{ ios: 'arrow.triangle.2.circlepath.icloud', android: 'backup' }} tintColor={color} size={size} />,
        }}
      />
    </Tabs>
  )
}
