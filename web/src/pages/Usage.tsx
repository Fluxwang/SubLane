import { useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { authOptions } from '@/lib/auth'
import { useTimeZone } from '@/lib/timezone'
import {
  statisticsOptions,
  ownLimitOptions,
  type UsageScope,
} from '@/lib/statistics'
import type { en } from '@/locales/en'
import { Button } from '@/components/ui/Button'
import { UsageChart } from '@/components/UsageChart'
import { UsageRanking } from '@/components/UsageRanking'
import { PersonalAllocations } from '@/components/PersonalAllocations'
import { ActivityMap } from '@/components/ActivityMap'
import {
  metricLabels,
  metricPartial,
  metricValue,
  type UsageMetric,
  type Statistic,
} from '@/lib/statistics'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/DropdownMenu'

export function Usage() {
  return <UsageScopeView scope="personal" />
}
export function TeamUsage() {
  return <UsageScopeView scope="all" />
}

function UsageScopeView({ scope }: { scope: UsageScope }) {
  const { data } = useQuery(authOptions())
  if (!data?.user || (scope === 'all' && data.user.role !== 'admin'))
    return null
  return (
    <UsageReport
      key={scope + data.user.id}
      scope={scope}
      userID={data.user.id}
    />
  )
}
function UsageReport({ scope, userID }: { scope: UsageScope; userID: number }) {
  const { t, i18n } = useTranslation()
  const timeZone = useTimeZone()
  const client = useQueryClient()
  const [days, setDays] = useState(7)
  const [metric, setMetric] = useState<UsageMetric>('tokens')
  const [showDetails, setShowDetails] = useState(false)
  const [exactTokens, setExactTokens] = useState(false)
  const [view, setView] = useState<'days' | 'models' | 'groups' | 'members'>(
    'days',
  )
  const query = useQuery(statisticsOptions(client, userID, scope, days))
  const limits = useQuery(ownLimitOptions(client, userID, scope))
  const number = new Intl.NumberFormat(i18n.resolvedLanguage ?? 'en', {
    maximumFractionDigits: 1,
  })
  const date = new Intl.DateTimeFormat(i18n.resolvedLanguage ?? 'en', {
    dateStyle: 'medium',
    timeZone: 'UTC',
  })
  const timestamp = new Intl.DateTimeFormat(i18n.resolvedLanguage ?? 'en', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone,
  })
  const periods = [1, 7, 30, 90]
  const period = (value: number) =>
    value === 1 ? t('usageToday') : t('usageLastDays', { days: value })
  const labels = {
    days: 'usageDaily',
    models: 'usageModels',
    groups: 'accountGroups',
    members: 'usageMembers',
  } as const
  const rows =
    query.data?.[view].filter(
      (row) =>
        view !== 'days' || Number(row.id) + 86400 > query.data!.tracking_since,
    ) ?? []
  const totals = query.data?.totals
  const labelName = (row: Statistic) =>
    row.name === '[other models]'
      ? t('usageOtherModels')
      : row.name || t('statisticsUnknown')
  const rowName = (row: Statistic) =>
    view === 'days' ? date.format(Number(row.id) * 1000) : labelName(row)
  const exactNumber = new Intl.NumberFormat(i18n.resolvedLanguage ?? 'en')
  // Token units stay K/M/B in every interface language.
  const compactNumber = new Intl.NumberFormat('en', {
    notation: 'compact',
    maximumFractionDigits: 1,
  })
  // Unreported counts are null; any reported count makes the value switchable.
  const tokenToggle = (counts: (number | null)[]) => {
    const format = (formatter: Intl.NumberFormat) =>
      counts
        .map((count) => (count === null ? '—' : formatter.format(count)))
        .join(' / ')
    const exact = format(exactNumber)
    if (counts.every((count) => count === null)) return exact
    const shown = exactTokens ? exact : format(compactNumber)
    return (
      <button
        type="button"
        aria-pressed={exactTokens}
        aria-label={t(
          exactTokens ? 'usageTokensShowCompact' : 'usageTokensShowExact',
          { value: shown },
        )}
        title={exactTokens ? undefined : exact}
        onClick={() => setExactTokens((value) => !value)}
        className="cursor-pointer rounded-sm tabular-nums underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {shown}
      </button>
    )
  }
  const tokenPair = (metrics: Statistic | NonNullable<typeof totals>) =>
    tokenToggle([
      metrics.input_reported > 0 ? metrics.input_tokens : null,
      metrics.output_reported > 0 ? metrics.output_tokens : null,
    ])
  // Like token units, USD amounts keep the same form in every interface language.
  const usd = new Intl.NumberFormat('en', {
    style: 'currency',
    currency: 'USD',
  })
  const usdAxis = new Intl.NumberFormat('en', {
    style: 'currency',
    currency: 'USD',
    notation: 'compact',
    maximumFractionDigits: 2,
  })
  const cost = (microUSD: number) =>
    microUSD > 0 && microUSD < 10_000
      ? t('usageCostBelow', { value: usd.format(0.01) })
      : usd.format(microUSD / 1_000_000)
  const dailyCost = new Map(
    query.data?.cost.days.map((day) => [day.id, day.estimated_micro_usd]),
  )
  const trend = (query.data?.days ?? [])
    .filter((row) => Number(row.id) + 86400 > query.data!.tracking_since)
    .map((row) =>
      metric === 'cost'
        ? {
            id: row.id,
            amount: dailyCost.get(row.id) ?? 0,
            partial: false,
          }
        : {
            id: row.id,
            amount: metricValue(row, metric),
            partial: metricPartial(row, metric),
          },
    )
  const percentage = (completed: number, requests: number) =>
    requests ? number.format((completed / requests) * 100) + '%' : '—'
  const duration = (sum: number, requests: number) =>
    requests ? number.format(sum / requests) + ' ms' : '—'
  return (
    <section
      aria-labelledby={
        scope === 'personal' ? 'personal-usage-title' : 'team-usage-title'
      }
      className={
        scope === 'personal'
          ? 'space-y-6 border-t border-border pt-8'
          : 'space-y-6'
      }
    >
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          {scope === 'personal' ? (
            <h2 id="personal-usage-title" className="text-xl font-semibold">
              {t('yourUsage')}
            </h2>
          ) : (
            <h1 id="team-usage-title" className="page-title">
              {t('teamUsage')}
            </h1>
          )}
        </div>
        <Button
          variant="outline"
          aria-label={
            scope === 'personal'
              ? `${t('yourUsage')}: ${t('refresh')}`
              : undefined
          }
          disabled={query.isFetching}
          onClick={() =>
            Promise.all([
              query.refetch(),
              ...(scope === 'personal' ? [limits.refetch()] : []),
            ])
          }
        >
          <RefreshCw
            aria-hidden="true"
            className={query.isFetching ? 'motion-safe:animate-spin' : ''}
          />
          {t('refresh')}
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" aria-label={t('usagePeriod')}>
              {period(days)}
              <ChevronDown aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup
              value={String(days)}
              onValueChange={(value) => setDays(Number(value))}
            >
              {periods.map((value) => (
                <DropdownMenuRadioItem key={value} value={String(value)}>
                  {period(value)}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" aria-label={t('usageBreakdown')}>
              {t(labels[view])}
              <ChevronDown aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            <DropdownMenuRadioGroup
              value={view}
              onValueChange={(value) => setView(value as typeof view)}
            >
              {(Object.keys(labels) as (keyof typeof labels)[])
                .filter((value) => scope === 'all' || value !== 'members')
                .map((value) => (
                  <DropdownMenuRadioItem key={value} value={value}>
                    {t(labels[value])}
                  </DropdownMenuRadioItem>
                ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <Button
          variant="ghost"
          className="ml-auto"
          aria-expanded={showDetails}
          aria-controls="usage-details"
          onClick={() => setShowDetails((value) => !value)}
        >
          {t(showDetails ? 'usageHideDetails' : 'usageViewDetails')}
        </Button>
      </div>
      {query.isPending ? (
        <p role="status" className="py-10 text-sm text-muted-foreground">
          {t('loadingUsageSummary')}
        </p>
      ) : query.isError ? (
        <div role="alert" className="space-y-3">
          <p className="text-sm text-error">{t('usageSummaryFailed')}</p>
          <Button variant="outline" onClick={() => query.refetch()}>
            {t('reconnect')}
          </Button>
        </div>
      ) : (
        totals && (
          <>
            <p className="text-xs text-muted-foreground">
              {t('usageTrackingSince', {
                time: timestamp.format(query.data.tracking_since * 1000),
                zone: timeZone,
              })}
            </p>
            <dl className="grid grid-cols-2 gap-4 lg:grid-cols-4">
              {(
                [
                  {
                    label: 'usageTotalCost',
                    value: cost(query.data.cost.estimated_micro_usd),
                  },
                  {
                    label: 'usageTotalRequests',
                    value: number.format(totals.requests),
                  },
                  {
                    label: 'usageTokenConsumption',
                    value: tokenToggle([
                      totals.input_reported > 0 || totals.output_reported > 0
                        ? totals.input_tokens + totals.output_tokens
                        : null,
                    ]),
                    detail:
                      totals.input_reported > 0 || totals.output_reported > 0
                        ? t('usageTokenSplit', {
                            input: compactNumber.format(totals.input_tokens),
                            output: compactNumber.format(totals.output_tokens),
                          })
                        : undefined,
                  },
                  {
                    label: 'usageCacheHitRate',
                    // Only calls that reported a cache split contribute cached tokens.
                    value:
                      totals.cached_reported > 0 && totals.input_tokens > 0
                        ? number.format(
                            (totals.cached_tokens / totals.input_tokens) * 100,
                          ) + '%'
                        : '—',
                  },
                ] as {
                  label: keyof typeof en
                  value: ReactNode
                  detail?: string
                }[]
              ).map((item) => (
                <div
                  key={item.label}
                  className="min-w-0 rounded-xl border border-border bg-card p-4 sm:p-5"
                >
                  <dt className="text-sm text-muted-foreground">
                    {t(item.label)}
                  </dt>
                  <dd className="mt-2 break-words text-2xl font-semibold tracking-tight tabular-nums sm:text-3xl">
                    {item.value}
                  </dd>
                  {item.detail && (
                    <dd className="mt-1 text-xs text-muted-foreground tabular-nums">
                      {item.detail}
                    </dd>
                  )}
                </div>
              ))}
            </dl>
            {query.data.cost.unpriced_tokens > 0 && (
              <p className="text-sm text-warning">{t('usageCostUnpriced')}</p>
            )}
            {totals.requests > 0 && (
              <>
                <p className="text-sm text-muted-foreground">
                  {t('usageOutcomes', {
                    completed: number.format(totals.completed),
                    incomplete: number.format(totals.incomplete),
                    errors: number.format(totals.errors),
                    canceled: number.format(totals.canceled),
                    rejected: number.format(totals.rejected),
                  })}
                </p>
                {(totals.input_reported < totals.requests ||
                  totals.output_reported < totals.requests) && (
                  <p className="text-sm text-warning">
                    {t('usagePartialTokens')}
                  </p>
                )}
              </>
            )}
            {totals.requests === 0 ? (
              <>
                <div className="rounded-xl border border-dashed border-border p-10 text-center">
                  <h2 className="font-medium">{t('noUsageSummary')}</h2>
                  <p className="mt-2 text-sm text-muted-foreground">
                    {t('noUsageSummaryHint')}
                  </p>
                </div>
                <ActivityMap
                  key={days}
                  activity={query.data.activity}
                  metric={metric === 'cost' ? 'tokens' : metric}
                />
              </>
            ) : (
              <div className="space-y-5">
                <div className="grid gap-5 xl:grid-cols-3">
                  <section
                    aria-label={t('usageChart')}
                    className="min-w-0 rounded-xl border border-border bg-card p-4 sm:p-6 xl:col-span-2"
                  >
                    <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
                      <h3 className="font-medium">
                        {t('usageTrend')} · {period(days)}
                      </h3>
                      <div
                        role="group"
                        aria-label={t('usageMetric')}
                        className="flex rounded-lg bg-muted p-1"
                      >
                        {(Object.keys(metricLabels) as UsageMetric[]).map(
                          (value) => (
                            <button
                              key={value}
                              type="button"
                              aria-pressed={metric === value}
                              onClick={() => setMetric(value)}
                              className="rounded-md px-3 py-1 text-sm text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring aria-pressed:bg-card aria-pressed:font-medium aria-pressed:text-foreground aria-pressed:shadow-sm"
                            >
                              {t(metricLabels[value])}
                            </button>
                          ),
                        )}
                      </div>
                    </div>
                    <UsageChart
                      key={days + metric}
                      points={trend}
                      label={t('usageTrend') + ' · ' + t(metricLabels[metric])}
                      metricLabel={t(metricLabels[metric])}
                      format={
                        metric === 'cost'
                          ? cost
                          : (value) => exactNumber.format(value)
                      }
                      axis={
                        metric === 'cost'
                          ? (value) => usdAxis.format(value / 1_000_000)
                          : (value) => compactNumber.format(value)
                      }
                    />
                  </section>
                  <UsageRanking
                    key={days}
                    items={query.data.models.map((row) => ({
                      id: row.id,
                      name: labelName(row),
                      tokens: row.input_tokens + row.output_tokens,
                    }))}
                    formatTokens={(tokens) => tokenToggle([tokens])}
                  />
                </div>
                <ActivityMap
                  key={days}
                  activity={query.data.activity}
                  metric={metric === 'cost' ? 'tokens' : metric}
                />
                {showDetails && (
                  <div
                    id="usage-details"
                    className="overflow-x-auto rounded-xl border border-border bg-card"
                  >
                    <table className="w-full text-left text-sm">
                      <caption className="sr-only">{t(labels[view])}</caption>
                      <thead className="border-b border-border text-muted-foreground">
                        <tr>
                          {(
                            [
                              labels[view],
                              'usageRequests',
                              'usageCompletionRate',
                              'usageAverageDuration',
                              'requestTokens',
                            ] as const
                          ).map((label) => (
                            <th
                              key={label}
                              scope="col"
                              className="whitespace-nowrap px-4 py-3 font-medium"
                            >
                              {t(label)}
                            </th>
                          ))}
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-border">
                        {rows.map((row) => (
                          <tr key={row.id}>
                            <th
                              scope="row"
                              className="max-w-64 px-4 py-4 font-medium"
                            >
                              <span
                                className="block max-w-64 truncate"
                                title={row.name}
                              >
                                {rowName(row)}
                              </span>
                            </th>
                            <td className="px-4 py-4 tabular-nums">
                              {number.format(row.requests)}
                            </td>
                            <td className="px-4 py-4 tabular-nums">
                              {percentage(row.completed, row.requests)}
                            </td>
                            <td className="whitespace-nowrap px-4 py-4 tabular-nums">
                              {duration(row.duration_ms, row.requests)}
                            </td>
                            <td className="whitespace-nowrap px-4 py-4 tabular-nums">
                              {tokenPair(row)}
                              {row.requests > 0 &&
                                (row.input_reported < row.requests ||
                                  row.output_reported < row.requests) && (
                                  <span className="mt-1 block text-xs text-muted-foreground">
                                    {t('usageReportedOnly')}
                                  </span>
                                )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              {t('usageRetentionNote')}
            </p>
          </>
        )
      )}
      {scope === 'personal' && <PersonalAllocations userID={userID} />}
      {scope === 'personal' && (
        <section className="space-y-3 border-t border-border pt-5">
          <h2 className="font-medium">{t('yourRequestLimits')}</h2>
          {limits.isPending ? (
            <p role="status" className="text-sm text-muted-foreground">
              {t('loadingMemberLimits')}
            </p>
          ) : limits.isError ? (
            <p role="alert" className="text-sm text-error">
              {t('memberLimitsFailed')}
            </p>
          ) : (
            <dl className="flex flex-wrap gap-x-10 gap-y-4 text-sm">
              <div>
                <dt className="text-muted-foreground">
                  {t('requestsPerMinute')}
                </dt>
                <dd className="mt-1 tabular-nums">
                  {limits.data.requests_per_minute
                    ? number.format(limits.data.requests_this_minute) +
                      ' / ' +
                      number.format(limits.data.requests_per_minute)
                    : t('unlimited')}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">
                  {t('memberConcurrency')}
                </dt>
                <dd className="mt-1 tabular-nums">
                  {number.format(limits.data.in_flight)} /{' '}
                  {limits.data.max_concurrency
                    ? number.format(limits.data.max_concurrency)
                    : t('unlimited')}
                </dd>
              </div>
            </dl>
          )}
          <p className="text-xs text-muted-foreground">
            {t('personalLimitsNotice')}
          </p>
        </section>
      )}
    </section>
  )
}
