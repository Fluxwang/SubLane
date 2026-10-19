import { withPage } from '@/test/fixtures'
import { act, render, screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { expect, it, vi } from 'vitest'
import { App } from '@/App'
import { createAppRouter } from '@/router'
import { authenticated } from '@/test/fixtures'
import * as queries from '@/lib/query'
import { authKey } from '@/lib/auth'

const syntheticMember = {
  id: 2,
  username: 'member-test',
  role: 'member',
  enabled: true,
  created_at: 1900000000,
}
const response = (data: unknown, status = 200) =>
  new Response(JSON.stringify(withPage(data)), { status })

it('clears the previous administrator roster while the next identity is loading', async () => {
  const client = queries.createQueryClient()
  client.setQueryData(authKey, authenticated)
  vi.spyOn(queries, 'createQueryClient').mockReturnValue(client)
  let complete: ((value: Response) => void) | undefined
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url === '/api/workspaces')
        return Promise.resolve(response({ tenants: [] }))
      if (url.startsWith('/api/members?')) {
        if (client.getQueryData<typeof authenticated>(authKey)?.user.id === 1) {
          return Promise.resolve(
            response({
              members: [
                { ...syntheticMember, username: 'synthetic-private-first' },
              ],
              next_cursor: 0,
            }),
          )
        }
        return new Promise<Response>((resolve) => {
          complete = resolve
        })
      }
      return Promise.resolve(response(authenticated))
    }),
  )
  open()
  await screen.findByText('synthetic-private-first')
  await act(async () => {
    client.setQueryData(authKey, {
      ...authenticated,
      user: { ...authenticated.user, id: 99 },
    })
  })
  await waitFor(() => expect(complete).toBeTypeOf('function'))
  const leaked = screen.queryByText('synthetic-private-first') !== null
  await act(async () => {
    complete?.(response({ members: [], next_cursor: 0 }))
  })
  expect(leaked).toBe(false)
  client.clear()
})

it('lets the workspace owner edit their own request limits', async () => {
  const policy = {
    user_id: 1,
    requests_per_minute: 0,
    max_concurrency: 10,
    in_flight: 0,
    requests_this_minute: 0,
    reset_at: 1900000000,
  }
  const fetchMock = vi
    .fn()
    .mockImplementation((url: string, init?: RequestInit) => {
      if (url === '/api/auth/state')
        return Promise.resolve(response(authenticated))
      if (url === '/api/members/1/limits') {
        if (init?.method === 'PATCH')
          Object.assign(policy, JSON.parse(String(init.body)))
        return Promise.resolve(response(policy))
      }
      return Promise.resolve(
        response({
          members: [
            {
              id: 1,
              username: 'synthetic-owner',
              role: 'owner',
              enabled: true,
              created_at: 1900000000,
            },
          ],
          next_cursor: 0,
        }),
      )
    })
  vi.stubGlobal('fetch', fetchMock)
  open()
  const row = await screen.findByRole('row', { name: /synthetic-owner/ })
  expect(within(row).getByText('Owner')).toBeTruthy()
  expect(within(row).queryByRole('button', { name: /disable/i })).toBeNull()
  expect(within(row).queryByRole('button', { name: /change role/i })).toBeNull()
  const user = userEvent.setup()
  await user.click(within(row).getByRole('button', { name: /more actions/i }))
  expect(screen.queryByRole('menuitem', { name: 'Reset password' })).toBeNull()
  await user.click(screen.getByRole('menuitem', { name: 'Request limits' }))
  const limits = await screen.findByRole('dialog', {
    name: 'Request limits for synthetic-owner',
  })
  const concurrency = await within(limits).findByLabelText(
    'Concurrent requests',
  )
  await user.clear(concurrency)
  await user.type(concurrency, '5')
  await user.click(within(limits).getByRole('button', { name: 'Save changes' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(
    fetchMock.mock.calls.some(
      ([url, init]) =>
        url === '/api/members/1/limits' &&
        init?.method === 'PATCH' &&
        JSON.parse(String(init.body)).max_concurrency === 5,
    ),
  ).toBe(true)
})

it('offers request limits for workspace administrators', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation((url: string) =>
      Promise.resolve(
        response(
          url === '/api/auth/state'
            ? authenticated
            : {
                members: [{ ...syntheticMember, role: 'admin' }],
                next_cursor: 0,
              },
        ),
      ),
    ),
  )
  open()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'More actions for member-test' }),
  )
  expect(screen.getByRole('menuitem', { name: 'Request limits' })).toBeTruthy()
})

