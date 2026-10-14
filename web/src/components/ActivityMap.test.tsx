import { fireEvent, render, screen, within } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ActivityMap } from './ActivityMap'
import type { Activity } from '@/lib/statistics'

const activity: Activity = {
  tracking_since: 1900000000,
  observed_until: 1900604800,
  cells: Array.from({ length: 168 }, (_, index) => ({
    weekday: Math.floor(index / 24),
    hour: index % 24,
    samples: index > 0 && index < 4 ? 1 : 0,
    requests: index === 2 || index === 3 ? 2 : 0,
    input_tokens: index === 3 ? 8 : 0,
    output_tokens: 0,
    input_reported: index === 3 ? 1 : 0,
    output_reported: 0,
  })),
}

it('distinguishes observed zero activity from hours without collected data', () => {
  render(<ActivityMap activity={activity} metric="requests" />)
  expect(screen.getAllByRole('gridcell')).toHaveLength(168)
  fireEvent.click(screen.getByRole('gridcell', { name: /Mon 00:00–01:00/ }))
  expect(
    within(screen.getByRole('status')).getByText('Not collected'),
  ).toBeTruthy()
  fireEvent.click(screen.getByRole('gridcell', { name: /Mon 01:00–02:00/ }))
  expect(within(screen.getByRole('status')).getByText('0')).toBeTruthy()
})

it('supports keyboard navigation and preserves unknown or partial token values', () => {
  const { rerender } = render(
    <ActivityMap activity={activity} metric="tokens" />,
  )
  const zero = screen.getByRole('gridcell', { name: /Mon 01:00–02:00/ })
  zero.focus()
  fireEvent.keyDown(zero, { key: 'ArrowRight' })
  expect(document.activeElement).toBe(
    screen.getByRole('gridcell', { name: /Mon 02:00–03:00/ }),
  )
  expect(
    within(screen.getByRole('status')).getByText('Not reported'),
  ).toBeTruthy()
  fireEvent.keyDown(document.activeElement!, { key: 'ArrowRight' })
  expect(within(screen.getByRole('status')).getByText('8')).toBeTruthy()
  expect(
    within(screen.getByRole('status')).getByText('Reported values only'),
  ).toBeTruthy()
  rerender(<ActivityMap activity={activity} metric="requests" />)
  expect(within(screen.getByRole('status')).getByText('2')).toBeTruthy()
  fireEvent.keyDown(document.activeElement!, { key: 'ArrowDown' })
  expect(document.activeElement).toBe(
    screen.getByRole('gridcell', { name: /Tue 03:00–04:00/ }),
  )
})
