import { withPage } from '@/test/fixtures'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { afterEach, expect, it, vi } from 'vitest'
import { App } from '@/App'
import { createAppRouter } from '@/router'
import {
  authenticated,
  memberAuthenticated,
  system,
  workspaces,
} from '@/test/fixtures'
import type { SetupProgress } from '@/lib/activation'
import { selectWorkspace } from '@/lib/workspace'

vi.mock('@/pages/Usage', () => ({ Usage: () => null, TeamUsage: () => null }))
afterEach(() => sessionStorage.clear())

const account = {
  id: 'synthetic-account',
  name: 'Synthetic account',
  provider: 'codex',
  email: 'synthetic@example.test',
  plan: 'synthetic',
  enabled: true,
  status: 'unverified',
  expires_at: 1900003600,
  created_at: 1900000000,
  updated_at: 1900000000,
}
const key = {
  id: 1,
  user_id: 1,
  name: 'Synthetic key',
  group_id: 1,
  group_name: 'Synthetic pool',
  group_access: 'allowed',
  prefix: 'sl_synthetic',
  copyable: true,
  enabled: true,
  created_at: 1900000000,
  last_used_at: null,
  revoked_at: null,
  expires_at: null,
}
function scenario(stage: SetupProgress['stage'], member = false) {
  const state = { stage, failed: false }
  const fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const response = (value: unknown, status = 200) =>
      new Response(JSON.stringify(withPage(value)), { status })
    if (url === '/api/auth/state')
      return response(member ? memberAuthenticated : authenticated)
    if (url === '/api/workspaces') return response(workspaces)
    if (url === '/api/connection')
      return state.failed
        ? response({ error: 'unavailable' }, 503)
        : response({
            status: 'ready',
            setup: {
              stage: state.stage,
              has_successful_request: state.stage === 'complete',
            },
          })
    if (url === '/api/system')
      return response({
        ...system,
        gateway: {
          ...system.gateway,
          status: state.stage === 'complete' ? 'ready' : system.gateway.status,
          has_usable_key: state.stage === 'complete',
          setup: { stage: state.stage, has_successful_request: false },
        },
      })
    if (url === '/api/accounts')
      return response({
        accounts:
          stage === 'account'
            ? []
            : [
                {
                  ...account,
                  status: state.stage === 'verify' ? 'unverified' : 'ready',
                },
              ],
      })
    if (url === '/api/accounts/runtime')
      return response({ accounts: [], server_time: 1900000000 })
    if (url === '/api/proxies') return response({ proxies: [] })
    if (url === '/api/accounts/synthetic-account/check') {
      state.stage = 'pool'
      return response({ account: { ...account, status: 'ready' }, models: [] })
    }
    if (url === '/api/groups') return response({ groups: [] })
    if (url === '/api/keys/groups')
      return response({
        groups: [{ id: 1, name: 'Synthetic pool', account_count: 1 }],
      })
    if (url === '/api/keys' && init?.method === 'POST') {
      state.stage = 'client'
      return response({ key, secret: 'sl_' + 'x'.repeat(43) }, 201)
    }
    if (url.startsWith('/api/keys?'))
      return response({
        keys: state.stage === 'key' ? [] : [key],
        server_time: 1900000000,
        next_cursor: 0,
      })
    return response({ error: 'unavailable' }, 503)
  })
  vi.stubGlobal('fetch', fetch)
  const router = createAppRouter(createMemoryHistory({ initialEntries: ['/'] }))
  const view = render(<App router={router} />)
  return { state, fetch, router, ...view }
}

it('guides real account verification, gates the next step, and resumes from saved server progress', async () => {
  const { router } = scenario('verify')
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Start guided setup' }),
  )
  const guide = await screen.findByRole('region', { name: 'Guided setup' })
  await waitFor(() =>
    expect(within(guide).getByRole('heading')).toBe(document.activeElement),
  )
  expect(router.state.location.pathname).toBe('/accounts')
  expect(
    within(guide).getByRole('button', { name: 'Next step' }),
  ).toHaveProperty('disabled', true)
  await user.click(
    await screen.findByRole('button', {
      name: 'Verify connection for Synthetic account',
    }),
  )
  await waitFor(() =>
    expect(
      within(guide).getByRole('button', { name: 'Next step' }),
    ).toHaveProperty('disabled', false),
  )
  await user.click(within(guide).getByRole('button', { name: 'Next step' }))
  await waitFor(() => expect(router.state.location.pathname).toBe('/groups'))
  expect(await screen.findByText('Step 3 of 5')).toBeTruthy()
  await user.click(screen.getByRole('button', { name: 'Previous step' }))
  await waitFor(() => expect(router.state.location.pathname).toBe('/accounts'))
  await user.click(screen.getByRole('button', { name: 'Skip guide' }))
  expect(screen.queryByRole('region', { name: 'Guided setup' })).toBeNull()
  await user.click(screen.getByRole('link', { name: 'Overview' }))
  await user.click(
    await screen.findByRole('button', { name: 'Start guided setup' }),
  )
  await waitFor(() => expect(router.state.location.pathname).toBe('/groups'))
})

it('lets the account dialog take focus without ending the guide', async () => {
  scenario('account')
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Start guided setup' }),
  )
  await screen.findByRole('region', { name: 'Guided setup' })
  await user.click(await screen.findByRole('button', { name: 'Add account' }))
  const dialog = await screen.findByRole('dialog')
  await waitFor(() =>
    expect(screen.queryByRole('region', { name: 'Guided setup' })).toBeNull(),
  )
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(
    await screen.findByRole('region', { name: 'Guided setup' }),
  ).toBeTruthy()
})

