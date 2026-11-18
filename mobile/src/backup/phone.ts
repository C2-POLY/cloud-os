import AsyncStorage from '@react-native-async-storage/async-storage'
import * as Device from 'expo-device'
import { FileSystemSessionType, FileSystemUploadType, uploadAsync } from 'expo-file-system/legacy'
// Read only: the backup lists the camera roll and reads its files. It never
// imports a function that deletes or edits an element (see backup.test.ts).
import { getAssetInfoAsync, getAssetsAsync, getPermissionsAsync, MediaType } from 'expo-media-library/legacy'
import { authHeaders, client, type Connection } from '../lib/api'
import { hashFile } from './hash'
import { loadLedger, saveLedger } from './ledger'
import { folderName } from './paths'
import type { Deps, PhoneAsset } from './run'

const phoneKey = 'cloudos.backup.phone'
const enabledKey = 'cloudos.backup.enabled'

/** The folder this phone backs up to, under Photos/: its name, or the one chosen in the Sauvegarde tab. */
export async function phoneFolder(): Promise<string> {
  return folderName((await AsyncStorage.getItem(phoneKey)) ?? Device.deviceName ?? Device.modelName)
}

export function setPhoneFolder(name: string): Promise<void> {
  return AsyncStorage.setItem(phoneKey, folderName(name))
}

export async function backupEnabled(): Promise<boolean> {
  return (await AsyncStorage.getItem(enabledKey)) === '1'
}

export function setBackupEnabled(on: boolean): Promise<void> {
  return AsyncStorage.setItem(enabledKey, on ? '1' : '0')
}

/** Every photo and video of the camera roll, oldest first. */
async function allAssets(): Promise<PhoneAsset[]> {
  const out: PhoneAsset[] = []
  let after: string | undefined
  for (;;) {
    const page = await getAssetsAsync({ first: 500, after, mediaType: [MediaType.photo, MediaType.video], sortBy: [['creationTime', true]] })
    for (const a of page.assets) out.push({ id: a.id, filename: a.filename, takenMs: a.creationTime })
    if (!page.hasNextPage) return out
    after = page.endCursor
  }
}

/** The pass's dependencies on this phone and this box. */
export function phoneDeps(c: Connection): Deps {
  const api = client(c)
  return {
    assets: allAssets,
    localUri: async (a) => {
      const info = await getAssetInfoAsync(a.id, { shouldDownloadFromNetwork: true })
      if (!info.localUri) throw new Error(`${a.filename} n'est pas disponible sur le téléphone.`)
      return info.localUri
    },
    hash: hashFile,
    known: api.known,
    upload: async (uri, path, sha256, takenMs) => {
      // A background upload session: iOS goes on sending with the app closed.
      const res = await uploadAsync(`${c.box}/api/files/content?path=${encodeURIComponent(path)}`, uri, {
        httpMethod: 'PUT',
        uploadType: FileSystemUploadType.BINARY_CONTENT,
        sessionType: FileSystemSessionType.BACKGROUND,
        headers: {
          ...authHeaders(c.token),
          'Content-Type': 'application/octet-stream',
          'X-Content-SHA256': sha256,
          'X-Modified-At': String(Math.floor(takenMs / 1000)),
        },
      })
      return res.status
    },
    save: (ledger) => saveLedger(AsyncStorage, ledger),
    now: Date.now,
  }
}

/** How far the backup is, for the Sauvegarde tab: null counts when the camera roll is not readable. */
export async function backupStatus(): Promise<{ remaining: number | null; total: number | null; lastSuccess: number | null; lastError: string | null }> {
  const ledger = await loadLedger(AsyncStorage)
  const { granted } = await getPermissionsAsync(false)
  if (!granted) return { remaining: null, total: null, lastSuccess: ledger.lastSuccess, lastError: ledger.lastError }
  const assets = await allAssets()
  const remaining = assets.filter((a) => !ledger.items[a.id]?.done).length
  return { remaining, total: assets.length, lastSuccess: ledger.lastSuccess, lastError: ledger.lastError }
}
