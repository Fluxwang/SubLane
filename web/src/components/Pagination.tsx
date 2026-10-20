import { useState } from 'react'
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  MoreHorizontal,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from './ui/Button'
import { Input } from './ui/Input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from './ui/DropdownMenu'

function pageNumbers(page: number, total: number) {
  if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1)
  const start = Math.max(2, Math.min(page - 1, total - 3))
  const end = Math.min(total - 1, Math.max(page + 1, 4))
  return [
    1,
    ...(start > 2 ? ['before' as const] : []),
    ...Array.from({ length: end - start + 1 }, (_, index) => start + index),
    ...(end < total - 1 ? ['after' as const] : []),
    total,
  ]
}

export function Pagination({
  page,
  pageSize,
  total,
  disabled = false,
  onPageChange,
  onPageSizeChange,
}: {
  page: number
  pageSize: number
  total: number
  disabled?: boolean
  onPageChange: (page: number) => void
  onPageSizeChange: (size: number) => void
}) {
  const { t, i18n } = useTranslation()
  const [jump, setJump] = useState('')
  const pages = Math.max(1, Math.ceil(total / pageSize))
  const numbers = new Intl.NumberFormat(i18n.resolvedLanguage ?? 'en')
  return (
    <nav
      aria-label={t('pagination')}
      className="flex shrink-0 flex-wrap items-center justify-between gap-x-6 gap-y-3"
    >
      <div className="flex flex-wrap items-center gap-3">
        <p role="status" className="text-sm text-muted-foreground tabular-nums">
          {t('paginationRange', {
            start: numbers.format(total === 0 ? 0 : (page - 1) * pageSize + 1),
            end: numbers.format(Math.min(page * pageSize, total)),
            total: numbers.format(total),
          })}
        </p>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="outline"
              aria-label={t('paginationSize')}
              disabled={disabled}
            >
              <span className="hidden sm:inline">{t('paginationSize')}</span>
              {numbers.format(pageSize)}
              <ChevronDown aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuRadioGroup
              value={String(pageSize)}
              onValueChange={(value) => onPageSizeChange(Number(value))}
            >
              {[10, 25, 50, 100].map((size) => (
                <DropdownMenuRadioItem key={size} value={String(size)}>
                  {numbers.format(size)}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-sm text-muted-foreground tabular-nums">
          {t('paginationPage', {
            page: numbers.format(page),
            pages: numbers.format(pages),
          })}
        </span>
        <div className="flex items-center gap-1">
          <Button
            variant="outline"
            size="icon"
            className="[@media(pointer:coarse)]:size-11"
            aria-label={t('previousPage')}
            title={t('previousPage')}
            disabled={disabled || page === 1}
            onClick={() => onPageChange(page - 1)}
          >
            <ChevronLeft aria-hidden="true" />
          </Button>
          <div className="hidden items-center gap-1 sm:flex">
            {pageNumbers(page, pages).map((number) =>
              typeof number === 'number' ? (
                <Button
                  key={number}
                  variant={number === page ? 'default' : 'outline'}
                  size="icon"
                  className="[@media(pointer:coarse)]:size-11"
                  aria-label={t('paginationNumber', { page: number })}
                  aria-current={number === page ? 'page' : undefined}
                  disabled={disabled || total === 0}
                  onClick={() => onPageChange(number)}
                >
                  {numbers.format(number)}
                </Button>
              ) : (
                <span
                  key={number}
                  aria-hidden="true"
                  className="flex size-9 items-center justify-center text-muted-foreground"
                >
                  <MoreHorizontal className="size-4" />
                </span>
              ),
            )}
          </div>
          <Button
            variant="outline"
            size="icon"
            className="[@media(pointer:coarse)]:size-11"
            aria-label={t('nextPage')}
            title={t('nextPage')}
            disabled={disabled || page === pages}
            onClick={() => onPageChange(page + 1)}
          >
            <ChevronRight aria-hidden="true" />
          </Button>
        </div>
        <form
          className="flex items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            const target = Number(jump)
            if (
              !disabled &&
              Number.isInteger(target) &&
              target >= 1 &&
              target <= pages
            ) {
              onPageChange(target)
              setJump('')
            }
          }}
        >
          <label className="flex items-center gap-2 text-sm text-muted-foreground">
            <span className="sr-only sm:not-sr-only">
              {t('paginationJump')}
            </span>
            <Input
              type="number"
              min={1}
              max={pages}
              step={1}
              required
              inputMode="numeric"
              value={jump}
              placeholder={String(page)}
              onChange={(event) => setJump(event.target.value)}
              disabled={disabled || total === 0}
              className="w-16 tabular-nums"
            />
          </label>
          <Button
            type="submit"
            variant="outline"
            disabled={disabled || total === 0 || !jump}
          >
            {t('paginationGo')}
          </Button>
        </form>
      </div>
    </nav>
  )
}