it.each([0, 10, 64, 1000])(
  'updates shared member concurrency to %i and resets a member password',
  async (maxConcurrency) => {
    const policy = {
      user_id: 2,
      requests_per_minute: 0,
      max_concurrency: 10,
      in_flight: 0,
      requests_this_minute: 0,
      reset_at: 1900000000,
    }
    const fetch = vi
      .fn()
      .mockImplementation((url: string, init?: RequestInit) => {
        if (url === '/api/auth/state')
          return Promise.resolve(response(authenticated))
        if (url.endsWith('/limits')) return Promise.resolve(response(policy))
        if (url.endsWith('/password') && init?.method === 'POST')
          return Promise.resolve(new Response(null, { status: 204 }))
        return Promise.resolve(
          response({ members: [syntheticMember], next_cursor: 0 }),
        )
      })
    vi.stubGlobal('fetch', fetch)
    const user = userEvent.setup()
    open()
    await user.click(
      await screen.findByRole('button', {
        name: 'More actions for member-test',
      }),
    )
    await user.click(screen.getByRole('menuitem', { name: 'Request limits' }))
    const limits = await screen.findByRole('dialog', {
      name: 'Request limits for member-test',
    })
    const rate = await within(limits).findByLabelText('Requests per minute')
    await user.clear(rate)
    await user.type(rate, '60')
    const concurrency = within(limits).getByLabelText('Concurrent requests')
    expect((concurrency as HTMLInputElement).value).toBe('10')
    expect(concurrency.getAttribute('max')).toBe('1000')
    await user.clear(concurrency)
    await user.type(concurrency, '1001')
    await user.click(
      within(limits).getByRole('button', { name: 'Save changes' }),
    )
    expect(within(limits).getByRole('alert').textContent).toContain('0–1,000')
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'PATCH')).toBe(
      false,
    )
    await user.clear(concurrency)
    await user.type(concurrency, String(maxConcurrency))
    await user.click(
      within(limits).getByRole('button', { name: 'Save changes' }),
    )
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const call = fetch.mock.calls.find(
      ([url, init]) => url.endsWith('/limits') && init?.method === 'PATCH',
    )
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      requests_per_minute: 60,
      max_concurrency: maxConcurrency,
    })
    await user.click(
      screen.getByRole('button', { name: 'More actions for member-test' }),
    )
    await user.click(screen.getByRole('menuitem', { name: 'Reset password' }))
    const reset = await screen.findByRole('dialog', {
      name: 'Reset password for member-test',
    })
    expect(within(reset).queryByLabelText('Current password')).toBeNull()
    await user.type(
      within(reset).getByLabelText('New password'),
      'synthetic-reset',
    )
    await user.type(
      within(reset).getByLabelText('Confirm password'),
      'synthetic-reset',
    )
    await user.click(
      within(reset).getByRole('button', { name: 'Save password' }),
    )
    await screen.findByText('Password reset for member-test.')
    expect(screen.queryByDisplayValue('synthetic-reset')).toBeNull()
  },
)

function open() {
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/members'] }),
      )}
    />,
  )
}

it('creates a one-time invitation link for the selected workspace', async () => {
  const fetchMock = vi
    .fn()
    .mockImplementation((url: string, init?: RequestInit) => {
      if (url === '/api/auth/state')
        return Promise.resolve(response(authenticated))
      if (url === '/api/members/invitations' && init?.method === 'POST')
        return Promise.resolve(
          response(
            {
              token: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
              expires_at: '2030-01-01T08:00:00+08:00',
            },
            201,
          ),
        )
      return Promise.resolve(response({ members: [], next_cursor: 0 }))
    })
  vi.stubGlobal('fetch', fetchMock)
  open()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Create invite link' }),
  )
  const link = await screen.findByRole<HTMLInputElement>('textbox', {
    name: 'Invitation link',
  })
  expect(link.value).toContain(
    '/invite?workspace=1#aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  )
  expect(screen.getByText(/valid for 7 days/i)).toBeTruthy()
})

