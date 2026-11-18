import AsyncStorage from '@react-native-async-storage/async-storage'
import * as BackgroundTask from 'expo-background-task'
import { getPermissionsAsync, requestPermissionsAsync } from 'expo-media-library/legacy'
import * as TaskManager from 'expo-task-manager'
import { storedConnection } from '../lib/session'
import { loadLedger } from './ledger'
import { backupEnabled, phoneDeps, phoneFolder } from './phone'
import { runBackup, type Summary } from './run'

export const TASK = 'cloudos-backup'
const BACKGROUND_BUDGET = 25_000 // ms: iOS gives a background task about 30 s

let running: Promise<Summary> | null = null

/** Asks for the camera roll, read only. */
export async function askPhotoAccess(): Promise<boolean> {
  return (await requestPermissionsAsync(false)).granted
}

/** Runs one pass, or joins the one already running (foreground and background never overlap). */
export function backupNow(deadline = Infinity, onProgress?: (s: Summary) => void): Promise<Summary> {
  if (running) return running
  running = (async () => {
    const c = await storedConnection()
    if (!c) throw new Error('Connecte-toi à la box pour sauvegarder.')
    if (!(await getPermissionsAsync(false)).granted) throw new Error("Autorise l'accès aux photos pour sauvegarder.")
    const ledger = await loadLedger(AsyncStorage)
    return runBackup(phoneDeps(c), ledger, await phoneFolder(), deadline, onProgress)
  })().finally(() => {
    running = null
  })
  return running
}

// Defined at startup, before iOS or Android may wake the app for it.
TaskManager.defineTask(TASK, async () => {
  try {
    if (!(await backupEnabled())) return BackgroundTask.BackgroundTaskResult.Success
    const s = await backupNow(Date.now() + BACKGROUND_BUDGET)
    return s.stopped === 'error' ? BackgroundTask.BackgroundTaskResult.Failed : BackgroundTask.BackgroundTaskResult.Success
  } catch {
    return BackgroundTask.BackgroundTaskResult.Failed
  }
})

/** Asks the system to wake the app for a pass every 15 minutes at best, or stops asking. */
export async function scheduleBackup(on: boolean): Promise<void> {
  const registered = await TaskManager.isTaskRegisteredAsync(TASK)
  if (on && !registered) await BackgroundTask.registerTaskAsync(TASK, { minimumInterval: 15 })
  if (!on && registered) await BackgroundTask.unregisterTaskAsync(TASK)
}
