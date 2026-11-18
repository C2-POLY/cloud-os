import AsyncStorage from '@react-native-async-storage/async-storage'
import * as SecureStore from 'expo-secure-store'
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import type { Account } from '../../../shared/api-types'
import { ApiError, client, signIn as callSignIn, type Client, type Connection } from './api'

const boxKey = 'cloudos.box' // the box's address, not a secret
const tokenKey = 'cloudos.token' // the session token, in the Keychain

type Session =
  | { state: 'loading' }
  | { state: 'signed-out'; box: string }
  | { state: 'signed-in'; connection: Connection; account: Account; api: Client }

interface SessionValue {
  session: Session
  signIn: (box: string, name: string, password: string) => Promise<void>
  signOut: () => Promise<void>
}

const SessionContext = createContext<SessionValue | null>(null)

/** Keeps the box's address and this phone's session, and checks it at start. */
export function SessionProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session>({ state: 'loading' })

  useEffect(() => {
    let current = true
    ;(async () => {
      const box = (await AsyncStorage.getItem(boxKey)) ?? ''
      const token = await SecureStore.getItemAsync(tokenKey)
      if (!box || !token) return current && setSession({ state: 'signed-out', box })
      const connection = { box, token }
      const api = client(connection)
      try {
        const account = await api.me()
        if (current) setSession({ state: 'signed-in', connection, account, api })
      } catch (err) {
        // A rejected token signs out; an unreachable box keeps it for later.
        if (err instanceof ApiError && err.status === 401) await SecureStore.deleteItemAsync(tokenKey)
        if (current) setSession({ state: 'signed-out', box })
      }
    })()
    return () => {
      current = false
    }
  }, [])

  const signIn = useCallback(async (box: string, name: string, password: string) => {
    const { account, ...connection } = await callSignIn(box, name, password)
    await AsyncStorage.setItem(boxKey, box)
    await SecureStore.setItemAsync(tokenKey, connection.token)
    setSession({ state: 'signed-in', connection, account, api: client(connection) })
  }, [])

  const signOut = useCallback(async () => {
    if (session.state === 'signed-in') {
      await session.api.signOut().catch(() => {}) // the box may be away; forget the token anyway
      setSession({ state: 'signed-out', box: session.connection.box })
    }
    await SecureStore.deleteItemAsync(tokenKey)
  }, [session])

  const value = useMemo(() => ({ session, signIn, signOut }), [session, signIn, signOut])
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}

export function useSession(): SessionValue {
  const value = useContext(SessionContext)
  if (!value) throw new Error('useSession outside SessionProvider')
  return value
}

/** The signed-in client; only for screens shown when signed in. */
export function useApi(): Client {
  const { session } = useSession()
  if (session.state !== 'signed-in') throw new Error('useApi while signed out')
  return session.api
}
