import { Stack } from 'expo-router'
import { Pressable, Text } from 'react-native'
import { useSession } from '../../../lib/session'
import { useTheme } from '../../../lib/theme'

/** Folders stack on each other, with the native back gesture. */
export default function FilesLayout() {
  const { signOut } = useSession()
  const t = useTheme()
  return (
    <Stack
      screenOptions={{
        headerLargeTitle: true,
        headerTintColor: t.accent,
        // No headerStyle background: on iOS 26 it hides the large title.
        headerTitleStyle: { color: t.fg },
        headerLargeTitleStyle: { color: t.fg },
        contentStyle: { backgroundColor: t.bg },
      }}
    >
      <Stack.Screen
        name="index"
        options={{
          title: 'Mon drive',
          headerRight: () => (
            <Pressable accessibilityRole="button" onPress={signOut} hitSlop={12}>
              <Text style={{ color: t.accent, fontSize: 16 }}>Déconnexion</Text>
            </Pressable>
          ),
        }}
      />
      <Stack.Screen name="[...path]" options={{ title: '' }} />
    </Stack>
  )
}
