import { useId, useRef, useState } from 'react'
import {
  keepPreviousData,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import {
  ChevronDown,
  LoaderCircle,
  RefreshCw,
  SlidersHorizontal,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { accountOptions, providerLabels } from '@/lib/accounts'
import {
  requestOptions,
  outcomeKeys,
  outcomes,
  cacheHitRate,
  formatRequestCost,
  type RequestFilters,
  type RequestRecord,
} from '@/lib/requests'
import { reasonKeys } from '@/lib/runtime'
import { authKey, authOptions, type AuthState } from '@/lib/auth'
import { Button } from '@/components/ui/Button'
import { RequestFilterForm } from '@/components/RequestFilters'
import { RequestID } from '@/components/RequestID'
import { RequestDetails } from '@/components/RequestDetails'
import { Status } from '@/components/Status'
import { Pagination } from '@/components/Pagination'
import { TableViewport } from '@/components/TableViewport'
import { useTablePagination } from '@/lib/pagination'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/DropdownMenu'

import { useTimeZone } from '@/lib/timezone'
export function Requests() {
  return <RequestHistory scope="personal" />
}

export function AllRequests() {
  return <RequestHistory scope="all" />
}

function RequestHistory({ scope }: { scope: 'personal' | 'all' }) {
  const { data: auth } = useQuery(authOptions())
  const user = auth?.user
  if (!user || (scope === 'all' && user.role !== 'admin')) return null
  return (
    <RequestTable key={`${scope}:${user.id}`} scope={scope} userID={user.id} />
  )
}

function RequestTable({
  scope,
  userID,
}: {
  scope: 'personal' | 'all'
  userID: number
}) {
  const { t, i18n } = useTranslation()
  const timeZone = useTimeZone()
  const client = useQueryClient()
  const personal = scope === 'personal'
  const { page, pageSize, viewport, changePage, changePageSize, syncPage } =
    useTablePagination()
  const [filters, setFilters] = useState<RequestFilters>({})
  const [expanded, setExpanded] = useState(false)
  const [selected, setSelected] = useState<RequestRecord | null>(null)
  const filterID = useId()
  const detailTrigger = useRef<HTMLButtonElement | null>(null)
  const account = filters.account_id ?? ''
  const outcome = filters.outcome ?? ''
  const apply = (value: RequestFilters) => {
    setFilters(value)
    changePage(1)
  }
  const active = Object.values(filters).some(
    (value) => value !== undefined && value !== '',
  )
  const query = useQuery({
    ...requestOptions(client, userID, scope, page, filters, pageSize),
    placeholderData: keepPreviousData,
  })
  syncPage(query.data?.page, query.isPlaceholderData)
  const accounts = useQuery({
    ...accountOptions,
    enabled: () =>
      !personal &&
      client.getQueryData<AuthState>(authKey)?.user?.role === 'admin',
  })
  const dates = new Intl.DateTimeFormat(i18n.resolvedLanguage ?? 'en', {
    timeZone,
    dateStyle: 'short',
  })
  const clock = new Intl.DateTimeFormat(i18n.resolvedLanguage ?? 'en', {
    timeZone,
    timeStyle: 'medium',
  })
  const numbers = new Intl.NumberFormat(i18n.resolvedLanguage ?? 'en')
  const count = (value: number | null) =>
    value === null ? '—' : numbers.format(value)
  const percentages = new Intl.NumberFormat(i18n.resolvedLanguage ?? 'en', {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  const hitRate = (input: number | null, cached: number | null) => {
    const rate = cacheHitRate(input, cached)
    return rate === null ? '—' : percentages.format(rate)
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="page-title">
            {t(personal ? 'yourRequests' : 'allRequests')}
          </h1>
          <p className="page-description">
            {t(
              personal ? 'personalRequestsDescription' : 'requestsDescription',
            )}
          </p>
        </div>
        <Button
          variant="outline"
          disabled={query.isFetching}
          onClick={() => query.refetch()}
        >
          <RefreshCw
            aria-hidden="true"
            className={query.isFetching ? 'motion-safe:animate-spin' : ''}
          />
          {t('refresh')}
        </Button>
      </div>
      <div className="flex flex-wrap gap-3">
        {!personal && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" aria-label={t('requestAccountFilter')}>
                {account
                  ? (accounts.data?.accounts.find((item) => item.id === account)
                      ?.name ?? t('requestDeletedAccount'))
                  : t('allRequestAccounts')}
                <ChevronDown aria-hidden="true" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent>
              <DropdownMenuRadioGroup
                value={account}
                onValueChange={(value) => {
                  apply({ ...filters, account_id: value })
                }}
              >
                <DropdownMenuRadioItem value="">
                  {t('allRequestAccounts')}
                </DropdownMenuRadioItem>
                {accounts.data?.accounts.map((item) => (
                  <DropdownMenuRadioItem key={item.id} value={item.id}>
                    {item.name}
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" aria-label={t('requestResultFilter')}>
              {outcome
                ? t(outcomeKeys[outcome as keyof typeof outcomeKeys])
                : t('allRequestOutcomes')}
              <ChevronDown aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuRadioGroup
              value={outcome}
              onValueChange={(value) => {
                apply({ ...filters, outcome: value })
              }}
            >
              <DropdownMenuRadioItem value="">
                {t('allRequestOutcomes')}
              </DropdownMenuRadioItem>
              {outcomes.map((value) => (
                <DropdownMenuRadioItem key={value} value={value}>
                  {t(outcomeKeys[value])}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <Button
          variant="outline"
          aria-expanded={expanded}
          aria-controls={filterID}
          onClick={() => setExpanded((value) => !value)}
        >
          <SlidersHorizontal aria-hidden="true" />
          {t('requestFilters')}
        </Button>
        {active && (
          <Button
            variant="ghost"
            onClick={() => {
              apply({})
              setExpanded(false)
            }}
          >
            {t('requestClearFilters')}
          </Button>
        )}
      </div>
      {expanded && (
        <RequestFilterForm
          key={JSON.stringify(filters)}
          id={filterID}
          value={filters}
          scope={scope}
          userID={userID}
          onApply={apply}
        />
      )}
      {query.isPending ? (
        <p
          role="status"
          className="flex items-center gap-2 py-10 text-sm text-muted-foreground"
        >
          <LoaderCircle
            aria-hidden="true"
            className="size-4 motion-safe:animate-spin"
          />
          {t('loadingRequests')}
        </p>
      ) : query.isError ? (
        <div role="alert" className="space-y-3">
          <p className="text-sm text-error">{t('requestsLoadFailed')}</p>
          <Button variant="outline" onClick={() => query.refetch()}>
            {t('reconnect')}
          </Button>
        </div>
      ) : query.data.requests.length === 0 ? (
        <div className="rounded-xl border border-dashed border-border p-10 text-center">
          <h2 className="font-medium">{t('noRequests')}</h2>
          <p className="mt-2 text-sm text-muted-foreground">
            {t('noRequestsHint')}
          </p>
        </div>
      ) : (
        <TableViewport ref={viewport} aria-busy={query.isFetching}>
          <table className="w-full text-left text-sm">
            <thead className="sticky top-0 z-10 border-b border-border bg-card text-muted-foreground">
              <tr>
                {(
                  [
                    'requestTime',
                    personal ? 'requestKey' : 'requestCaller',
                    personal ? 'requestGroup' : 'requestAccount',
                    'requestModel',
                    'requestResult',
                    'requestDuration',
                    'requestTokens',
                    'requestEstimatedCost',
                  ] as const
                ).map((label) => (
                  <th
                    key={label}
                    scope="col"
                    className={`whitespace-nowrap px-4 py-3 font-medium ${label === 'requestEstimatedCost' ? 'text-right' : ''}`}
                  >
                    {t(label)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {query.data.requests.map((item) => (
                <tr key={item.id}>
                  <td className="whitespace-nowrap px-4 py-4 text-xs text-muted-foreground">
                    <button
                      type="button"
                      className="rounded-sm text-left text-foreground underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
                      aria-label={t('requestViewDetails')}
                      title={t('requestViewDetails')}
                      onClick={(event) => {
                        detailTrigger.current = event.currentTarget
                        setSelected(item)
                      }}
                    >
                      <time
                        dateTime={new Date(
                          item.started_at * 1000,
                        ).toISOString()}
                      >
                        <span className="block">
                          {dates.format(item.started_at * 1000)}
                        </span>
                        <span className="mt-1 block">
                          {clock.format(item.started_at * 1000)}
                        </span>
                      </time>
                    </button>
                    <div className="mt-1">
                      <RequestID value={item.request_id} compact />
                    </div>
                  </td>
                  <td className="px-4 py-4">
                    {!personal && (
                      <p className="whitespace-nowrap">
                        {item.username || '—'}
                      </p>
                    )}
                    <p className="mt-1 text-xs text-muted-foreground">
                      {item.key_name || '—'}
                    </p>
                  </td>
                  <td className="px-4 py-4">
                    {!personal && (
                      <p className="min-w-24">
                        {item.account_name ||
                          (item.account_id ? t('requestDeletedAccount') : '—')}
                      </p>
                    )}
                    <p className="mt-1 text-xs text-muted-foreground">
                      {item.group_name || '—'}
                    </p>
                  </td>
                  <td className="px-4 py-4">
                    <code
                      className="block w-48 truncate text-xs"
                      title={item.model}
                    >
                      {item.model || '—'}
                    </code>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t('requestReasoningEffortValue', {
                        value: item.reasoning_effort || '—',
                      })}
                    </p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {item.provider && `${providerLabels[item.provider]} · `}
                      {item.transport === 'websocket'
                        ? 'WebSocket'
                        : 'HTTP'} · {item.operation}
                    </p>
                  </td>
                  <td className="px-4 py-4">
                    <Status
                      kind={
                        item.outcome === 'success'
                          ? 'success'
                          : item.outcome === 'canceled'
                            ? 'neutral'
                            : 'warning'
                      }
                    >
                      {t(outcomeKeys[item.outcome])}
                    </Status>
                    {item.error_code && (
                      <p className="mt-1 min-w-28 text-xs text-muted-foreground">
                        {t(reasonKeys[item.error_code] ?? 'reasonUnknown')}
                        {item.upstream_status !== null &&
                          ` · ${item.upstream_status}`}
                      </p>
                    )}
                    {item.content &&
                      (item.content.rule_ids.length > 0 ||
                        item.content.check_failed) && (
                        <p className="mt-1 text-xs text-warning">
                          {t(
                            item.content.check_failed
                              ? 'requestContentFailed'
                              : 'contentTitle',
                          )}
                        </p>
                      )}
                  </td>
                  <td className="whitespace-nowrap px-4 py-4 tabular-nums">
                    {numbers.format(item.duration_ms)} ms
                    <p
                      className="mt-1 text-xs text-muted-foreground"
                      title={t('requestFirstTokenHint')}
                    >
                      {t('requestFirstTokenValue', {
                        value:
                          item.first_token_ms === null
                            ? '—'
                            : `${numbers.format(item.first_token_ms)} ms`,
                      })}
                    </p>
                  </td>
                  <td className="whitespace-nowrap px-4 py-4 text-xs tabular-nums">
                    {count(item.input_tokens)} / {count(item.output_tokens)}
                    <p className="mt-1 text-muted-foreground">
                      {item.cached_tokens !== null && (
                        <>
                          <span>
                            {t('requestCachedTokens', {
                              count: numbers.format(item.cached_tokens),
                            })}
                          </span>
                          {' · '}
                        </>
                      )}
                      <span title={t('requestCacheHitRateHint')}>
                        {t('requestCacheHitRate', {
                          rate: hitRate(item.input_tokens, item.cached_tokens),
                        })}
                      </span>
                    </p>
                  </td>
                  <td
                    className="whitespace-nowrap px-4 py-4 text-right tabular-nums"
                    title={t('requestEstimatedCostHint')}
                  >
                    {formatRequestCost(
                      item.estimated_cost_micro_usd,
                      i18n.resolvedLanguage ?? 'en',
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableViewport>
      )}
      <RequestDetails
        key={selected?.id ?? 'closed'}
        value={selected}
        onClose={() => setSelected(null)}
        onRestoreFocus={() => detailTrigger.current?.focus()}
      />
      {query.data && (
        <Pagination
          page={query.data.page}
          pageSize={query.data.page_size}
          total={query.data.total}
          disabled={query.isFetching}
          onPageChange={changePage}
          onPageSizeChange={changePageSize}
        />
      )}
    </div>
  )
}
