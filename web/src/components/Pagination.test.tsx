import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { Pagination } from './Pagination'

const defaults = {
  page: 10,
  pageSize: 25,
  total: 523,
  disabled: false,
  onPageChange: vi.fn(),
  onPageSizeChange: vi.fn(),
}

it('shows totals, the current range and bounded page choices', () => {
  render(<Pagination {...defaults} />)
  const nav = screen.getByRole('navigation', { name: 'Pagination' })
  expect(within(nav).getByText('Showing 226–250 of 523')).toBeTruthy()
  expect(within(nav).getByText('Page 10 of 21')).toBeTruthy()
  expect(
    within(nav)
      .getByRole('button', { name: 'Page 10' })
      .getAttribute('aria-current'),
  ).toBe('page')
  expect(
    within(nav).getAllByRole('button', { name: /^Page \d+$/ }).length,
  ).toBeLessThanOrEqual(7)
  expect(within(nav).getByRole('button', { name: 'Page 1' })).toBeTruthy()
  expect(within(nav).getByRole('button', { name: 'Page 21' })).toBeTruthy()
})

it('supports a valid jump and rejects invalid page numbers', async () => {
  const onPageChange = vi.fn()
  const user = userEvent.setup()
  render(<Pagination {...defaults} onPageChange={onPageChange} />)
  const input = screen.getByRole('spinbutton', { name: 'Go to page' })
  for (const value of ['0', '22', '1.5']) {
    await user.clear(input)
    await user.type(input, value)
    await user.click(screen.getByRole('button', { name: 'Go' }))
  }
  expect(onPageChange).not.toHaveBeenCalled()
  await user.clear(input)
  await user.type(input, '7{Enter}')
  expect(onPageChange).toHaveBeenCalledWith(7)
})

it('offers page sizes and disables navigation at the ends and while loading', async () => {
  const onPageSizeChange = vi.fn()
  const { rerender } = render(
    <Pagination {...defaults} page={1} onPageSizeChange={onPageSizeChange} />,
  )
  expect(
    (screen.getByRole('button', { name: 'Previous' }) as HTMLButtonElement)
      .disabled,
  ).toBe(true)
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Rows per page' }))
  await userEvent
    .setup()
    .click(screen.getByRole('menuitemradio', { name: '100' }))
  expect(onPageSizeChange).toHaveBeenCalledWith(100)
  rerender(<Pagination {...defaults} page={21} />)
  expect(screen.getByText('Showing 501–523 of 523')).toBeTruthy()
  expect(
    (screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement)
      .disabled,
  ).toBe(true)
  rerender(<Pagination {...defaults} disabled />)
  for (const button of screen.getAllByRole('button'))
    expect((button as HTMLButtonElement).disabled).toBe(true)
})

it('keeps an empty result honest and disables all page changes', () => {
  render(<Pagination {...defaults} page={1} total={0} />)
  expect(screen.getByText('Showing 0–0 of 0')).toBeTruthy()
  expect(screen.getByText('Page 1 of 1')).toBeTruthy()
  expect(
    (screen.getByRole('button', { name: 'Previous' }) as HTMLButtonElement)
      .disabled,
  ).toBe(true)
  expect(
    (screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement)
      .disabled,
  ).toBe(true)
})
