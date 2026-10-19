import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { expect, it, vi } from 'vitest'
import { App } from '@/App'
import { createAppRouter } from '@/router'
import { i18n } from '@/lib/i18n'
import { authenticated, memberAuthenticated, workspaces } from '@/test/fixtures'

it.each(['/requests', '/admin/requests'])(
  'paginates %s by number and resets page size, filters and pruned pages safely',
  async (path) => {
    let available = 121
    const fetch = vi.fn((url: string) => {
      const query = new URL(url, 'http://example.test')
      let data: unknown = { accounts: [] }
      if (url === '/api/auth/state') data = authenticated
      if (url === '/api/workspaces') data = workspaces
      if (query.pathname.endsWith('/requests')) {
        const total =
          query.searchParams.get('outcome') === 'error' ? 13 : available
        const pageSize = Number(query.searchParams.get('limit') || 50)
        const page = Math.min(
          Number(query.searchParams.get('page') || 1),
          Math.max(1, Math.ceil(total / pageSize)),
        )
        const offset = (page - 1) * pageSize
        data = {
          total,
          page,
          page_size: pageSize,
          next_cursor: 0,
          requests: Array.from(
            { length: Math.min(pageSize, total - offset) },
            (_, index) => ({
              id: total - offset - index,
              user_id: 1,
              key_id: 1,
              group_id: 1,
              account_id: '',
              account_name: '',
              provider: 'codex',
              model: 'synthetic-model',
              transport: 'http',
              operation: 'responses',
              started_at: 1900000000,
              duration_ms: 125,
              request_id: `req_synthetic_${total - offset - index}`,
              first_token_ms: 42,
              outcome: 'success',
              error_code: '',
              upstream_status: 200,
              input_tokens: 1250,
              output_tokens: 50,
              cached_tokens: 1070,
              username: 'synthetic-member',
              key_name: 'Synthetic client',
              group_name: 'Synthetic group',
            }),
          ),
        }
      }
      return Promise.resolve(new Response(JSON.stringify(data)))
    })
    vi.stubGlobal('fetch', fetch)
    render(
      <App
        router={createAppRouter(
          createMemoryHistory({ initialEntries: [path] }),
        )}
      />,
    )
    const user = userEvent.setup()
    await screen.findByText('Showing 1–50 of 121')
    await user.click(screen.getByRole('button', { name: 'Page 3' }))
    await screen.findByText('Showing 101–121 of 121')
    expect(
      (screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement)
        .disabled,
    ).toBe(true)
    await user.click(screen.getByRole('button', { name: 'Rows per page' }))
    await user.click(screen.getByRole('menuitemradio', { name: '100' }))
    await screen.findByText('Showing 1–100 of 121')
    expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(
      101,
    )
    await user.click(screen.getByRole('button', { name: 'Rows per page' }))
    await user.click(screen.getByRole('menuitemradio', { name: '25' }))
    await screen.findByText('Showing 1–25 of 121')
    await user.click(screen.getByRole('button', { name: 'Page 4' }))
    await screen.findByText('Showing 76–100 of 121')
    available = 12
    await user.click(screen.getByRole('button', { name: 'Refresh' }))
    await screen.findByText('Showing 1–12 of 12')
    await user.click(screen.getByRole('button', { name: 'Result filter' }))
    await user.click(screen.getByRole('menuitemradio', { name: 'Failed' }))
    await screen.findByText('Showing 1–13 of 13')
    expect(
      fetch.mock.calls.some(([url]) => {
        const query = new URL(url, 'http://example.test').searchParams
        return (
          query.get('page') === '1' &&
          query.get('limit') === '25' &&
          query.get('outcome') === 'error'
        )
      }),
    ).toBe(true)
  },
)