it('creates a member and toggles their access using the management API', async () => {
  let members: (typeof syntheticMember)[] = []
  const fetchMock = vi
    .fn()
    .mockImplementation((url: string, init?: RequestInit) => {
      if (url === '/api/auth/state')
        return Promise.resolve(response(authenticated))
      if (init?.method === 'POST') {
        members = [syntheticMember]
        return Promise.resolve(response(syntheticMember, 201))
      }
      if (init?.method === 'PATCH') {
        members = [{ ...syntheticMember, ...JSON.parse(String(init.body)) }]
        return Promise.resolve(response(members[0]))
      }
      return Promise.resolve(response({ members, next_cursor: 0 }))
    })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  open()
  await screen.findByText('No members yet')
  await user.click(screen.getByRole('button', { name: 'Add member' }))
  const drawer = await screen.findByRole('dialog', { name: 'Add member' })
  await user.type(within(drawer).getByLabelText('Username'), 'member-test')
  await user.type(
    within(drawer).getByLabelText('Password', { exact: true }),
    'member pass 42',
  )
  await user.type(
    within(drawer).getByLabelText('Confirm password'),
    'member pass 42',
  )
  await user.click(
    within(drawer).getByRole('button', { name: 'Create member' }),
  )
  await screen.findByRole('button', { name: 'Disable member-test' })
  expect(
    screen.getAllByRole('button', {
      name: 'Manage groups for member-test',
    }),
  ).toHaveLength(2)
  const created = fetchMock.mock.calls.find(
    ([, init]) => init?.method === 'POST',
  )
  expect(JSON.parse(String(created?.[1]?.body))).toEqual({
    username: 'member-test',
    password: 'member pass 42',
  })
  await user.click(screen.getByRole('button', { name: 'Disable member-test' }))
  await screen.findByRole('button', { name: 'Enable member-test' })
  await user.click(screen.getByRole('button', { name: 'Enable member-test' }))
  await screen.findByRole('button', { name: 'Disable member-test' })
})

it('grants resource pool access directly to a member', async () => {
  const fetchMock = vi
    .fn()
    .mockImplementation((url: string, init?: RequestInit) => {
      if (url === '/api/auth/state')
        return Promise.resolve(response(authenticated))
      if (url === '/api/members?page=1&limit=50')
        return Promise.resolve(
          response({ members: [syntheticMember], next_cursor: 0 }),
        )
      if (url === '/api/groups')
        return Promise.resolve(
          response({
            groups: [
              {
                id: 3,
                name: 'Synthetic pool',
                enabled: true,
                is_default: false,
                restricted_models: false,
                created_at: 1,
                updated_at: 1,
                account_count: 0,
                member_count: 0,
              },
            ],
          }),
        )
      if (url === '/api/groups/members/2')
        return Promise.resolve(
          response({ group_ids: init?.method === 'PUT' ? [3] : [] }),
        )
      return Promise.resolve(response({}))
    })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  open()
  await user.click(
    await screen.findByRole('button', {
      name: 'Manage groups for member-test',
    }),
  )
  const dialog = await screen.findByRole('dialog', { name: 'Group access' })
  await user.click(
    within(dialog).getByRole('checkbox', { name: 'Synthetic pool' }),
  )
  await user.click(within(dialog).getByRole('button', { name: 'Save access' }))
  await waitFor(() =>
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/groups/members/2',
      expect.objectContaining({ method: 'PUT', body: '{"group_ids":[3]}' }),
    ),
  )
})

it('keeps the create form usable after a duplicate username and validates confirmation', async () => {
  const create = vi
    .fn()
    .mockResolvedValue(response({ error: 'username_taken' }, 409))
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (url === '/api/auth/state')
        return Promise.resolve(response(authenticated))
      if (init?.method === 'POST') return create()
      return Promise.resolve(response({ members: [], next_cursor: 0 }))
    }),
  )
  const user = userEvent.setup()
  open()
  await screen.findByText('No members yet')
  await user.click(screen.getByRole('button', { name: 'Add member' }))
  const drawer = await screen.findByRole('dialog', { name: 'Add member' })
  await user.type(within(drawer).getByLabelText('Username'), 'member-test')
  await user.type(
    within(drawer).getByLabelText('Password', { exact: true }),
    'member pass 42',
  )
  await user.type(
    within(drawer).getByLabelText('Confirm password'),
    'different pass 42',
  )
  await user.click(
    within(drawer).getByRole('button', { name: 'Create member' }),
  )
  expect((await within(drawer).findByRole('alert')).textContent).toBe(
    'Passwords do not match.',
  )
  expect(create).not.toHaveBeenCalled()
  await user.clear(within(drawer).getByLabelText('Confirm password'))
  await user.type(
    within(drawer).getByLabelText('Confirm password'),
    'member pass 42',
  )
  await user.click(
    within(drawer).getByRole('button', { name: 'Create member' }),
  )
  expect((await within(drawer).findByRole('alert')).textContent).toBe(
    'This username is already in use.',
  )
  await waitFor(() =>
    expect(
      within(drawer)
        .getByRole('button', { name: 'Create member' })
        .hasAttribute('disabled'),
    ).toBe(false),
  )
})

