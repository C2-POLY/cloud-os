import { Stack, useLocalSearchParams } from 'expo-router'
import { Folder } from '../../../components/Folder'

/** A folder below the root: /files/Photos/Été. */
export default function SubFolder() {
  const { path } = useLocalSearchParams<{ path: string[] | string }>()
  const folder = (Array.isArray(path) ? path : [path]).join('/')
  return (
    <>
      <Stack.Screen options={{ title: folder.split('/').pop() ?? '' }} />
      <Folder path={folder} />
    </>
  )
}