it('shows content rule observations and blocked-request recovery without request bodies', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) =>
      Promise.resolve(
        new Response(
          JSON.stringify(
            url === '/api/auth/state'
              ? authenticated
              : url === '/api/workspaces'
                ? workspaces
                : url.startsWith('/api/requests?')
                  ? {
                      requests: [
                        {
                          id: 1,
                          user_id: 2,
                          key_id: 1,
                          group_id: 1,
                          account_id: '',
                          account_name: '',
                          provider: '',
                          model: 'synthetic-model',
                          transport: 'http',
                          operation: 'responses',
                          started_at: 1900000000,
                          duration_ms: 1,
                          request_id: 'req_synthetic_content',
                          first_token_ms: null,
                          outcome: 'rejected',
                          error_code: 'content_policy_blocked',
                          upstream_status: null,
                          input_tokens: null,
                          output_tokens: null,
                          cached_tokens: null,
                          username: 'synthetic-member',
                          key_name: 'Synthetic client',
                          group_name: 'Synthetic pool',
                          content: {
                            mode: 'block',
                            revision: 4,
                            rule_ids: ['rule_synthetic'],
                            check_failed: false,
                          },
                        },
                      ],
                      next_cursor: 0,
                      total: 1,
                      page: 1,
                      page_size: 50,
                    }
                  : { accounts: [] },
          ),
        ),
      ),
    ),
  )
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/requests'] }),
      )}
    />,
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'View request details' }),
  )
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByText('rule_synthetic')).toBeTruthy()
  expect(
    within(dialog).getByText('Policy version').nextElementSibling?.textContent,
  ).toBe('4')
  expect(
    within(dialog)
      .getByRole('link', { name: 'Content rules' })
      .getAttribute('href'),
  ).toBe('/admin/content')
})

it.each(
  (['en', 'zh'] as const).flatMap((language) =>
    [123456, 1, 0, null, undefined].flatMap((cost) =>
      [
        ['/requests', memberAuthenticated, '/api/me/requests?'],
        ['/admin/requests', authenticated, '/api/requests?'],
      ].map(([path, session, endpoint]) => ({
        language,
        cost,
        path,
        session,
        endpoint,
      })),
    ),
  ),
)(
  'shows estimated cost $cost in $path using $language',
  async ({ language, cost, path, session, endpoint }) => {
    await i18n.changeLanguage(language)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        let data: unknown = { accounts: [] }
        if (url === '/api/auth/state') data = session
        if (url === '/api/workspaces') data = workspaces
        if (url.startsWith(endpoint as string))
          data = {
            requests: [
              {
                id: 1,
                user_id: 2,
                key_id: 1,
                group_id: 1,
                account_id: '',
                account_name: '',
                provider: 'codex',
                model: 'synthetic-model',
                transport: 'http',
                operation: 'responses',
                started_at: 1900000000,
                duration_ms: 125,
                request_id: 'req_synthetic_cost',
                first_token_ms: 42,
                outcome: 'success',
                error_code: '',
                upstream_status: 200,
                input_tokens: 1250,
                output_tokens: 50,
                cached_tokens: 1070,
                estimated_cost_micro_usd: cost,
                username: 'synthetic-member',
                key_name: 'Synthetic cost client',
                group_name: 'Synthetic pool',
              },
            ],
            next_cursor: 0,
            total: 1,
            page: 1,
            page_size: 50,
          }
        return Promise.resolve(new Response(JSON.stringify(data)))
      }),
    )
    render(
      <App
        router={createAppRouter(
          createMemoryHistory({ initialEntries: [path as string] }),
        )}
      />,
    )
    const table = await screen.findByRole('table')
    const label = language === 'zh' ? '估算金额（USD）' : 'Estimated cost (USD)'
    expect(
      within(table).getByRole('columnheader', { name: label }),
    ).toBeTruthy()
    const expected =
      cost === null || cost === undefined
        ? '—'
        : new Intl.NumberFormat(language, {
            style: 'currency',
            currency: 'USD',
            minimumFractionDigits: 2,
            maximumFractionDigits: 6,
          }).format(cost / 1_000_000)
    const row = within(table).getAllByRole('row')[1]
    expect(within(row).getAllByRole('cell').at(-1)?.textContent).toBe(expected)
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', {
        name: language === 'zh' ? '查看请求详情' : 'View request details',
      }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(label).nextElementSibling?.textContent,
    ).toBe(expected)
  },
)