it('keeps the new key visible until the user closes its dialog', async () => {
  scenario('key')
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Start guided setup' }),
  )
  await user.click(await screen.findByRole('button', { name: 'Create key' }))
  const dialog = await screen.findByRole('dialog', { name: 'Create API key' })
  await user.type(within(dialog).getByLabelText('Name'), 'Synthetic key')
  await user.click(
    within(dialog).getByRole('button', { name: 'Resource group' }),
  )
  await user.click(
    await screen.findByRole('menuitemradio', { name: /Synthetic pool/ }),
  )
  await user.click(within(dialog).getByRole('button', { name: 'Create key' }))
  const created = await screen.findByRole('dialog', {
    name: 'Save your API key',
  })
  expect(within(created).getByLabelText('API key')).toHaveProperty(
    'value',
    'sl_' + 'x'.repeat(43),
  )
  expect(screen.queryByRole('region', { name: 'Guided setup' })).toBeNull()
  await user.click(within(created).getByRole('button', { name: 'Done' }))
  const guide = await screen.findByRole('region', { name: 'Guided setup' })
  expect(
    within(guide).getByRole('button', { name: 'Next step' }),
  ).toHaveProperty('disabled', false)
})

it('keeps member guidance out of administrator APIs and requires an observed successful call', async () => {
  const { state, fetch } = scenario('access', true)
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Start guided setup' }),
  )
  await screen.findByRole('region', { name: 'Guided setup' })
  expect(screen.getByText('Step 1 of 3')).toBeTruthy()
  expect(
    screen.getByText(
      'After your administrator grants access, check progress to continue.',
    ),
  ).toBeTruthy()
  expect(
    fetch.mock.calls.some(
      ([url]) =>
        url.startsWith('/api/accounts') ||
        url === '/api/groups' ||
        url === '/api/system',
    ),
  ).toBe(false)
  state.stage = 'client'
  await user.click(screen.getByRole('button', { name: 'Check progress' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Next step' })).toHaveProperty(
      'disabled',
      false,
    ),
  )
  await user.click(screen.getByRole('button', { name: 'Next step' }))
  await user.click(screen.getByRole('button', { name: 'Next step' }))
  expect(screen.getByText('Step 3 of 3')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Next step' })).toHaveProperty(
    'disabled',
    true,
  )
  state.stage = 'complete'
  await user.click(screen.getByRole('button', { name: 'Check progress' }))
  expect(
    await screen.findByRole('button', { name: 'Finish guide' }),
  ).toBeTruthy()
  await user.click(screen.getByRole('button', { name: 'Finish guide' }))
  expect(screen.queryByRole('region', { name: 'Guided setup' })).toBeNull()
})

it('returns to an unmet prerequisite and never advances from an unconfirmed refresh', async () => {
  const { state, router } = scenario('pool')
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Start guided setup' }),
  )
  await screen.findByRole('region', { name: 'Guided setup' })
  state.stage = 'account'
  await user.click(screen.getByRole('button', { name: 'Check progress' }))
  expect(await screen.findByText('Step 1 of 5')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Next step' })).toHaveProperty(
    'disabled',
    true,
  )
  await user.click(screen.getByRole('button', { name: 'Go to this step' }))
  await waitFor(() => expect(router.state.location.pathname).toBe('/accounts'))
  state.failed = true
  await user.click(screen.getByRole('button', { name: 'Check progress' }))
  expect(
    await screen.findByText(
      'Progress could not be confirmed. Check again before continuing.',
    ),
  ).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Next step' })).toHaveProperty(
    'disabled',
    true,
  )
})

it('resumes a guide after reload without carrying it into another workspace', async () => {
  const first = scenario('verify')
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Start guided setup' }),
  )
  await screen.findByRole('region', { name: 'Guided setup' })
  first.unmount()
  const resumed = render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/accounts'] }),
      )}
    />,
  )
  expect(
    await screen.findByRole('region', { name: 'Guided setup' }),
  ).toBeTruthy()
  expect(screen.getByText('Step 2 of 5')).toBeTruthy()
  resumed.unmount()
  selectWorkspace(2)
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/accounts'] }),
      )}
    />,
  )
  await screen.findByRole('heading', { name: 'Subscription accounts' })
  expect(screen.queryByRole('region', { name: 'Guided setup' })).toBeNull()
})

it.each([false, true])(
  'lets a returning user replay the guide from the toolbar (member: %s)',
  async (member) => {
    const { fetch, router } = scenario('complete', member)
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Guided setup' }),
    )
    const guide = await screen.findByRole('region', { name: 'Guided setup' })
    expect(router.state.location.pathname).toBe(member ? '/keys' : '/accounts')
    expect(within(guide).getByText(`Step 1 of ${member ? 3 : 5}`)).toBeTruthy()
    expect(
      within(guide).queryByRole('button', { name: 'Finish guide' }),
    ).toBeNull()
    expect(
      within(guide).getByRole('button', { name: 'Next step' }),
    ).toHaveProperty('disabled', false)
    for (let index = 1; index < (member ? 3 : 5); index++) {
      await user.click(screen.getByRole('button', { name: 'Next step' }))
    }
    expect(
      await screen.findByRole('button', { name: 'Finish guide' }),
    ).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Finish guide' }))
    expect(screen.queryByRole('region', { name: 'Guided setup' })).toBeNull()
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(
      false,
    )
  },
)
