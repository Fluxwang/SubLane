import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { createQueryClient } from '@/lib/query'
import { DeleteScheme } from './DeleteScheme'

function mount(onDeleted = vi.fn().mockResolvedValue(undefined)) {
  render(
    <QueryClientProvider client={createQueryClient()}>
      <DeleteScheme id={3} name="Synthetic allowance" onDeleted={onDeleted} />
    </QueryClientProvider>,
  )
  return onDeleted
}

it('deletes a paused scheme after confirmation', async () => {
  const fetcher = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
  vi.stubGlobal('fetch', fetcher)
  const user = userEvent.setup()
  const onDeleted = mount()
  await user.click(
    screen.getByRole('button', { name: 'Delete Synthetic allowance' }),
  )
  expect(
    await screen.findByText(/Keys bound to this allowance keep working/),
  ).toBeTruthy()
  await user.click(screen.getByRole('button', { name: 'Delete allowance' }))
  expect(fetcher).toHaveBeenCalledWith(
    '/api/allocations/3',
    expect.objectContaining({ method: 'DELETE' }),
  )
  expect(onDeleted).toHaveBeenCalledOnce()
})

it('explains why a scheme with in-flight requests cannot be deleted', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: 'allocation_in_use' }), {
        status: 409,
      }),
    ),
  )
  const user = userEvent.setup()
  const onDeleted = mount()
  await user.click(
    screen.getByRole('button', { name: 'Delete Synthetic allowance' }),
  )
  await user.click(
    await screen.findByRole('button', { name: 'Delete allowance' }),
  )
  expect(
    await screen.findByText(
      'Requests are still running on this allowance. Try again after they finish.',
    ),
  ).toBeTruthy()
  expect(onDeleted).not.toHaveBeenCalled()
})