it.each(['responses', 'messages', 'gemini'])(
  'shows %s request metadata and filters failed calls',
  async (operation) => {
    const fetch = vi.fn().mockImplementation((url: string) => {
      let data: unknown = { accounts: [] }
      if (url === '/api/auth/state') data = authenticated
      if (url.startsWith('/api/requests?'))
        data = {
          requests: [
            {
              id: 1,
              user_id: 2,
              key_id: 1,
              group_id: 1,
              account_id: 'synthetic-account',
              provider: 'codex',
              model: 'codex/synthetic-model',
              transport: 'http',
              operation,
              started_at: 1900000000,
              duration_ms: 125,
              request_id: 'req_synthetic',
              first_token_ms: 42,
              outcome: 'error',
              error_code: 'rate_limited',
              upstream_status: 429,
              input_tokens: null,
              output_tokens: null,
              cached_tokens: null,
              username: 'member-test',
              key_name: 'Synthetic client',
              group_name: 'Default',
              account_name: 'Synthetic account',
            },
          ],
          next_cursor: 0,
          total: 1,
          page: 1,
          page_size: 50,
        }
      return Promise.resolve(new Response(JSON.stringify(data)))
    })
    vi.stubGlobal('fetch', fetch)
    render(
      <App
        router={createAppRouter(
          createMemoryHistory({ initialEntries: ['/admin/requests'] }),
        )}
      />,
    )
    await screen.findByRole('heading', { name: 'All requests' })
    await screen.findByText('Synthetic account')
    expect(screen.getByText(/^Rate limited/)).toBeTruthy()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Result filter' }))
    await user.click(screen.getByRole('menuitemradio', { name: 'Failed' }))
    await waitFor(() =>
      expect(
        fetch.mock.calls.some(([url]) => url.includes('outcome=error')),
      ).toBe(true),
    )
  },
)

it.each([memberAuthenticated, authenticated])(
  'shows only the personal history endpoint for $user.role',
  async (session) => {
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url === '/api/auth/state')
        return Promise.resolve(new Response(JSON.stringify(session)))
      if (url === '/api/workspaces')
        return Promise.resolve(new Response(JSON.stringify(workspaces)))
      if (url.startsWith('/api/me/requests?'))
        return Promise.resolve(
          new Response(
            JSON.stringify({
              requests: [
                {
                  id: 1,
                  user_id: session.user.id,
                  key_id: 1,
                  group_id: 1,
                  account_id: '',
                  account_name: '',
                  provider: 'codex',
                  model: 'codex/synthetic-model',
                  transport: 'http',
                  operation: 'responses',
                  started_at: 1900000000,
                  duration_ms: 125,
                  request_id: 'req_synthetic',
                  first_token_ms: 42,
                  outcome: 'success',
                  error_code: '',
                  upstream_status: 200,
                  input_tokens: 12,
                  output_tokens: 4,
                  cached_tokens: null,
                  username: session.user.username,
                  key_name: 'My synthetic client',
                  group_name: 'Default',
                },
              ],
              next_cursor: 0,
              total: 1,
              page: 1,
              page_size: 50,
            }),
          ),
        )
      return Promise.reject(new Error('Unexpected management request'))
    })
    vi.stubGlobal('fetch', fetch)
    render(
      <App
        router={createAppRouter(
          createMemoryHistory({ initialEntries: ['/requests'] }),
        )}
      />,
    )
    await screen.findByRole('heading', { name: 'Your requests' })
    await screen.findByText('My synthetic client')
    expect(screen.getByText('Hit rate: —')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Account filter' })).toBeNull()
    expect(screen.getByRole('columnheader', { name: 'API key' })).toBeTruthy()
    expect(screen.getByRole('columnheader', { name: 'Group' })).toBeTruthy()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Result filter' }))
    await user.click(screen.getByRole('menuitemradio', { name: 'Failed' }))
    await waitFor(() =>
      expect(
        fetch.mock.calls.some(
          ([url]) =>
            url.startsWith('/api/me/requests?') &&
            url.includes('outcome=error'),
        ),
      ).toBe(true),
    )
    expect(
      fetch.mock.calls.every(
        ([url]) =>
          url === '/api/auth/state' ||
          url === '/api/workspaces' ||
          url.startsWith('/api/me/requests?'),
      ),
    ).toBe(true)
  },
)

