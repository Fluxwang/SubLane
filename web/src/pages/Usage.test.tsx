import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { expect, it, vi } from 'vitest'
import { App } from '@/App'
import { createAppRouter } from '@/router'
import {
  authenticated,
  memberAuthenticated,
  hourlyActivity,
  system,
  workspaces,
} from '@/test/fixtures'

const metrics = {
  requests: 2,
  completed: 1,
  incomplete: 0,
  errors: 1,
  canceled: 0,
  rejected: 0,
  duration_ms: 840,
  input_tokens: 120,
  output_tokens: 38,
  cached_tokens: 0,
  input_reported: 1,
  output_reported: 1,
  cached_reported: 0,
}
const summary = {
  activity: hourlyActivity,
  from_day: 1900000000,
  to_day: 1900086400,
  tracking_since: 1900000000,
  totals: metrics,
  days: [{ id: '1900000000', name: '2030-03-17', ...metrics }],
  members: [],
  models: [
    { id: 'codex/synthetic-model', name: 'codex/synthetic-model', ...metrics },
  ],
  groups: [],
  keys: [],
  key_tracking_since: 1900000000,
  cost: {
    estimated_micro_usd: 0,
    unpriced_tokens: 0,
    days: [{ id: '1900000000', estimated_micro_usd: 0 }],
  },
}

it.each([
  ['/', memberAuthenticated, '/api/me/usage'],
  ['/admin/usage', authenticated, '/api/usage'],
] as const)(
  'shows honest usage totals at %s and changes the period',
  async (path, session, endpoint) => {
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url === '/api/auth/state')
        return Promise.resolve(new Response(JSON.stringify(session)))
      if (url === '/api/workspaces')
        return Promise.resolve(new Response(JSON.stringify(workspaces)))
      if (url === '/api/connection')
        return Promise.resolve(
          new Response(JSON.stringify({ status: 'ready' })),
        )
      if (url === '/api/me/allocations')
        return Promise.resolve(new Response(JSON.stringify({ schemes: [] })))
      if (url === '/api/me/limits')
        return Promise.resolve(
          new Response(
            JSON.stringify({
              user_id: 2,
              requests_per_minute: 0,
              max_concurrency: 0,
              in_flight: 0,
              requests_this_minute: 0,
              reset_at: 1900000060,
            }),
          ),
        )
      if (url.startsWith(endpoint + '?'))
        return Promise.resolve(new Response(JSON.stringify(summary)))
      return Promise.reject(new Error('Unexpected privileged data request'))
    })
    vi.stubGlobal('fetch', fetch)
    const user = userEvent.setup()
    render(
      <App
        router={createAppRouter(
          createMemoryHistory({ initialEntries: [path] }),
        )}
      />,
    )
    await screen.findByRole('heading', {
      name: path === '/' ? 'Your usage' : 'Team usage',
    })
    if (path === '/') {
      expect(
        screen.getByRole('heading', { name: 'Your workspace', level: 1 }),
      ).toBeTruthy()
      expect(
        screen.getByRole('heading', { name: 'Your usage', level: 2 }),
      ).toBeTruthy()
      expect(screen.queryByRole('link', { name: 'Usage' })).toBeNull()
    }
    await screen.findByText(
      'Some calls did not report token usage; totals include reported values only.',
    )
    await screen.findByRole('img', { name: 'Usage trend · Tokens' })
    await screen.findByRole('region', { name: 'Activity by time' })
    expect(screen.getAllByRole('gridcell')).toHaveLength(168)
    if (path === '/')
      await screen.findByText('No resource allowances assigned.')
    const reads = fetch.mock.calls.filter(
      ([url]) => url === endpoint + '?days=7',
    ).length
    await user.click(
      screen.getByRole('button', {
        name: path === '/' ? 'Your usage: Refresh' : 'Refresh',
      }),
    )
    await waitFor(() =>
      expect(
        fetch.mock.calls.filter(([url]) => url === endpoint + '?days=7').length,
      ).toBeGreaterThan(reads),
    )
    expect(screen.queryByRole('table')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'View details' }))
    expect(screen.getByRole('table', { name: 'Daily' })).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Hide details' }))
    await user.click(screen.getByRole('button', { name: 'Usage period' }))
    await user.click(
      screen.getByRole('menuitemradio', { name: 'Last 30 days' }),
    )
    await waitFor(() =>
      expect(
        fetch.mock.calls.some(([url]) => url === endpoint + '?days=30'),
      ).toBe(true),
    )
    await user.click(screen.getByRole('button', { name: 'Breakdown' }))
    await user.click(screen.getByRole('menuitemradio', { name: 'Models' }))
    await user.click(screen.getByRole('button', { name: 'View details' }))
    await within(screen.getByRole('table', { name: 'Models' })).findByText(
      'codex/synthetic-model',
    )
    expect(
      screen.getByRole('region', { name: 'Activity by time' }),
    ).toBeTruthy()
    expect(
      fetch.mock.calls.every(
        ([url]) =>
          url === '/api/auth/state' ||
          url === '/api/workspaces' ||
          url === '/api/connection' ||
          url === '/api/me/limits' ||
          url === '/api/me/allocations' ||
          url.startsWith(endpoint + '?'),
      ),
    ).toBe(true)
  },
)