it('adds an existing login as an administrator and changes its workspace role', async () => {
  let members: (typeof syntheticMember)[] = []
  const calls: Array<{ username: string; role: string }> = []
  const fetchMock = vi
    .fn()
    .mockImplementation((url: string, init?: RequestInit) => {
      if (url === '/api/auth/state')
        return Promise.resolve(response(authenticated))
      if (url === '/api/tenants/1/members' && init?.method === 'POST') {
        const input = JSON.parse(String(init.body))
        calls.push(input)
        members = [{ ...syntheticMember, role: input.role }]
        return Promise.resolve(response(members[0], 201))
      }
      if (url === '/api/members/2/role' && init?.method === 'PATCH') {
        const input = JSON.parse(String(init.body))
        members = [{ ...syntheticMember, role: input.role }]
        return Promise.resolve(response(members[0]))
      }
      return Promise.resolve(response({ members, next_cursor: 0 }))
    })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  open()
  await screen.findByText('No members yet')
  await user.click(screen.getByRole('button', { name: 'Add existing account' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Add existing account',
  })
  await user.type(within(dialog).getByLabelText('Username'), 'member-test')
  await user.click(within(dialog).getByRole('radio', { name: 'Administrator' }))
  await user.click(within(dialog).getByRole('button', { name: 'Save access' }))
  const row = await screen.findByRole('row', { name: /member-test/ })
  expect(within(row).getByText('Administrator')).toBeTruthy()
  expect(calls[0]).toEqual({ username: 'member-test', role: 'admin' })
  await user.click(
    screen.getByRole('button', { name: 'Change role for member-test' }),
  )
  const change = await screen.findByRole('dialog', {
    name: 'Change workspace role',
  })
  await user.click(within(change).getByRole('radio', { name: 'Member' }))
  await user.click(within(change).getByRole('button', { name: 'Save access' }))
  await waitFor(() =>
    expect(
      within(screen.getByRole('row', { name: /member-test/ })).getByText(
        'Member',
      ),
    ).toBeTruthy(),
  )
  expect(
    screen.getAllByRole('button', {
      name: 'Manage groups for member-test',
    }),
  ).toHaveLength(2)
  expect(
    fetchMock.mock.calls.some(
      ([url, init]) =>
        url === '/api/members/2/role' &&
        init?.method === 'PATCH' &&
        init.body === '{"role":"member"}',
    ),
  ).toBe(true)
})

it('offers a pool grant immediately when an older linked login is outside the current page', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (url === '/api/auth/state')
        return Promise.resolve(response(authenticated))
      if (url === '/api/tenants/1/members' && init?.method === 'POST')
        return Promise.resolve(response(syntheticMember, 201))
      return Promise.resolve(response({ members: [], next_cursor: 0 }))
    }),
  )
  const user = userEvent.setup()
  open()
  await screen.findByText('No members yet')
  await user.click(screen.getByRole('button', { name: 'Add existing account' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Add existing account',
  })
  await user.type(within(dialog).getByLabelText('Username'), 'member-test')
  await user.click(within(dialog).getByRole('button', { name: 'Save access' }))
  expect(
    await screen.findByRole('button', {
      name: 'Manage groups for member-test',
    }),
  ).toBeTruthy()
})

it('explains how to change a role when an existing login already belongs here', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (url === '/api/auth/state')
        return Promise.resolve(response(authenticated))
      if (url === '/api/tenants/1/members' && init?.method === 'POST')
        return Promise.resolve(
          response({ error: 'member_already_exists' }, 409),
        )
      return Promise.resolve(response({ members: [], next_cursor: 0 }))
    }),
  )
  const user = userEvent.setup()
  open()
  await screen.findByText('No members yet')
  await user.click(screen.getByRole('button', { name: 'Add existing account' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Add existing account',
  })
  await user.type(within(dialog).getByLabelText('Username'), 'member-test')
  await user.click(within(dialog).getByRole('button', { name: 'Save access' }))
  expect((await within(dialog).findByRole('alert')).textContent).toContain(
    'Change workspace role',
  )
})