it.each([
  ['/requests', memberAuthenticated, '/api/me/requests?'],
  ['/admin/requests', authenticated, '/api/requests?'],
] as const)(
  'shows input-token cache hit rates in %s',
  async (path, session, endpoint) => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        if (url === '/api/auth/state')
          return Promise.resolve(new Response(JSON.stringify(session)))
        if (url.startsWith(endpoint))
          return Promise.resolve(
            new Response(
              JSON.stringify({
                requests: [
                  {
                    id: 1,
                    user_id: session.user.id,
                    key_id: 1,
                    group_id: 1,
                    account_id: '',
                    account_name: '',
                    provider: 'codex',
                    model: 'synthetic-model',
                    transport: 'http',
                    operation: 'responses',
                    started_at: 1900000000,
                    duration_ms: 125,
                    request_id: 'req_synthetic',
                    first_token_ms: 42,
                    outcome: 'success',
                    error_code: '',
                    upstream_status: 200,
                    input_tokens: 1250,
                    output_tokens: 50,
                    cached_tokens: 1070,
                    username: session.user.username,
                    key_name: 'Synthetic cache client',
                    group_name: 'Default',
                  },
                ],
                next_cursor: 0,
                total: 1,
                page: 1,
                page_size: 50,
              }),
            ),
          )
        return Promise.resolve(new Response(JSON.stringify({ accounts: [] })))
      }),
    )
    render(
      <App
        router={createAppRouter(
          createMemoryHistory({ initialEntries: [path] }),
        )}
      />,
    )
    expect(await screen.findByText('Cached: 1,070')).toBeTruthy()
    expect(await screen.findByText('Hit rate: 85.6%')).toBeTruthy()
    expect(screen.getByText('1,250 / 50')).toBeTruthy()
    const table = screen.getByRole('table')
    expect(within(table).getByText('req_synthetic')).toBeTruthy()
    const user = userEvent.setup()
    const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    await user.click(
      within(table).getByRole('button', { name: 'Copy request ID' }),
    )
    expect(copy).toHaveBeenCalledWith('req_synthetic')
    expect(within(table).getByRole('status').textContent).toContain('Copied')
    expect(screen.queryByRole('dialog')).toBeNull()
  },
)