it('shows personal usage beneath the administrator overview', async () => {
  const fetch = vi.fn().mockImplementation((url: string) => {
    if (url === '/api/auth/state')
      return Promise.resolve(new Response(JSON.stringify(authenticated)))
    if (url === '/api/workspaces')
      return Promise.resolve(new Response(JSON.stringify(workspaces)))
    if (url === '/api/system')
      return Promise.resolve(new Response(JSON.stringify(system)))
    if (url.startsWith('/api/me/usage?'))
      return Promise.resolve(new Response(JSON.stringify(summary)))
    if (url === '/api/me/allocations')
      return Promise.resolve(new Response(JSON.stringify({ schemes: [] })))
    if (url === '/api/me/limits')
      return Promise.resolve(
        new Response(
          JSON.stringify({
            user_id: 1,
            requests_per_minute: 0,
            max_concurrency: 0,
            in_flight: 0,
            requests_this_minute: 0,
            reset_at: 1900000060,
          }),
        ),
      )
    return Promise.reject(new Error(`Unexpected request: ${url}`))
  })
  vi.stubGlobal('fetch', fetch)
  render(
    <App
      router={createAppRouter(createMemoryHistory({ initialEntries: ['/'] }))}
    />,
  )
  expect(
    await screen.findByRole('heading', {
      name: 'Workspace overview',
      level: 1,
    }),
  ).toBeTruthy()
  expect(
    await screen.findByRole('heading', { name: 'Your usage', level: 2 }),
  ).toBeTruthy()
  expect(
    await screen.findByRole('img', { name: 'Usage trend · Tokens' }),
  ).toBeTruthy()
  expect(screen.queryByRole('heading', { name: 'Service' })).toBeNull()
  expect(screen.getByRole('heading', { name: 'Gateway setup' })).toBeTruthy()
  expect(screen.getByRole('link', { name: 'Instance status' })).toBeTruthy()
  expect(
    screen
      .getByRole('heading', { name: 'Gateway setup' })
      .compareDocumentPosition(
        screen.getByRole('heading', { name: 'Your usage' }),
      ) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy()
  expect(
    screen
      .getByText(
        'Some calls did not report token usage; totals include reported values only.',
      )
      .compareDocumentPosition(
        screen.getByRole('heading', { name: 'My resource allowances' }),
      ) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy()
  expect(screen.queryByRole('link', { name: 'Usage' })).toBeNull()
  expect(screen.getByRole('link', { name: 'Team usage' })).toBeTruthy()
  expect(fetch.mock.calls.some(([url]) => url === '/api/me/usage?days=7')).toBe(
    true,
  )
})

it('redirects the former personal usage URL to the combined home page', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation((url: string) => {
      if (url === '/api/auth/state')
        return Promise.resolve(
          new Response(JSON.stringify(memberAuthenticated)),
        )
      if (url === '/api/workspaces')
        return Promise.resolve(new Response(JSON.stringify(workspaces)))
      if (url.startsWith('/api/me/usage?'))
        return Promise.resolve(new Response(JSON.stringify(summary)))
      if (url === '/api/me/allocations')
        return Promise.resolve(new Response(JSON.stringify({ schemes: [] })))
      if (url === '/api/me/limits')
        return Promise.resolve(
          new Response(
            JSON.stringify({
              user_id: 2,
              requests_per_minute: 0,
              max_concurrency: 0,
              in_flight: 0,
              requests_this_minute: 0,
              reset_at: 1900000060,
            }),
          ),
        )
      return Promise.reject(new Error(`Unexpected request: ${url}`))
    }),
  )
  const router = createAppRouter(
    createMemoryHistory({ initialEntries: ['/usage'] }),
  )
  render(<App router={router} />)
  await screen.findByRole('heading', { name: 'Your workspace' })
  expect(router.state.location.pathname).toBe('/')
  expect(
    await screen.findByRole('heading', { name: 'Your usage' }),
  ).toBeTruthy()
})

