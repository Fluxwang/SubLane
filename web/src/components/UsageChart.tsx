import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from 'react'
import { useTranslation } from 'react-i18next'

export type TrendPoint = {
  id: string
  // null marks calls without the reports needed for this metric.
  amount: number | null
  partial: boolean
}

export function UsageChart({
  points,
  label,
  metricLabel,
  format,
  axis,
}: {
  points: TrendPoint[]
  label: string
  metricLabel: string
  format: (value: number) => string
  axis: (value: number) => string
}) {
  const { t } = useTranslation()
  if (!points.length)
    return (
      <p className="py-12 text-center text-sm text-muted-foreground">
        {t('noUsageSummary')}
      </p>
    )
  if (
    points.some((point) => point.amount === null) &&
    points.every((point) => !point.amount)
  )
    return (
      <p className="py-12 text-center text-sm text-muted-foreground">
        {t('usageNoTokenReports')}
      </p>
    )
  return (
    <DailyChart
      points={points}
      label={label}
      metricLabel={metricLabel}
      format={format}
      axis={axis}
    />
  )
}

function DailyChart({
  points,
  label,
  metricLabel,
  format,
  axis,
}: {
  points: TrendPoint[]
  label: string
  metricLabel: string
  format: (value: number) => string
  axis: (value: number) => string
}) {
  const { t, i18n } = useTranslation()
  const help = useId()
  const container = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(720)
  const [active, setActive] = useState<number | null>(null)
  useEffect(() => {
    const element = container.current
    if (!element) return
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver((entries) => {
      const next = entries[0]?.contentRect.width
      if (next) setWidth(Math.max(240, Math.round(next)))
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [])
  const locale = i18n.resolvedLanguage ?? 'en'
  const dates = new Intl.DateTimeFormat(locale, {
    month: 'short',
    day: 'numeric',
    timeZone: 'UTC',
  })
  const fullDates = new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium',
    timeZone: 'UTC',
  })
  const amounts = points.map((point) => point.amount)
  const maximum = Math.max(1e-6, ...amounts.map((amount) => amount ?? 0))
  const magnitude = 10 ** Math.floor(Math.log10(maximum / 4))
  const step =
    ([1, 2, 5, 10].find((size) => size * magnitude >= maximum / 4) ?? 10) *
    magnitude
  const top = Math.ceil(maximum / step) * step
  const left = 60,
    right = 16,
    height = 260,
    bottom = 222
  const slot = (width - left - right) / points.length
  const barWidth = Math.max(2, Math.min(48, slot * 0.6))
  const x = (index: number) => left + (index + 0.5) * slot
  const y = (amount: number) => bottom - (amount / top) * (bottom - 16)
  const ticks = Array.from(
    { length: Math.round(top / step) + 1 },
    (_, index) => index * step,
  )
  const tickCount = Math.min(points.length, width < 480 ? 3 : 6)
  const datesToShow = new Set(
    Array.from({ length: tickCount }, (_, index) =>
      tickCount === 1
        ? 0
        : Math.round((index * (points.length - 1)) / (tickCount - 1)),
    ),
  )
  const select = (event: PointerEvent<SVGSVGElement>) => {
    const bounds = event.currentTarget.getBoundingClientRect()
    const relative = ((event.clientX - bounds.left) * width) / bounds.width
    setActive(
      Math.max(
        0,
        Math.min(points.length - 1, Math.floor((relative - left) / slot)),
      ),
    )
  }
  const navigate = (event: KeyboardEvent<SVGSVGElement>) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    setActive((current) =>
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? points.length - 1
          : Math.max(
              0,
              Math.min(
                points.length - 1,
                (current ?? 0) + (event.key === 'ArrowLeft' ? -1 : 1),
              ),
            ),
    )
  }
  const selected = active === null ? null : points[active]
  return (
    <div ref={container} className="space-y-3">
      <svg
        role="img"
        aria-label={label}
        aria-describedby={help}
        tabIndex={0}
        viewBox={'0 0 ' + width + ' ' + height}
        height={height}
        className="w-full rounded-sm outline-none focus-visible:outline-2 focus-visible:outline-ring"
        onPointerMove={select}
        onPointerDown={select}
        onPointerLeave={() => setActive(null)}
        onFocus={() => setActive(points.length - 1)}
        onBlur={() => setActive(null)}
        onKeyDown={navigate}
      >
        {ticks.map((tick) => (
          <g key={tick}>
            <line
              x1={left}
              x2={width - right}
              y1={y(tick)}
              y2={y(tick)}
              stroke="var(--border)"
              strokeDasharray={tick === 0 ? undefined : '3 4'}
            />
            <text
              x={left - 10}
              y={y(tick) + 4}
              textAnchor="end"
              fontSize={12}
              fill="var(--muted-foreground)"
            >
              {axis(tick)}
            </text>
          </g>
        ))}
        {active !== null && (
          <rect
            x={x(active) - slot / 2}
            y={16}
            width={slot}
            height={bottom - 16}
            fill="var(--muted)"
          />
        )}
        {amounts.map((amount, index) =>
          amount ? (
            <rect
              key={points[index].id}
              x={x(index) - barWidth / 2}
              y={y(amount)}
              width={barWidth}
              height={Math.max(1, bottom - y(amount))}
              rx={Math.min(4, barWidth / 4)}
              fill="var(--info)"
            />
          ) : null,
        )}
        {points.map((point, index) =>
          datesToShow.has(index) ? (
            <text
              key={point.id}
              x={x(index)}
              y={height - 12}
              textAnchor="middle"
              fontSize={12}
              fill="var(--muted-foreground)"
            >
              {dates.format(Number(point.id) * 1000)}
            </text>
          ) : null,
        )}
      </svg>
      <p id={help} className="sr-only">
        {t('usageChartKeys')}
      </p>
      <div
        role="status"
        aria-live="polite"
        className="flex min-h-10 flex-wrap items-center gap-x-3 gap-y-1 text-sm"
      >
        {selected ? (
          <>
            <span className="text-muted-foreground">
              {fullDates.format(Number(selected.id) * 1000)} · {metricLabel}
            </span>
            <strong className="font-medium tabular-nums">
              {selected.amount === null
                ? t('usageTokenNotReported')
                : format(selected.amount)}
            </strong>
            {selected.partial && selected.amount !== null && (
              <span className="text-xs text-muted-foreground">
                {t('usageReportedOnly')}
              </span>
            )}
          </>
        ) : (
          <span className="text-muted-foreground">{t('usageChartHint')}</span>
        )}
      </div>
      {amounts.some((amount) => amount === null) && (
        <p className="text-xs text-muted-foreground">{t('usageChartGaps')}</p>
      )}
    </div>
  )
}
