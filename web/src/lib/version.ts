import { queryOptions, type QueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { authKey, type AuthState } from './auth'
import { ApiError, request } from './request'

const stable = /^(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})$/
export const validVersion = (value: string) =>
  !value.trim() || stable.test(value.trim())
const schema = z.object({
  manual_version: z.string().max(24),
  auto_sync: z.boolean(),
  effective_version: z.string().regex(stable),
  default_version: z.string().regex(stable),
  latest_version: z.string().max(24),
  source: z.enum(['manual', 'synced', 'builtin']),
  checked_at: z.number().int().nonnegative(),
  successful_at: z.number().int().nonnegative(),
  next_check_at: z.number().int().nonnegative(),
  server_time: z.number().int(),
  retry_after_seconds: z.number().int().nonnegative(),
  syncing: z.boolean(),
  sync_failed: z.boolean(),
})
export type VersionState = z.infer<typeof schema>
export type VersionProvider = 'codex' | 'grok' | 'claude'
export type VersionConfig = Pick<VersionState, 'manual_version' | 'auto_sync'>
export function versionOptions(
  client: QueryClient,
  userID: number,
  provider: VersionProvider = 'codex',
) {
  return queryOptions({
    queryKey: [provider + '-version', userID],
    queryFn: ({ signal }) =>
      request('/api/settings/' + provider, schema, { signal }),
    enabled: () => {
      const user = client.getQueryData<AuthState>(authKey)?.user
      return user?.id === userID && user.role === 'admin'
    },
    staleTime: 15_000,
    refetchInterval: (query) =>
      query.state.error ? false : query.state.data?.syncing ? 1000 : 60_000,
  })
}
export function saveVersion(
  input: VersionConfig,
  signal: AbortSignal,
  provider: VersionProvider = 'codex',
) {
  return request('/api/settings/' + provider, schema, {
    method: 'PATCH',
    body: JSON.stringify(input),
    signal,
  })
}
export function syncVersion(
  signal: AbortSignal,
  provider: VersionProvider = 'codex',
) {
  return request('/api/settings/' + provider + '/sync', schema, {
    method: 'POST',
    body: '{}',
    signal,
  })
}
export function versionErrorKey(
  error: Error,
  provider: VersionProvider = 'codex',
) {
  if (error instanceof ApiError) {
    if (error.code === 'demo_read_only') return 'demoReadOnly'
    if (error.code === 'invalid_' + provider + '_version')
      return 'clientVersionInvalid'
    if (error.code === provider + '_version_sync_cooldown')
      return 'clientVersionCooldown'
    if (error.code === provider + '_version_storage_failed')
      return 'clientVersionSaveFailed'
  }
  return 'clientVersionSyncFailed'
}