it('summarizes cost, requests, tokens, and cache hits and toggles exact token values page-wide', async () => {
  const large = {
    ...metrics,
    input_tokens: 22732006,
    output_tokens: 97308,
    cached_tokens: 11366003,
    input_reported: 2,
    output_reported: 2,
    cached_reported: 2,
  }
  const fetch = vi.fn().mockImplementation((url: string) => {
    if (url === '/api/auth/state')
      return Promise.resolve(new Response(JSON.stringify(authenticated)))
    if (url === '/api/workspaces')
      return Promise.resolve(new Response(JSON.stringify(workspaces)))
    if (url === '/api/connection')
      return Promise.resolve(new Response(JSON.stringify({ status: 'ready' })))
    if (url.startsWith('/api/usage?'))
      return Promise.resolve(
        new Response(
          JSON.stringify({
            ...summary,
            totals: large,
            days: [{ id: '1900000000', name: '2030-03-17', ...large }],
            cost: {
              estimated_micro_usd: 9_370_000,
              unpriced_tokens: 120,
              days: [{ id: '1900000000', estimated_micro_usd: 9_370_000 }],
            },
          }),
        ),
      )
    return Promise.reject(new Error('Unexpected request ' + url))
  })
  vi.stubGlobal('fetch', fetch)
  const user = userEvent.setup()
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/usage'] }),
      )}
    />,
  )
  const total = await screen.findByRole('button', {
    name: '22.8M, show exact token counts',
  })
  const card = (label: string) =>
    screen.getByText(label, { selector: 'dt' }).parentElement!
  expect(card('Total cost').textContent).toContain('$9.37')
  expect(card('Total requests').textContent).toContain('2')
  expect(card('Tokens consumed').textContent).toContain('In 22.7M · Out 97.3K')
  expect(card('Cache hit rate').textContent).toContain('50%')
  expect(
    screen.getByText(/Some models have no price/, { selector: 'p' }),
  ).toBeTruthy()
  expect(total.getAttribute('aria-pressed')).toBe('false')
  expect(total.getAttribute('title')).toBe('22,829,314')
  await user.click(screen.getByRole('button', { name: 'View details' }))
  await user.click(
    screen.getByRole('button', {
      name: '22.7M / 97.3K, show exact token counts',
    }),
  )
  expect(
    screen
      .getByRole('button', {
        name: '22,829,314, show abbreviated token counts',
      })
      .getAttribute('aria-pressed'),
  ).toBe('true')
  await user.click(
    screen.getByRole('button', {
      name: '22,732,006 / 97,308, show abbreviated token counts',
    }),
  )
  expect(
    screen.getByRole('button', { name: '22.8M, show exact token counts' }),
  ).toBeTruthy()
  await user.click(screen.getByRole('button', { name: 'Cost', pressed: false }))
  expect(screen.getByRole('img', { name: 'Usage trend · Cost' })).toBeTruthy()
})

