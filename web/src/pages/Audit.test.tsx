import { withPage } from '@/test/fixtures'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { expect, it, vi } from 'vitest'
import { App } from '@/App'
import { createAppRouter } from '@/router'
import { authenticated, memberAuthenticated } from '@/test/fixtures'
const response = (value: unknown) =>
  new Response(JSON.stringify(withPage(value)))
it('filters administrator audit records and follows bounded pagination', async () => {
  const fetch = vi.fn().mockImplementation((url: string) => {
    if (url === '/api/auth/state')
      return Promise.resolve(response(authenticated))
    if (url.startsWith('/api/audit'))
      return Promise.resolve(
        response({
          events: [
            {
              id: 52,
              actor_id: 1,
              actor_name: 'synthetic-admin',
              actor_role: 'admin',
              source: 'user',
              action: 'key.update',
              resource: 'key',
              resource_id: '9',
              outcome: 'success',
              http_status: null,
              created_at: 1900000000,
            },
          ],
          next_cursor: url.includes('page=2') ? 0 : 52,
          total: 51,
          page: url.includes('page=2') ? 2 : 1,
          page_size: 50,
        }),
      )
    return Promise.reject(new Error('Unexpected request'))
  })
  vi.stubGlobal('fetch', fetch)
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/audit'] }),
      )}
    />,
  )
  const user = userEvent.setup()
  await screen.findByText('Updated API key')
  await user.click(screen.getByRole('button', { name: 'Next' }))
  await waitFor(() =>
    expect(fetch.mock.calls.some(([url]) => url.includes('page=2'))).toBe(true),
  )
  await user.click(screen.getByRole('button', { name: 'Resource type' }))
  await user.click(
    screen.getByRole('menuitemradio', { name: 'Resource group' }),
  )
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(
        ([url]) => url.includes('page=1') && url.includes('resource=group'),
      ),
    ).toBe(true),
  )
})
it('never requests audit records for members, including direct navigation', async () => {
  const fetch = vi
    .fn()
    .mockImplementation((url: string) =>
      Promise.resolve(
        response(
          url === '/api/auth/state' ? memberAuthenticated : { status: 'ready' },
        ),
      ),
    )
  vi.stubGlobal('fetch', fetch)
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/audit'] }),
      )}
    />,
  )
  await screen.findByRole('heading', { name: 'Access denied' })
  expect(fetch.mock.calls.some(([url]) => url.startsWith('/api/audit'))).toBe(
    false,
  )
})

it('renders and filters network proxy audit records', async () => {
  const fetch = vi.fn().mockImplementation((url: string) => {
    if (url === '/api/auth/state')
      return Promise.resolve(response(authenticated))
    if (url.startsWith('/api/audit'))
      return Promise.resolve(
        response({
          events: [
            {
              id: 1,
              actor_id: 1,
              actor_name: 'synthetic-admin',
              actor_role: 'admin',
              source: 'user',
              action: 'proxy.create',
              resource: 'proxy',
              resource_id: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
              outcome: 'success',
              http_status: null,
              created_at: 1900000000,
            },
          ],
          next_cursor: 0,
        }),
      )
    return Promise.reject(new Error('Unexpected request'))
  })
  vi.stubGlobal('fetch', fetch)
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/audit'] }),
      )}
    />,
  )
  await screen.findByText('Created network proxy')
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Resource type' }))
  await user.click(screen.getByRole('menuitemradio', { name: 'Network proxy' }))
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(([url]) => url.includes('resource=proxy')),
    ).toBe(true),
  )
})

it('shows invitation audit records and filters them by resource', async () => {
  const fetch = vi.fn().mockImplementation((url: string) => {
    if (url === '/api/auth/state')
      return Promise.resolve(response(authenticated))
    if (url.startsWith('/api/audit'))
      return Promise.resolve(
        response({
          events: [
            {
              id: 3,
              actor_id: 1,
              actor_name: 'synthetic-admin',
              actor_role: 'admin',
              source: 'user',
              action: 'invitation.create',
              resource: 'invitation',
              resource_id: '3',
              outcome: 'success',
              http_status: null,
              created_at: 1900000000,
            },
          ],
          next_cursor: 0,
        }),
      )
    return Promise.reject(new Error('Unexpected request'))
  })
  vi.stubGlobal('fetch', fetch)
  render(
    <App
      router={createAppRouter(
        createMemoryHistory({ initialEntries: ['/admin/audit'] }),
      )}
    />,
  )

  await screen.findByText('Created invitation link')
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Resource type' }))
  await user.click(
    screen.getByRole('menuitemradio', { name: 'Invitation link' }),
  )
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(([url]) => url.includes('resource=invitation')),
    ).toBe(true),
  )
})
