import { z } from 'zod'
import { request } from './request'

export const memberLimitSchema = z.object({
  user_id: z.number().int().positive(),
  requests_per_minute: z.number().int().min(0).max(6000),
  max_concurrency: z.number().int().min(0).max(1000),
  in_flight: z.number().int().nonnegative(),
  requests_this_minute: z.number().int().nonnegative(),
  reset_at: z.number().int(),
})
export function memberLimitOptions(id: number) {
  return {
    queryKey: ['member-limits', id],
    queryFn: ({ signal }: { signal: AbortSignal }) =>
      request(`/api/members/${id}/limits`, memberLimitSchema, { signal }),
  }
}
export function setMemberLimits(input: {
  id: number
  requests_per_minute: number
  max_concurrency: number
}) {
  const { id, ...policy } = input
  return request(`/api/members/${id}/limits`, memberLimitSchema, {
    method: 'PATCH',
    body: JSON.stringify(policy),
  })
}
