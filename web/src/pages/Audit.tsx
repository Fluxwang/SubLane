import { Pagination } from '@/components/Pagination'
import { TableViewport } from '@/components/TableViewport'
import { useTablePagination } from '@/lib/pagination'
import { useState } from 'react'
import {
  keepPreviousData,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { ChevronDown, LoaderCircle, RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { authOptions } from '@/lib/auth'
import {
  auditOptions,
  auditActions,
  auditResources,
  type AuditResource,
  type AuditOutcome,
} from '@/lib/audit'
import { Button } from '@/components/ui/Button'
import { Status } from '@/components/Status'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/DropdownMenu'

import { useTimeZone } from '@/lib/timezone'
export function Audit() {
  const { data } = useQuery(authOptions())
  if (data?.user?.role !== 'admin') return null
  return <AuditLog key={data.user.id} />
}
function AuditLog() {
  const { t, i18n } = useTranslation()
  const timeZone = useTimeZone()
  const client = useQueryClient()
  const { page, pageSize, viewport, changePage, changePageSize, syncPage } =
    useTablePagination()
  const [resource, setResource] = useState<AuditResource>('')
  const [outcome, setOutcome] = useState<AuditOutcome>('')
  const query = useQuery({
    ...auditOptions(client, page, resource, outcome, pageSize),
    placeholderData: keepPreviousData,
  })
  syncPage(query.data?.page, query.isPlaceholderData)
  const dates = new Intl.DateTimeFormat(i18n.resolvedLanguage ?? 'en', {
    timeZone,
    dateStyle: 'short',
    timeStyle: 'medium',
  })
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="page-title">{t('auditLog')}</h1>
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
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" aria-label={t('auditResourceFilter')}>
              {resource ? t(auditResources[resource]) : t('auditAllResources')}
              <ChevronDown aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuRadioGroup
              value={resource}
              onValueChange={(value) => {
                setResource(value as AuditResource)
                changePage(1)
              }}
            >
              <DropdownMenuRadioItem value="">
                {t('auditAllResources')}
              </DropdownMenuRadioItem>
              {(
                Object.keys(auditResources) as (keyof typeof auditResources)[]
              ).map((value) => (
                <DropdownMenuRadioItem value={value} key={value}>
                  {t(auditResources[value])}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" aria-label={t('requestResultFilter')}>
              {t(
                outcome === 'success'
                  ? 'auditSuccess'
                  : outcome === 'failure'
                    ? 'auditFailure'
                    : 'allRequestOutcomes',
              )}
              <ChevronDown aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuRadioGroup
              value={outcome}
              onValueChange={(value) => {
                setOutcome(value as AuditOutcome)
                changePage(1)
              }}
            >
              {(['', 'success', 'failure'] as const).map((value) => (
                <DropdownMenuRadioItem value={value} key={value}>
                  {t(
                    value === 'success'
                      ? 'auditSuccess'
                      : value === 'failure'
                        ? 'auditFailure'
                        : 'allRequestOutcomes',
                  )}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      {query.isPending ? (
        <p
          role="status"
          className="flex items-center gap-2 py-10 text-sm text-muted-foreground"
        >
          <LoaderCircle
            aria-hidden="true"
            className="size-4 motion-safe:animate-spin"
          />
          {t('auditLoading')}
        </p>
      ) : query.isError ? (
        <div role="alert" className="space-y-3">
          <p className="text-sm text-error">{t('auditLoadFailed')}</p>
          <Button variant="outline" onClick={() => query.refetch()}>
            {t('reconnect')}
          </Button>
        </div>
      ) : query.data.events.length === 0 ? (
        <div className="rounded-xl border border-border p-10 text-center">
          <h2 className="font-medium">{t('auditEmpty')}</h2>
          <p className="mt-2 text-sm text-muted-foreground">
            {t('auditEmptyHint')}
          </p>
        </div>
      ) : (
        <TableViewport ref={viewport} aria-busy={query.isFetching}>
          <table className="w-full text-left text-sm">
            <thead className="border-b border-border text-muted-foreground">
              <tr>
                {(
                  [
                    'requestTime',
                    'auditActor',
                    'auditAction',
                    'auditTarget',
                    'requestResult',
                  ] as const
                ).map((label) => (
                  <th
                    key={label}
                    scope="col"
                    className="whitespace-nowrap px-5 py-3 font-medium"
                  >
                    {t(label)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {query.data.events.map((event) => (
                <tr key={event.id}>
                  <td className="whitespace-nowrap px-5 py-4 text-xs text-muted-foreground tabular-nums">
                    {dates.format(event.created_at * 1000)}
                  </td>
                  <td className="px-5 py-4">
                    <p className="max-w-48 break-words font-medium">
                      {event.actor_name}
                    </p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {event.source === 'local'
                        ? t('auditLocal')
                        : t(
                            event.actor_role === 'admin'
                              ? 'administrator'
                              : 'member',
                          )}
                    </p>
                  </td>
                  <td className="whitespace-nowrap px-5 py-4">
                    {auditActions[event.action]
                      ? t(auditActions[event.action])
                      : event.action}
                  </td>
                  <td className="px-5 py-4">
                    <p className="whitespace-nowrap">
                      {t(auditResources[event.resource])}
                    </p>
                    <code
                      className="mt-1 block max-w-40 truncate text-xs text-muted-foreground"
                      title={event.resource_id}
                    >
                      {event.resource_id ? `#${event.resource_id}` : '—'}
                    </code>
                  </td>
                  <td className="px-5 py-4">
                    <Status
                      kind={event.outcome === 'success' ? 'success' : 'error'}
                    >
                      {t(
                        event.outcome === 'success'
                          ? 'auditSuccess'
                          : 'auditFailure',
                      )}
                    </Status>
                    {event.http_status !== null && (
                      <p className="mt-1 text-xs text-muted-foreground">
                        HTTP {event.http_status}
                      </p>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableViewport>
      )}
      <p className="text-xs leading-5 text-muted-foreground">
        {t('auditRetention')}
      </p>
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