it('filters diagnostics by model and caller, then copies the request ID', async () => {
  const user = userEvent.setup()
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  const fetch = vi.fn().mockImplementation((url: string) => {
    if (url === '/api/auth/state')
      return Promise.resolve(new Response(JSON.stringify(authenticated)))
    if (url === '/api/requests/filters')
      return Promise.resolve(
        new Response(
          JSON.stringify({
            callers: [
              {
                user_id: 2,
                key_id: 7,
                username: 'synthetic-member',
                key_name: 'Synthetic diagnostic key',
              },
            ],
          }),
        ),
      )
    if (url.startsWith('/api/requests?'))
      return Promise.resolve(
        new Response(
          JSON.stringify({
            requests: [
              {
                id: 1,
                user_id: 2,
                key_id: 7,
                group_id: 1,
                account_id: 'synthetic-account',
                account_name: 'Synthetic account',
                provider: 'codex',
                model: 'synthetic-model',
                transport: 'http',
                operation: 'responses',
                started_at: 1900000000,
                duration_ms: 120,
                first_token_ms: 42,
                request_id: 'req_synthetic',
                outcome: 'success',
                error_code: '',
                upstream_status: 200,
                input_tokens: 10,
                output_tokens: 5,
                cached_tokens: 5,
                username: 'synthetic-member',
                key_name: 'Synthetic diagnostic key',
                group_name: 'Default',
              },
            ],
            next_cursor: 0,
            total: 1,
            page: 1,
            page_size: 50,
          }),
        ),
      )
    return Promise.resolve(new Response(JSON.stringify({ accounts: [] })))
  })
  vi.stubGlobal('fetch', fetch)
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/requests'] }),
      )}
    />,
  )
  await screen.findByText('First output: 42 ms')
  await user.click(screen.getByRole('button', { name: 'Filters' }))
  await user.type(screen.getByLabelText('Model name'), 'synthetic-model')
  await user.click(screen.getByRole('button', { name: 'Member filter' }))
  await user.click(
    await screen.findByRole('menuitemradio', { name: 'synthetic-member' }),
  )
  await user.click(screen.getByRole('button', { name: 'API key filter' }))
  await user.click(
    screen.getByRole('menuitemradio', {
      name: 'Synthetic diagnostic key · synthetic-member',
    }),
  )
  await user.click(screen.getByRole('button', { name: 'Apply filters' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(
        ([url]) =>
          url.includes('model=synthetic-model') &&
          url.includes('user_id=2') &&
          url.includes('key_id=7'),
      ),
    ).toBe(true),
  )
  await user.click(screen.getByRole('button', { name: 'View request details' }))
  expect(
    await screen.findByRole('dialog', { name: 'Request details' }),
  ).toBeTruthy()
  await user.click(screen.getByRole('button', { name: 'Copy request ID' }))
  expect(copy).toHaveBeenCalledWith('req_synthetic')
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'View request details' }),
    ),
  )
})

it.each(
  (['en', 'zh'] as const).flatMap((language) =>
    (['high', '', undefined] as const).flatMap((effort) =>
      [
        ['/requests', memberAuthenticated, '/api/me/requests?'],
        ['/admin/requests', authenticated, '/api/requests?'],
      ].map(([path, session, endpoint]) => ({
        language,
        effort,
        path: path as string,
        session: session as typeof authenticated | typeof memberAuthenticated,
        endpoint: endpoint as string,
      })),
    ),
  ),
)(
  'shows reasoning effort $effort in $path using $language',
  async ({ language, effort, path, session, endpoint }) => {
    await i18n.changeLanguage(language)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((url: string) => {
        let data: unknown = { accounts: [] }
        if (url === '/api/auth/state') data = session
        if (url === '/api/workspaces') data = workspaces
        if (url.startsWith(endpoint))
          data = {
            requests: [
              {
                id: 1,
                user_id: session.user.id,
                key_id: 1,
                group_id: 1,
                account_id: '',
                account_name: '',
                provider: 'codex',
                model: 'synthetic-model',
                reasoning_effort: effort,
                transport: 'http',
                operation: 'responses',
                started_at: 1900000000,
                duration_ms: 125,
                request_id: 'req_synthetic_effort',
                first_token_ms: 42,
                outcome: 'success',
                error_code: '',
                upstream_status: 200,
                input_tokens: 12,
                output_tokens: 4,
                cached_tokens: null,
                username: session.user.username,
                key_name: 'Synthetic effort client',
                group_name: 'Synthetic pool',
              },
            ],
            next_cursor: 0,
            total: 1,
            page: 1,
            page_size: 50,
          }
        return Promise.resolve(new Response(JSON.stringify(data)))
      }),
    )
    render(
      <App
        router={createAppRouter(
          createMemoryHistory({ initialEntries: [path] }),
        )}
      />,
    )
    const value = effort || '—'
    const label = language === 'zh' ? '推理强度' : 'Reasoning effort'
    expect(
      await screen.findByText(
        `${label}${language === 'zh' ? '：' : ': '}${value}`,
      ),
    ).toBeTruthy()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', {
        name: language === 'zh' ? '查看请求详情' : 'View request details',
      }),
    )
    const dialog = await screen.findByRole('dialog')
    const term = within(dialog).getByText(label)
    expect(term.nextElementSibling?.textContent).toBe(value)
  },
)