it('ranks models by reported tokens beside the usage trend and expands in place', async () => {
  const model = (name: string, input: number, output: number) => ({
    id: name,
    name,
    ...metrics,
    input_tokens: input,
    output_tokens: output,
  })
  const models = [
    model('codex/heavy', 900_000, 100_000),
    model('[other models]', 50_000, 0),
    model('codex/mid', 30_000, 0),
    model('codex/small-a', 10_000, 0),
    model('codex/small-b', 6_000, 0),
    model('codex/small-c', 4_000, 0),
    { ...model('codex/silent', 0, 0), input_reported: 0, output_reported: 0 },
  ]
  const fetch = vi.fn().mockImplementation((url: string) => {
    if (url === '/api/auth/state')
      return Promise.resolve(new Response(JSON.stringify(authenticated)))
    if (url === '/api/workspaces')
      return Promise.resolve(new Response(JSON.stringify(workspaces)))
    if (url === '/api/connection')
      return Promise.resolve(new Response(JSON.stringify({ status: 'ready' })))
    if (url.startsWith('/api/usage?'))
      return Promise.resolve(
        new Response(JSON.stringify({ ...summary, models })),
      )
    return Promise.reject(new Error('Unexpected request ' + url))
  })
  vi.stubGlobal('fetch', fetch)
  const user = userEvent.setup()
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/usage'] }),
      )}
    />,
  )
  const ranking = await screen.findByRole('region', { name: 'Usage ranking' })
  const rows = () => within(ranking).getAllByRole('listitem')
  expect(rows()).toHaveLength(5)
  expect(rows()[0].textContent).toContain('codex/heavy')
  expect(rows()[0].textContent).toContain('1M')
  expect(rows()[0].textContent).toContain('90.9%')
  expect(rows()[1].textContent).toContain('Other models')
  expect(rows()[1].textContent).toContain('4.5%')
  await user.click(within(ranking).getByRole('button', { name: 'Show all 6' }))
  expect(rows()).toHaveLength(6)
  expect(ranking.textContent).not.toContain('codex/silent')
  await user.click(within(ranking).getByRole('button', { name: 'Show less' }))
  expect(rows()).toHaveLength(5)
})

it('switches the ranking to API keys with owners, revoked labels, and the key tracking start', async () => {
  const key = (id: string, name: string, input: number, extra = {}) => ({
    id,
    name,
    revoked: false,
    requests: 1,
    input_tokens: input,
    output_tokens: 0,
    cached_tokens: 0,
    input_reported: 1,
    output_reported: 1,
    cached_reported: 0,
    ...extra,
  })
  const fetch = vi.fn().mockImplementation((url: string) => {
    if (url === '/api/auth/state')
      return Promise.resolve(new Response(JSON.stringify(authenticated)))
    if (url === '/api/workspaces')
      return Promise.resolve(new Response(JSON.stringify(workspaces)))
    if (url === '/api/connection')
      return Promise.resolve(new Response(JSON.stringify({ status: 'ready' })))
    if (url.startsWith('/api/usage?'))
      return Promise.resolve(
        new Response(
          JSON.stringify({
            ...summary,
            key_tracking_since: 1900003600,
            keys: [
              key('4', 'ci-runner', 750, { owner: 'synthetic-member' }),
              key('2', 'old-laptop', 250, {
                owner: 'synthetic-admin',
                revoked: true,
              }),
            ],
          }),
        ),
      )
    return Promise.reject(new Error('Unexpected request ' + url))
  })
  vi.stubGlobal('fetch', fetch)
  const user = userEvent.setup()
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/usage'] }),
      )}
    />,
  )
  const ranking = await screen.findByRole('region', { name: 'Usage ranking' })
  expect(
    within(ranking)
      .getByRole('button', { name: 'Models' })
      .getAttribute('aria-pressed'),
  ).toBe('true')
  await user.click(within(ranking).getByRole('button', { name: 'API keys' }))
  const rows = within(ranking).getAllByRole('listitem')
  expect(rows).toHaveLength(2)
  expect(rows[0].textContent).toContain('ci-runner')
  expect(rows[0].textContent).toContain('synthetic-member')
  expect(rows[0].textContent).toContain('75%')
  expect(rows[1].textContent).toContain('old-laptop')
  expect(rows[1].textContent).toContain('Revoked')
  expect(
    within(ranking).getByText(/API key usage is recorded from/).textContent,
  ).toContain('2030')
})
