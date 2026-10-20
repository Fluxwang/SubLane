import { z } from 'zod'
import { queryOptions, type QueryClient } from '@tanstack/react-query'
import { authKey, type AuthState } from './auth'
import { request } from './request'
import { providers } from './accounts'
import { cachePage, paginationFields } from './pagination'

export function formatRequestCost(value: number | null, language: string) {
  return value === null
    ? '—'
    : new Intl.NumberFormat(language, {
        style: 'currency',
        currency: 'USD',
        minimumFractionDigits: 2,
        maximumFractionDigits: 6,
      }).format(value / 1_000_000)
}

export function cacheHitRate(input: number | null, cached: number | null) {
  // Missing or inconsistent usage must not look like a cache miss or a 100% hit.
  if (
    input === null ||
    cached === null ||
    input <= 0 ||
    cached < 0 ||
    cached > input
  )
    return null
  return cached / input
}

export const outcomes = [
  'success',
  'incomplete',
  'error',
  'canceled',
  'rejected',
] as const
export const outcomeKeys = {
  success: 'requestSuccess',
  incomplete: 'requestIncomplete',
  error: 'requestFailed',
  canceled: 'requestCanceled',
  rejected: 'requestRejected',
} as const
const tokens = z.number().int().nonnegative().nullable()
const recordSchema = z.object({
  id: z.number().int().positive(),
  user_id: z.number().int(),
  key_id: z.number().int(),
  group_id: z.number().int(),
  account_id: z.string(),
  provider: z.union([z.enum(providers), z.literal('')]),
  model: z.string(),
  reasoning_effort: z.string().max(32).default(''),
  transport: z.enum(['http', 'websocket']),
  operation: z.enum(['responses', 'chat', 'compact', 'messages', 'gemini']),
  started_at: z.number().int(),
  duration_ms: z.number().int().nonnegative(),
  request_id: z.string().max(64),
  first_token_ms: tokens,
  outcome: z.enum(outcomes),
  error_code: z.string(),
  upstream_status: z.number().int().nullable(),
  input_tokens: tokens,
  output_tokens: tokens,
  cached_tokens: tokens,
  estimated_cost_micro_usd: z
    .number()
    .int()
    .nonnegative()
    .nullable()
    .default(null),
  username: z.string(),
  key_name: z.string(),
  group_name: z.string(),
  account_name: z.string(),
  content: z
    .object({
      mode: z.enum(['', 'observe', 'block']),
      revision: z.number().int().nonnegative(),
      rule_ids: z.array(z.string().max(64)).max(50),
      check_failed: z.boolean(),
    })
    .optional(),
})
export type RequestRecord = z.infer<typeof recordSchema>
export type RequestFilters = {
  account_id?: string
  outcome?: string
  model?: string
  request_id?: string
  from?: number
  until?: number
  key_id?: number
  user_id?: number
}

function currentScope(
  client: QueryClient,
  userID: number | undefined,
  scope: 'personal' | 'all',
) {
  const user = client.getQueryData<AuthState>(authKey)?.user
  return (
    userID !== undefined &&
    user?.id === userID &&
    (scope === 'personal' || user.role === 'admin')
  )
}

export function requestOptions(
  client: QueryClient,
  userID: number | undefined,
  scope: 'personal' | 'all',
  page: number,
  filters: RequestFilters,
  pageSize = 50,
) {
  const query = new URLSearchParams({
    page: String(page),
    limit: String(pageSize),
  })
  for (const [key, value] of Object.entries(filters)) {
    if (scope === 'personal' && (key === 'account_id' || key === 'user_id'))
      continue
    if (value !== undefined && value !== '') query.set(key, String(value))
  }
  const key = (value: number) =>
    ['request-history', userID, scope, value, pageSize, filters] as const
  return queryOptions({
    queryKey: key(page),
    enabled: () => currentScope(client, userID, scope),
    queryFn: async ({ signal }: { signal: AbortSignal }) => {
      const data = await request(
        `${scope === 'personal' ? '/api/me/requests' : '/api/requests'}?${query}`,
        z.object({
          requests: z.array(recordSchema).max(100),
          ...paginationFields,
        }),
        { signal },
      )
      if (currentScope(client, userID, scope))
        cachePage(client, key, page, data)
      return data
    },
  })
}

export function requestCallerOptions(
  client: QueryClient,
  userID: number,
  scope: 'personal' | 'all',
) {
  return queryOptions({
    queryKey: ['request-callers', userID, scope],
    enabled: () => currentScope(client, userID, scope),
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      request(
        `${scope === 'personal' ? '/api/me/requests' : '/api/requests'}/filters`,
        z.object({
          callers: z
            .array(
              z.object({
                user_id: z.number().int(),
                key_id: z.number().int(),
                username: z.string(),
                key_name: z.string(),
              }),
            )
            .max(5000),
        }),
        { signal },
      ),
    staleTime: 30000,
  })
}
