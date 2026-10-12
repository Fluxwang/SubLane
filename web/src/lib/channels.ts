import { z } from 'zod'
import { ApiError, request } from './request'
import { runtimeSchema } from './runtime'
import { accountErrorKey } from './accounts'

export const channelSchema = z.object({
  id: z.string().min(1),
  name: z.string(),
  protocol: z.literal('openai'),
  base_url: z.string().max(2048),
  enabled: z.boolean(),
  status: z.enum(['ready', 'unverified', 'key_required']),
  proxy_id: z.string().default(''),
  max_concurrency: z.number().int().min(1).max(1000),
  group_count: z.number().int().nonnegative().optional(),
  created_at: z.number().int().nonnegative(),
  updated_at: z.number().int().nonnegative(),
})
export type Channel = z.infer<typeof channelSchema>
export type ChannelInput = {
  name: string
  api_key: string
  base_url: string
  proxy_id?: string
}
export const channelOptions = {
  queryKey: ['channels'],
  queryFn: ({ signal }: { signal: AbortSignal }) =>
    request(
      '/api/channels',
      z.object({ channels: z.array(channelSchema).max(100) }),
      { signal },
    ),
}
export const channelRuntimeOptions = {
  queryKey: ['channel-runtime'],
  queryFn: ({ signal }: { signal: AbortSignal }) =>
    request(
      '/api/channels/runtime',
      z.object({
        channels: z.array(runtimeSchema).max(100),
        server_time: z.number().int(),
      }),
      { signal },
    ),
  refetchInterval: 5000,
  staleTime: 2000,
}
export function saveChannel({ id, ...input }: ChannelInput & { id?: string }) {
  return request(
    id ? `/api/channels/${encodeURIComponent(id)}/key` : '/api/channels',
    channelSchema,
    { method: id ? 'PUT' : 'POST', body: JSON.stringify(input) },
  )
}
export function setChannelEnabled({
  id,
  enabled,
}: {
  id: string
  enabled: boolean
}) {
  return request(`/api/channels/${encodeURIComponent(id)}`, channelSchema, {
    method: 'PATCH',
    body: JSON.stringify({ enabled }),
  })
}
export function deleteChannel(id: string) {
  return request(`/api/channels/${encodeURIComponent(id)}`, z.undefined(), {
    method: 'DELETE',
    body: '{}',
  })
}
export function checkChannel(id: string) {
  return request(
    `/api/channels/${encodeURIComponent(id)}/check`,
    z.object({
      channel: channelSchema,
      models: z
        .array(
          z.object({
            id: z.string(),
            object: z.literal('model'),
            owned_by: z.string(),
          }),
        )
        .max(512),
    }),
    { method: 'POST', body: '{}' },
  )
}
export function bindChannelProxy({
  id,
  proxy_id,
}: {
  id: string
  proxy_id: string
}) {
  return request(
    `/api/channels/${encodeURIComponent(id)}/proxy`,
    channelSchema,
    { method: 'PUT', body: JSON.stringify({ proxy_id }) },
  )
}
export function setChannelConcurrency({
  id,
  max_concurrency,
}: {
  id: string
  max_concurrency: number
}) {
  return request(
    `/api/channels/${encodeURIComponent(id)}/limits`,
    channelSchema,
    { method: 'PATCH', body: JSON.stringify({ max_concurrency }) },
  )
}
export function resumeChannel(id: string) {
  return request(
    `/api/channels/${encodeURIComponent(id)}/resume`,
    z.undefined(),
    { method: 'POST', body: '{}' },
  )
}
export function channelErrorKey(error: unknown) {
  if (error instanceof ApiError) {
    if (error.code === 'account_exists') return 'channelExists'
    if (error.code === 'channel_not_found') return 'channelNotFound'
    if (error.code === 'account_reauthorization_required')
      return 'apiKeyReplacementHint'
  }
  return accountErrorKey(error)
}

export function channelLoadErrorKey(error: unknown) {
  // Domain errors such as missing workspaces can also return 404.
  if (
    error instanceof ApiError &&
    error.status === 404 &&
    error.code === 'not_found'
  )
    return 'channelBackendMissing'
  return 'channelsLoadFailed'
}
