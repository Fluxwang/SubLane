import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { createQueryClient } from '@/lib/query'
import { authKey } from '@/lib/auth'
import { authenticated } from '@/test/fixtures'
import { CatalogDialog } from './CatalogDialog'

it('inspects a pool model on demand and explains an unavailable snapshot', async () => {
  const fetcher = vi.fn((url: string) =>
    Promise.resolve(
      new Response(
        JSON.stringify(
          url.includes('/availability?')
            ? {
                model: 'synthetic-basic',
                state: 'unavailable',
                available: 0,
                unknown: 0,
                retry_at: 1900000060,
                server_time: 1900000010,
                reasons: [{ code: 'quota_exhausted', count: 1 }],
                accounts: [
                  {
                    id: 'synthetic-account',
                    name: 'Synthetic subscription',
                    provider: 'codex',
                    reason: 'quota_exhausted',
                    quota_state: 'exhausted',
                    retry_at: 1900000060,
                    in_flight: 0,
                    max_concurrency: 10,
                  },
                ],
              }
            : {
                models: [
                  {
                    id: 'synthetic-basic',
                    object: 'model',
                    owned_by: 'openai',
                  },
                ],
                known_accounts: 1,
                unknown_accounts: 0,
                stale_accounts: 0,
                refreshing: false,
                refresh_failed: false,
                server_time: 1900000010,
              },
        ),
      ),
    ),
  )
  vi.stubGlobal('fetch', fetcher)
  const client = createQueryClient()
  client.setQueryData(authKey, authenticated)
  render(
    <QueryClientProvider client={client}>
      <CatalogDialog target={{ kind: 'group', id: 7 }} name="Synthetic pool" />
    </QueryClientProvider>,
  )
  const user = userEvent.setup()
  await user.click(
    screen.getByRole('button', { name: 'Models for Synthetic pool' }),
  )
  await user.click(
    await screen.findByRole('button', { name: 'Inspect synthetic-basic' }),
  )
  expect(await screen.findByText('No eligible account')).toBeTruthy()
  expect(screen.getByText('Synthetic subscription')).toBeTruthy()
  expect(
    fetcher.mock.calls.filter(([url]) =>
      String(url).includes('/availability?'),
    ),
  ).toHaveLength(1)
})

const snapshot = {
  models: ['synthetic-basic'],
  updated_at: 1900000000,
  server_time: 1900000010,
  known: true,
  usable: true,
  stale: false,
  refreshing: false,
  refresh_failed: false,
  retry_after_seconds: 0,
}
function mount() {
  const client = createQueryClient()
  client.setQueryData(authKey, authenticated)
  render(
    <QueryClientProvider client={client}>
      <CatalogDialog
        target={{ kind: 'account', id: 'synthetic-account' }}
        name="Synthetic subscription"
      />
    </QueryClientProvider>,
  )
  return client
}
it('loads on demand and keeps the last successful catalog after a refresh failure', async () => {
  const fetch = vi
    .fn()
    .mockResolvedValue(new Response(JSON.stringify(snapshot)))
  vi.stubGlobal('fetch', fetch)
  const user = userEvent.setup()
  mount()
  expect(fetch).not.toHaveBeenCalled()
  await user.click(
    screen.getByRole('button', { name: 'Models for Synthetic subscription' }),
  )
  expect(await screen.findByText('synthetic-basic')).toBeTruthy()
  fetch.mockResolvedValueOnce(
    new Response(
      JSON.stringify({
        ...snapshot,
        stale: true,
        refresh_failed: true,
        retry_after_seconds: 30,
      }),
    ),
  )
  await user.click(screen.getByRole('button', { name: 'Refresh models' }))
  expect(
    await screen.findByText('Refresh failed. Showing the last saved catalog.'),
  ).toBeTruthy()
  expect(screen.getByText('synthetic-basic')).toBeTruthy()
})
it('shows unknown capability without claiming the account supports no models', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          ...snapshot,
          models: [],
          updated_at: 0,
          known: false,
          usable: false,
          stale: true,
          refresh_failed: true,
        }),
      ),
    ),
  )
  const user = userEvent.setup()
  mount()
  await user.click(
    screen.getByRole('button', { name: 'Models for Synthetic subscription' }),
  )
  expect(
    await screen.findByText(
      'Model support is unknown. Refresh to retrieve the account catalog.',
    ),
  ).toBeTruthy()
  expect(
    screen.queryByText('This account reported no visible models.'),
  ).toBeNull()
})

it('ignores a late refresh failure after the active identity changes', async () => {
  let resolve!: (value: Response) => void
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response(JSON.stringify(snapshot)))
    .mockImplementation(
      () =>
        new Promise<Response>((done) => {
          resolve = done
        }),
    )
  vi.stubGlobal('fetch', fetch)
  const user = userEvent.setup()
  const client = mount()
  await user.click(
    screen.getByRole('button', { name: 'Models for Synthetic subscription' }),
  )
  await screen.findByText('synthetic-basic')
  await user.click(screen.getByRole('button', { name: 'Refresh models' }))
  await act(async () => {
    client.setQueryData(authKey, {
      ...authenticated,
      user: { ...authenticated.user, id: 42, role: 'member' },
    })
    resolve(
      new Response(JSON.stringify({ error: 'unauthorized' }), { status: 401 }),
    )
  })
  await waitFor(() => expect(client.isMutating()).toBe(0))
  expect(client.getQueryData<typeof authenticated>(authKey)?.user?.id).toBe(42)
})
it('lists hidden account models separately from public models', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          ...snapshot,
          hidden_models: ['synthetic-internal'],
        }),
      ),
    ),
  )
  const user = userEvent.setup()
  mount()
  await user.click(
    screen.getByRole('button', { name: 'Models for Synthetic subscription' }),
  )
  const hidden = await screen.findByRole('list', { name: 'Hidden models' })
  expect(hidden.textContent).toContain('synthetic-internal')
  expect(
    screen.getByRole('list', { name: 'Available models' }).textContent,
  ).not.toContain('synthetic-internal')
  await user.type(screen.getByLabelText('Search models'), 'basic')
  expect(screen.queryByRole('list', { name: 'Hidden models' })).toBeNull()
})
