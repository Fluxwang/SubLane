import { z } from 'zod'
import type { en } from '@/locales/en'
import { accountSchema } from './accounts'
import { request } from './request'

export const runtimeSchema = z.object({
  id: z.string(),
  max_concurrency: z.number().int().min(1).max(1000),
  in_flight: z.number().int().nonnegative(),
  cooldown_until: z.number().int().nonnegative(),
  reason: z.string(),
  failures: z.number().int().nonnegative(),
  limited_models: z.number().int().nonnegative().default(0),
  state: z.enum([
    'available',
    'cooling',
    'retry_ready',
    'probing',
    'quota_exhausted',
  ]),
})
export type AccountRuntime = z.infer<typeof runtimeSchema>
export const runtimeOptions = {
  queryKey: ['account-runtime'],
  queryFn: ({ signal }: { signal: AbortSignal }) =>
    request(
      '/api/accounts/runtime',
      z.object({
        accounts: z.array(runtimeSchema).max(100),
        server_time: z.number().int(),
      }),
      { signal },
    ),
  refetchInterval: 5000,
  staleTime: 2000,
}
export function setConcurrency({
  id,
  max_concurrency,
}: {
  id: string
  max_concurrency: number
}) {
  return request(`/api/accounts/${id}/limits`, accountSchema, {
    method: 'PATCH',
    body: JSON.stringify({ max_concurrency }),
  })
}
export function resumeAccount(id: string) {
  return request(`/api/accounts/${id}/resume`, z.undefined(), {
    method: 'POST',
    body: '{}',
  })
}
export const reasonKeys: Record<string, keyof typeof en> = {
  content_policy_blocked: 'reasonContentBlocked',
  content_check_unavailable: 'reasonContentUnavailable',
  allocation_exhausted: 'allocationExhausted',
  allocation_pending: 'allocationPending',
  allocation_risk_limit: 'allocationRiskLimit',
  allocation_unavailable: 'allocationUnavailable',
  allocation_model_unpriced: 'allocationUnpriced',
  quota_exhausted: 'reasonQuotaExhausted',
  model_not_allowed: 'reasonModelNotAllowed',
  model_not_available: 'reasonModelUnavailable',
  model_catalog_unavailable: 'reasonCatalogUnavailable',
  token_quota_exceeded: 'reasonTokenQuota',
  token_usage_pending: 'reasonTokenPending',
  token_accounting_unavailable: 'reasonTokenAccounting',
  allocation_accounting_unavailable: 'reasonAllocationAccounting',
  member_busy: 'reasonMemberBusy',
  member_rate_limited: 'reasonMemberRate',
  context_limit: 'reasonContextLimit',
  rate_limited: 'reasonRateLimited',
  timeout: 'reasonTimeout',
  upstream_error: 'reasonUpstream',
  upstream_unavailable: 'reasonUpstream',
  stream_interrupted: 'reasonInterrupted',
  client_disconnected: 'reasonCanceled',
  account_busy: 'reasonAccountBusy',
  account_queue_full: 'reasonAccountQueueFull',
  account_wait_timeout: 'reasonAccountWaitTimeout',
  account_cooling: 'reasonCooling',
  model_cooling: 'reasonModelCooling',
  model_rate_limited: 'reasonModelRateLimited',
  account_unavailable: 'reasonAccountUnavailable',
  group_unavailable: 'reasonGroupUnavailable',
  auth_required: 'reasonAuthRequired',
  upstream_forbidden: 'reasonForbidden',
  upstream_rejected: 'reasonRejected',
  invalid_request: 'reasonInvalid',
  refresh_failed: 'reasonRefresh',
}
