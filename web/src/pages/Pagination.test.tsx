import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { it, expect, vi } from 'vitest'
import { App } from '@/App'
import { createAppRouter } from '@/router'
import { authenticated, workspaces } from '@/test/fixtures'

it.each([
  ['/admin/audit', '/api/audit', 'events'],
  ['/members', '/api/members', 'members'],
  ['/keys', '/api/keys', 'keys'],
  ['/pricing', '/api/pricing', 'prices'],
])(
  'uses the complete shared paginator in %s',
  async (path, endpoint, field) => {
    const rows = Array.from({ length: 121 }, (_, i) => {
      if (field === 'events')
        return {
          id: 121 - i,
          actor_id: 1,
          actor_name: 'synthetic-admin',
          actor_role: 'admin',
          source: 'user',
          action: 'key.update',
          resource: 'key',
          resource_id: String(i),
          outcome: 'success',
          http_status: 200,
          created_at: 1900000000,
        }
      if (field === 'members')
        return {
          id: 121 - i,
          username: `synthetic-member-${i}`,
          role: 'member',
          enabled: true,
          created_at: 1900000000,
        }
      if (field === 'keys')
        return {
          id: 121 - i,
          copyable: false,
          enabled: true,
          expires_at: null,
          group_id: 1,
          group_name: 'Synthetic group',
          group_access: 'allowed',
          name: `Synthetic key ${i}`,
          prefix: 'sl_synthetic',
          created_at: 1900000000,
          last_used_at: null,
          revoked_at: null,
        }
      return {
        model: `synthetic-model-${i}`,
        input: 1250000,
        cached: 125000,
        output: 10000000,
        source: 'remote',
      }
    })
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        const parsed = new URL(url, 'http://example.test')
        let data: unknown = { groups: [] }
        if (url === '/api/auth/state') data = authenticated
        if (url === '/api/workspaces') data = workspaces
        if (parsed.pathname === endpoint) {
          const size = Number(parsed.searchParams.get('limit') || 50)
          const page = Number(parsed.searchParams.get('page') || 1)
          data =
            field === 'prices'
              ? { unit: 'micro_usd_per_million_tokens', prices: rows }
              : {
                  [field]: rows.slice((page - 1) * size, page * size),
                  total: 121,
                  page,
                  page_size: size,
                  next_cursor: 0,
                  server_time: 1900000000,
                }
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
    await screen.findByText('Showing 1–50 of 121')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Page 3' }))
    await screen.findByText('Showing 101–121 of 121')
    await user.click(screen.getByRole('button', { name: 'Rows per page' }))
    await user.click(screen.getByRole('menuitemradio', { name: '100' }))
    await screen.findByText('Showing 1–100 of 121')
    expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(
      101,
    )
    await user.type(
      screen.getByRole('spinbutton', { name: 'Go to page' }),
      '2{Enter}',
    )
    await screen.findByText('Showing 101–121 of 121')
    expect(
      (screen.getByRole('button', { name: 'Next' }) as HTMLButtonElement)
        .disabled,
    ).toBe(true)
  },
)
