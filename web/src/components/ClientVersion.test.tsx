import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'
import { createQueryClient } from '@/lib/query'
import { authKey } from '@/lib/auth'
import { authenticated, memberAuthenticated } from '@/test/fixtures'
import { Settings } from '@/pages/Settings'
import { ClientVersion } from './ClientVersion'

const initial = {
  manual_version: '',
  auto_sync: true,
  effective_version: '0.100.0',
  default_version: '0.100.0',
  latest_version: '',
  source: 'builtin',
  checked_at: 0,
  successful_at: 0,
  next_check_at: 0,
  server_time: 1900000000,
  retry_after_seconds: 0,
  syncing: false,
  sync_failed: false,
}
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status })
function mount(provider: 'codex' | 'grok' | 'claude' = 'codex') {
  const client = createQueryClient()
  client.setQueryData(authKey, authenticated)
  render(
    <QueryClientProvider client={client}>
      <ClientVersion userID={1} provider={provider} />
    </QueryClientProvider>,
  )
  return client
}
it.each(['codex', 'grok', 'claude'] as const)(
  'saves a %s manual version and keeps it selected after synchronization',
  async (provider) => {
    const pinned = {
      ...initial,
      manual_version: '0.150.0',
      auto_sync: false,
      effective_version: '0.150.0',
      source: 'manual',
    }
    const fetch = vi
      .fn()
      .mockImplementation((_url: string, init?: RequestInit) =>
        Promise.resolve(
          response(
            init?.method === 'PATCH'
              ? pinned
              : init?.method === 'POST'
                ? {
                    ...pinned,
                    latest_version: '0.200.0',
                    checked_at: 1900000010,
                    successful_at: 1900000010,
                    retry_after_seconds: 60,
                  }
                : initial,
          ),
        ),
      )
    vi.stubGlobal('fetch', fetch)
    const user = userEvent.setup()
    mount(provider)
    await user.type(await screen.findByLabelText('Manual version'), '0.150.0')
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Automatically follow stable releases',
      }),
    )
    await user.click(screen.getByRole('button', { name: 'Save settings' }))
    expect(await screen.findByText('Settings saved.')).toBeTruthy()
    const saved = fetch.mock.calls.find(([, init]) => init?.method === 'PATCH')
    expect(saved![0]).toBe('/api/settings/' + provider)
    expect(JSON.parse(saved![1]!.body as string)).toEqual({
      manual_version: '0.150.0',
      auto_sync: false,
    })
    await user.click(screen.getByRole('button', { name: 'Check now' }))
    expect(await screen.findByText('0.200.0')).toBeTruthy()
    expect(
      fetch.mock.calls.find(([, init]) => init?.method === 'POST')?.[0],
    ).toBe('/api/settings/' + provider + '/sync')
    expect(
      (screen.getByLabelText('Manual version') as HTMLInputElement).value,
    ).toBe('0.150.0')
    expect(screen.getByText('0.150.0')).toBeTruthy()
  },
)
it('rejects malformed versions before sending a configuration change', async () => {
  const fetch = vi
    .fn()
    .mockImplementation(() => Promise.resolve(response(initial)))
  vi.stubGlobal('fetch', fetch)
  const user = userEvent.setup()
  mount()
  await user.type(await screen.findByLabelText('Manual version'), 'v0.150-beta')
  await user.click(screen.getByRole('button', { name: 'Save settings' }))
  expect(await screen.findByRole('alert')).toHaveProperty(
    'textContent',
    'Enter a stable version in major.minor.patch format.',
  )
  expect(
    fetch.mock.calls.some(
      ([, init]) => (init as RequestInit | undefined)?.method === 'PATCH',
    ),
  ).toBe(false)
})
it('does not let an old save failure sign out a replacement identity', async () => {
  let resolve!: (value: Response) => void
  const fetch = vi
    .fn()
    .mockImplementation((_url: string, init?: RequestInit) =>
      init?.method === 'PATCH'
        ? new Promise<Response>((done) => {
            resolve = done
          })
        : Promise.resolve(response(initial)),
    )
  vi.stubGlobal('fetch', fetch)
  const user = userEvent.setup()
  const client = mount()
  await user.type(await screen.findByLabelText('Manual version'), '0.150.0')
  await user.click(screen.getByRole('button', { name: 'Save settings' }))
  await act(async () => {
    client.setQueryData(authKey, memberAuthenticated)
    resolve(response({ error: 'unauthorized' }, 401))
  })
  await waitFor(() => expect(client.isMutating()).toBe(0))
  expect(
    client.getQueryData<typeof memberAuthenticated>(authKey)?.user?.id,
  ).toBe(memberAuthenticated.user.id)
})
it('does not render or fetch instance version settings for members', async () => {
  const fetch = vi
    .fn()
    .mockImplementation(() => Promise.resolve(response(memberAuthenticated)))
  vi.stubGlobal('fetch', fetch)
  const client = createQueryClient()
  client.setQueryData(authKey, memberAuthenticated)
  render(
    <QueryClientProvider client={client}>
      <Settings />
    </QueryClientProvider>,
  )
  expect(
    await screen.findByRole('heading', { name: 'Appearance' }),
  ).toBeTruthy()
  expect(
    screen.queryByRole('heading', { name: 'Codex client version' }),
  ).toBeNull()
  expect(
    fetch.mock.calls.some(([url]) => String(url).startsWith('/api/settings')),
  ).toBe(false)
})
