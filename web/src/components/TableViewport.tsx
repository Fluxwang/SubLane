import type { ComponentProps } from 'react'
import { cn } from '@/lib/cn'

export function TableViewport({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      className={cn(
        'min-h-48 flex-1 overflow-auto rounded-xl border border-border bg-card [&_thead]:sticky [&_thead]:top-0 [&_thead]:z-10 [&_thead]:bg-card',
        className,
      )}
      {...props}
    />
  )
}
