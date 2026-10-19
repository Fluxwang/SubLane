import { z } from 'zod'
import { queryOptions, type QueryClient } from '@tanstack/react-query'
import { request } from './request'
import { authKey, type AuthState } from './auth'
import { cachePage, paginationFields } from './pagination'
import { selectedWorkspace } from './workspace'

const memberSchema = z.object({
  id: z.number().int().positive(),
  username: z.string().min(3).max(32),
  role: z.enum(['member', 'admin', 'owner']),
  enabled: z.boolean(),
  created_at: z.number().int().nonnegative(),
})
const pageSchema = z.object({
  members: z.array(memberSchema).max(100),
  ...paginationFields,
})
export type Member = z.infer<typeof memberSchema>

const invitationSchema = z.object({
  token: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  expires_at: z.iso.datetime({ offset: true }),
})

export function createInvitation() {
  return request('/api/members/invitations', invitationSchema, {
    method: 'POST',
    body: '{}',
  })
}

export function memberOptions(
  client: QueryClient,
  page: number,
  pageSize: number,
) {
  const userID = client.getQueryData<AuthState>(authKey)?.user?.id
  const enabled = () => {
    const user = client.getQueryData<AuthState>(authKey)?.user
    return user?.id === userID && user?.role === 'admin'
  }
  const key = (value: number) => ['members', userID, value, pageSize] as const
  return queryOptions({
    queryKey: key(page),
    enabled,
    queryFn: async ({ signal }) => {
      const data = await request(
        `/api/members?page=${page}&limit=${pageSize}`,
        pageSchema,
        { signal },
      )
      if (enabled()) cachePage(client, key, page, data)
      return data
    },
  })
}
export function createMember(input: { username: string; password: string }) {
  return request('/api/members', memberSchema, {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function saveWorkspaceMember(input: {
  username: string
  role: 'member' | 'admin'
}) {
  return request(`/api/tenants/${selectedWorkspace()}/members`, memberSchema, {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function setWorkspaceMemberRole(id: number, role: 'member' | 'admin') {
  return request(`/api/members/${id}/role`, memberSchema, {
    method: 'PATCH',
    body: JSON.stringify({ role }),
  })
}
export function setMemberEnabled(input: { id: number; enabled: boolean }) {
  return request(`/api/members/${input.id}`, memberSchema, {
    method: 'PATCH',
    body: JSON.stringify({ enabled: input.enabled }),
  })
}

export function resetMemberPassword(id: number, password: string) {
  return request(`/api/members/${id}/password`, z.undefined(), {
    method: 'POST',
    body: JSON.stringify({ new_password: password }),
  })
}
