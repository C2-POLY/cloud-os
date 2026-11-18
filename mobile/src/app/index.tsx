import { Redirect } from 'expo-router'
import { ActivityIndicator, View } from 'react-native'
import { useSession } from '../lib/session'
import { useTheme } from '../lib/theme'

/** The app's start: the login, or the files once signed in. */
export default function Start() {
  const { session } = useSession()
  const t = useTheme()
  if (session.state === 'loading') {
    return (
      <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', backgroundColor: t.bg }}>
        <ActivityIndicator />
      </View>
    )
  }
  return <Redirect href={session.state === 'signed-in' ? '/files' : '/login'} />
}
