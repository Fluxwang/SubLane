import { queryOptions, type QueryClient } from '@tanstack/react-query'
import { z } from 'zod'
import { authKey, type AuthState } from './auth'
import { request } from './request'

const secretSchema = z.string().regex(/^sl_[A-Za-z0-9_-]{43}$/)
const keySchema = z.object({
  scheme_id: z.number().int().optional(),
  scheme_name: z.string().optional(),
  copyable: z.boolean(),
  id: z.number().int().positive(),
  group_id: z.number().int().positive(),
  group_name: z.string().min(1),
  group_access: z.enum(['allowed', 'blocked']),
  name: z.string().min(1).max(256),
  prefix: z.string().min(1).max(20),
  created_at: z.number().int().nonnegative(),
  last_used_at: z.number().int().nullable(),
  revoked_at: z.number().int().nullable(),
  enabled: z.boolean(),
  expires_at: z.number().int().nullable(),
})
const pageSchema = z.object({
  keys: z.array(keySchema).max(50),
  server_time: z.number().int(),
  next_cursor: z.number().int().nonnegative(),
})
const createdSchema = z.object({
  key: keySchema,
  secret: secretSchema,
})
export type APIKey = z.infer<typeof keySchema>

export function keyOptions(
  client: QueryClient,
  userID: number,
  cursor: number,
  includeRevoked: boolean,
) {
  return queryOptions({
    queryKey: ['keys', userID, cursor, includeRevoked],
    queryFn: ({ signal }) =>
      request(
        `/api/keys?cursor=${cursor}&include_revoked=${includeRevoked}`,
        pageSchema,
        { signal },
      ),
    // These personal queries opt into member access and stop immediately if the active identity changes.
    enabled: () =>
      userID > 0 &&
      client.getQueryData<AuthState>(authKey)?.user?.id === userID,
  })
}
export function createKey(input: {
  scheme_id?: number
  name: string
  group_id: number
  expires_at: number | null
}) {
  return request('/api/keys', createdSchema, {
    method: 'POST',
    body: JSON.stringify(input),
  })
}
export function revokeKey(id: number) {
  return request(`/api/keys/${id}/revoke`, keySchema, {
    method: 'POST',
    body: '{}',
  })
}

export function updateKey({
  id,
  ...input
}: {
  id: number
  name: string
  enabled: boolean
  expires_at: number | null
}) {
  return request(`/api/keys/${id}`, keySchema, {
    method: 'PATCH',
    body: JSON.stringify(input),
  })
}
export function keyState(value: APIKey, now: number) {
  if (value.revoked_at !== null) return 'revoked'
  if (value.expires_at !== null && value.expires_at <= now) return 'keyExpired'
  if (!value.enabled) return 'keyPaused'
  if (value.group_access !== 'allowed') return 'keyGroupUnavailable'
  return 'active'
}

export type ExpiryChoice = 'keep' | 'never' | '7' | '30' | '90'
export function expiryValue(choice: ExpiryChoice, current: number | null) {
  if (choice === 'keep') return current
  if (choice === 'never') return null
  return Math.floor(Date.now() / 1000) + Number(choice) * 86400
}

export function revealKey(id: number, signal: AbortSignal) {
  return request(`/api/keys/${id}/secret`, z.object({ secret: secretSchema }), {
    method: 'POST',
    body: '{}',
    signal,
    cache: 'no-store',
  })
}
