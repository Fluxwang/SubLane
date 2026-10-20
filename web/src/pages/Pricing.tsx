import { Pagination } from '@/components/Pagination'
import { TableViewport } from '@/components/TableViewport'
import { useTablePagination } from '@/lib/pagination'
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LoaderCircle, RefreshCw, Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { pricingOptions } from '@/lib/pricing'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'

const sourceLabels = {
  remote: 'pricingSourceRemote',
  cache: 'pricingSourceCache',
  fallback: 'pricingSourceFallback',
  override: 'pricingSourceOverride',
} as const

export function Pricing() {
  const { t, i18n } = useTranslation()
  const client = useQueryClient()
  const query = useQuery(pricingOptions(client))
  const [search, setSearch] = useState('')
  const {
    page: requestedPage,
    pageSize,
    viewport,
    changePage,
    changePageSize,
    syncPage,
  } = useTablePagination()
  const filter = search.trim().toLowerCase()
  const prices = (query.data?.prices ?? []).filter((price) =>
    price.model.toLowerCase().includes(filter),
  )
  const page = Math.min(
    requestedPage,
    Math.max(1, Math.ceil(prices.length / pageSize)),
  )
  syncPage(page)
  const offset = (page - 1) * pageSize
  const dollars = new Intl.NumberFormat(i18n.resolvedLanguage ?? 'en', {
    style: 'currency',
    currency: 'USD',
    currencyDisplay: 'narrowSymbol',
    minimumFractionDigits: 0,
    maximumFractionDigits: 6,
  })
  // Rates already cover one million tokens; only convert micro-USD to USD here.
  const format = (value: number) => dollars.format(value / 1_000_000)

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <h1 className="page-title">{t('modelPrices')}</h1>
        <Button
          variant="outline"
          disabled={query.isFetching}
          onClick={() => query.refetch()}
        >
          <RefreshCw
            aria-hidden="true"
            className={
              query.isFetching ? 'motion-safe:animate-spin' : undefined
            }
          />
          {t('refresh')}
        </Button>
      </div>
      <p className="max-w-3xl text-sm leading-6 text-muted-foreground">
        {t('pricingDescription')}
      </p>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="relative w-full sm:max-w-sm">
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            className="pl-9"
            value={search}
            disabled={!query.data?.prices.length}
            placeholder={t('pricingSearch')}
            aria-label={t('pricingSearch')}
            onChange={(event) => {
              setSearch(event.target.value)
              changePage(1)
            }}
          />
        </div>
        <p className="text-sm text-muted-foreground">{t('pricingUnit')}</p>
      </div>
      {query.isError && (
        <p role="alert" className="text-sm text-warning">
          {t(query.data ? 'pricingReloadFailed' : 'pricingLoadFailed')}
        </p>
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
          {t('pricingLoading')}
        </p>
      ) : query.data ? (
        query.data.prices.length === 0 ? (
          <p className="rounded-xl border border-border p-6 text-sm text-muted-foreground">
            {t('pricingEmpty')}
          </p>
        ) : prices.length === 0 ? (
          <p
            role="status"
            className="rounded-xl border border-border p-6 text-sm text-muted-foreground"
          >
            {t('pricingNoMatch')}
          </p>
        ) : (
          <>
            <TableViewport ref={viewport} aria-busy={query.isFetching}>
              <table
                role="table"
                aria-label={t('modelPrices')}
                className="block w-full text-sm sm:table"
              >
                <thead
                  role="rowgroup"
                  className="hidden bg-muted/50 text-xs text-muted-foreground sm:table-header-group"
                >
                  <tr role="row">
                    <th
                      role="columnheader"
                      scope="col"
                      className="px-5 py-3 text-left font-medium"
                    >
                      {t('pricingModel')}
                    </th>
                    <th
                      role="columnheader"
                      scope="col"
                      className="px-5 py-3 text-right font-medium"
                    >
                      {t('pricingInput')}
                    </th>
                    <th
                      role="columnheader"
                      scope="col"
                      className="px-5 py-3 text-right font-medium"
                    >
                      {t('pricingCached')}
                    </th>
                    <th
                      role="columnheader"
                      scope="col"
                      className="px-5 py-3 text-right font-medium"
                    >
                      {t('pricingOutput')}
                    </th>
                    <th
                      role="columnheader"
                      scope="col"
                      className="px-5 py-3 text-left font-medium"
                    >
                      {t('pricingSource')}
                    </th>
                  </tr>
                </thead>
                <tbody
                  role="rowgroup"
                  className="block divide-y divide-border sm:table-row-group"
                >
                  {prices.slice(offset, offset + pageSize).map((price) => (
                    <tr
                      role="row"
                      key={price.model}
                      className="grid grid-cols-3 gap-x-3 gap-y-3 p-4 sm:table-row sm:p-0"
                    >
                      <td
                        role="cell"
                        className="col-span-3 min-w-0 sm:px-5 sm:py-4"
                      >
                        <code className="break-all text-xs">{price.model}</code>
                      </td>
                      {(['input', 'cached', 'output'] as const).map((kind) => (
                        <td
                          role="cell"
                          key={kind}
                          className="min-w-0 tabular-nums sm:px-5 sm:py-4 sm:text-right"
                        >
                          <span className="mb-1 block text-xs text-muted-foreground sm:hidden">
                            {t(
                              kind === 'input'
                                ? 'pricingInput'
                                : kind === 'cached'
                                  ? 'pricingCached'
                                  : 'pricingOutput',
                            )}
                          </span>
                          {format(price[kind])}
                        </td>
                      ))}
                      <td
                        role="cell"
                        className="col-span-3 text-xs text-muted-foreground sm:px-5 sm:py-4"
                      >
                        <span className="sm:hidden">
                          {t('pricingSource')}:{' '}
                        </span>
                        {t(
                          sourceLabels[
                            price.source as keyof typeof sourceLabels
                          ] ?? 'pricingSourceUnknown',
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableViewport>
          </>
        )
      ) : null}
      {query.data && (
        <Pagination
          page={page}
          pageSize={pageSize}
          total={prices.length}
          disabled={query.isFetching}
          onPageChange={changePage}
          onPageSizeChange={changePageSize}
        />
      )}
    </div>
  )
}
