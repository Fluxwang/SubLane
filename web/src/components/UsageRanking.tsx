import { useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/Button'

export type RankingItem = {
  id: string
  name: string
  detail?: ReactNode
  tokens: number
}

const collapsedRows = 5

export function UsageRanking({
  items,
  controls,
  notice,
  formatTokens,
}: {
  // Items are ranked by reported input plus output tokens; unreported items carry no tokens.
  items: RankingItem[]
  controls?: ReactNode
  notice?: ReactNode
  formatTokens: (tokens: number) => ReactNode
}) {
  const { t, i18n } = useTranslation()
  const id = useId()
  const [expanded, setExpanded] = useState(false)
  const ranked = items
    .filter((item) => item.tokens > 0)
    .sort((a, b) => b.tokens - a.tokens)
  const total = ranked.reduce((sum, item) => sum + item.tokens, 0)
  const visible = expanded ? ranked : ranked.slice(0, collapsedRows)
  const percent = new Intl.NumberFormat(i18n.resolvedLanguage ?? 'en', {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  return (
    <section
      aria-labelledby={id + '-title'}
      className="flex min-w-0 flex-col rounded-xl border border-border bg-card p-4 sm:p-6"
    >
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-baseline gap-2">
          <h3 id={id + '-title'} className="font-medium">
            {t('usageRanking')}
          </h3>
          <span className="text-sm text-muted-foreground">
            {t('usageRankingByTokens')}
          </span>
        </div>
        {controls}
      </div>
      {notice}
      {ranked.length === 0 ? (
        <p className="py-10 text-center text-sm text-muted-foreground">
          {t('usageRankingEmpty')}
        </p>
      ) : (
        <>
          <ol
            id={id + '-list'}
            className={
              expanded ? 'max-h-96 space-y-4 overflow-y-auto pr-1' : 'space-y-4'
            }
          >
            {visible.map((item, index) => {
              const share = item.tokens / total
              return (
                <li key={item.id} className="min-w-0">
                  <div className="flex items-baseline gap-3">
                    <span
                      className={
                        'w-5 shrink-0 text-sm tabular-nums ' +
                        (index === 0 ? 'text-info' : 'text-muted-foreground')
                      }
                    >
                      {index + 1}
                    </span>
                    <div className="min-w-0 flex-1">
                      <span
                        className="block truncate text-sm font-medium"
                        title={item.name}
                      >
                        {item.name}
                      </span>
                      {item.detail}
                    </div>
                    <span className="shrink-0 text-sm font-medium">
                      {formatTokens(item.tokens)}
                    </span>
                  </div>
                  <div className="mt-2 flex items-center gap-3 pl-8">
                    <div
                      aria-hidden="true"
                      className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted"
                    >
                      <div
                        className="h-full rounded-full bg-info"
                        style={{ width: share * 100 + '%' }}
                      />
                    </div>
                    <span className="w-12 shrink-0 text-right text-xs text-muted-foreground tabular-nums">
                      {percent.format(share)}
                    </span>
                  </div>
                </li>
              )
            })}
          </ol>
          {ranked.length > collapsedRows && (
            <Button
              variant="ghost"
              size="sm"
              className="mt-3 self-center text-muted-foreground"
              aria-expanded={expanded}
              aria-controls={id + '-list'}
              onClick={() => setExpanded((value) => !value)}
            >
              {expanded
                ? t('usageRankingCollapse')
                : t('usageRankingExpand', { count: ranked.length })}
            </Button>
          )}
        </>
      )}
    </section>
  )
}
