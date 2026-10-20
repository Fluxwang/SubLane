import { useRef, useState } from 'react'
import type { QueryClient, QueryKey } from '@tanstack/react-query'
import { z } from 'zod'

export const paginationFields = {
  total: z.number().int().nonnegative(),
  page: z.number().int().positive(),
  page_size: z.number().int().min(1).max(100),
  next_cursor: z.number().int().nonnegative(),
}

export function cachePage<T extends { page: number }>(
  client: QueryClient,
  key: (page: number) => QueryKey,
  requested: number,
  data: T,
) {
  // Retention or deletion can move a page backward; its destination cache must use this snapshot.
  if (data.page !== requested) client.setQueryData(key(data.page), data)
}

export function useTablePagination() {
  const [page, setPage] = useState(1)
  const [pageSize, setSize] = useState(50)
  const viewport = useRef<HTMLDivElement | null>(null)
  const changePage = (value: number) => {
    setPage(value)
    if (viewport.current) viewport.current.scrollTop = 0
  }
  return {
    page,
    pageSize,
    viewport,
    changePage,
    changePageSize: (size: number) => {
      setSize(size)
      changePage(1)
    },
    syncPage: (value: number | undefined, placeholder = false) => {
      // Follow a server-clamped page only when it belongs to the completed query.
      if (value !== undefined && !placeholder && value !== page) setPage(value)
    },
  }
}
