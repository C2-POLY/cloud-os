import { sha256 } from '@noble/hashes/sha2.js'
import { bytesToHex } from '@noble/hashes/utils.js'
import { File } from 'expo-file-system'

/** Bytes read at a time: a video is never loaded whole into memory. */
export const CHUNK = 1 << 20

/** What hashChunks reads from: an open file. */
export interface ByteSource {
  readBytes(length: number): Promise<Uint8Array>
  close(): void
}

/** The SHA-256 (hex) of everything src holds, read chunk by chunk; closes src. */
export async function hashChunks(src: ByteSource, chunk = CHUNK): Promise<string> {
  const h = sha256.create()
  try {
    for (;;) {
      const bytes = await src.readBytes(chunk)
      if (bytes.length === 0) break
      h.update(bytes)
    }
  } finally {
    src.close()
  }
  return bytesToHex(h.digest())
}

/** The SHA-256 (hex) of a local file ("file://…"). */
export function hashFile(uri: string): Promise<string> {
  return hashChunks(new File(uri).open())
}
