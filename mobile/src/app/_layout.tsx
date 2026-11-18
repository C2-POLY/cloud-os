import { Stack } from 'expo-router'
import { StatusBar } from 'expo-status-bar'
import '../backup/task' // defines the background backup task at startup
import { SessionProvider } from '../lib/session'

export default function RootLayout() {
  return (
    <SessionProvider>
      <StatusBar style="auto" />
      <Stack screenOptions={{ headerShown: false }} />
    </SessionProvider>
  )
}
