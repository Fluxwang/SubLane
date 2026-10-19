import { Pagination } from '@/components/Pagination'
import { TableViewport } from '@/components/TableViewport'
import { useTablePagination } from '@/lib/pagination'
import { useState } from 'react'
import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { Copy, Link2, LoaderCircle, Plus, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  createInvitation,
  memberOptions,
  setMemberEnabled,
  type Member,
} from '@/lib/members'
import { authOptions } from '@/lib/auth'
import { selectedWorkspace } from '@/lib/workspace'
import { Button } from '@/components/ui/Button'
import { Status } from '@/components/Status'
import { CreateMember } from '@/components/CreateMember'
import { GroupAccess } from '@/components/GroupAccess'
import { MemberActions } from '@/components/MemberActions'
import { MembershipEditor } from '@/components/MembershipEditor'
import { useTimeZone } from '@/lib/timezone'

export function Members() {
  const { t, i18n } = useTranslation()
  const timeZone = useTimeZone()
  const client = useQueryClient()
  const session = useQuery(authOptions())
  const canResetPasswords =
    session.data?.user?.id === 1 && selectedWorkspace() === 1
  const { page, pageSize, viewport, changePage, changePageSize, syncPage } =
    useTablePagination()
  const [creating, setCreating] = useState(false)
  const [membershipTarget, setMembershipTarget] = useState<
    Member | 'new' | null
  >(null)
  const [createdMember, setCreatedMember] = useState<Member | null>(null)
  const [accessSaved, setAccessSaved] = useState<Member | null>(null)
  const [resetName, setResetName] = useState('')
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'failed'>(
    'idle',
  )
  const invitation = useMutation({ mutationFn: createInvitation, gcTime: 0 })
  // The fragment keeps the secret out of the HTTP request that loads the registration page.
  const invitationLink = invitation.data
    ? `${window.location.origin}/invite?workspace=${selectedWorkspace()}#${invitation.data.token}`
    : ''
  const query = useQuery({
    ...memberOptions(client, page, pageSize),
    placeholderData: keepPreviousData,
  })
  syncPage(query.data?.page, query.isPlaceholderData)
  const update = useMutation({
    mutationFn: setMemberEnabled,
    onSuccess: () => client.invalidateQueries({ queryKey: ['members'] }),
  })
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="page-title">{t('members')}</h1>
          <p className="page-description">{t('membersDescription')}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={invitation.isPending}
            onClick={() => {
              setCopyState('idle')
              invitation.mutate()
            }}
          >
            {invitation.isPending ? (
              <LoaderCircle
                className="motion-safe:animate-spin"
                aria-hidden="true"
              />
            ) : (
              <Link2 aria-hidden="true" />
            )}
            {t('createInviteLink')}
          </Button>
          <Button variant="outline" onClick={() => setMembershipTarget('new')}>
            {t('memberExistingAdd')}
          </Button>
          <Button
            onClick={() => {
              setCreatedMember(null)
              setCreating(true)
            }}
          >
            <Plus aria-hidden="true" />
            {t('addMember')}
          </Button>
        </div>
      </div>
      {invitation.isError && (
        <p role="alert" className="text-sm text-error">
          {t('inviteCreateFailed')}
        </p>
      )}
      {invitation.data && (
        <section
          className="space-y-3 rounded-xl border border-border bg-card p-4"
          aria-label={t('invitationLink')}
        >
          <div>
            <h2 className="text-sm font-medium">{t('invitationLink')}</h2>
            <p className="mt-1 text-sm leading-6 text-muted-foreground">
              {t('inviteLinkHint')}
            </p>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row">
            <label htmlFor="member-invitation" className="sr-only">
              {t('invitationLink')}
            </label>
            <input
              id="member-invitation"
              className="min-w-0 flex-1 rounded-md border border-input bg-background px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              readOnly
              value={invitationLink}
              onFocus={(event) => event.currentTarget.select()}
            />
            <Button
              variant="outline"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(invitationLink)
                  setCopyState('copied')
                } catch {
                  setCopyState('failed')
                }
              }}
            >
              <Copy aria-hidden="true" />
              {t('copyInviteLink')}
            </Button>
          </div>
          {copyState !== 'idle' && (
            <p
              role={copyState === 'failed' ? 'alert' : 'status'}
              className={
                copyState === 'failed'
                  ? 'text-sm text-error'
                  : 'text-sm text-success'
              }
            >
              {t(copyState === 'failed' ? 'inviteCopyFailed' : 'inviteCopied')}
            </p>
          )}
        </section>
      )}
      {createdMember && (
        <div
          role="status"
          className="flex flex-wrap items-center gap-3 text-sm text-success"
        >
          <span>
            {t('memberCreated', { username: createdMember.username })}
          </span>
          <GroupAccess member={createdMember} />
        </div>
      )}
      {accessSaved && (
        <div
          role="status"
          className="flex flex-wrap items-center gap-3 text-sm text-success"
        >
          <span>
            {t('memberAccessSaved', { username: accessSaved.username })}
          </span>
          {accessSaved.role === 'member' && (
            <GroupAccess member={accessSaved} />
          )}
        </div>
      )}
      {update.isError && (
        <p role="alert" className="text-sm text-error">
          {t('memberUpdateFailed')}
        </p>
      )}
      {resetName && (
        <p role="status" className="text-sm text-success">
          {t('memberPasswordReset', { username: resetName })}
        </p>
      )}
      {query.isPending ? (
        <div
          role="status"
          className="flex items-center gap-2 py-10 text-sm text-muted-foreground"
        >
          <LoaderCircle
            className="size-4 motion-safe:animate-spin"
            aria-hidden="true"
          />
          {t('loadingMembers')}
        </div>
      ) : query.isError ? (
        <div
          role="alert"
          className="space-y-4 rounded-xl border border-border p-6"
        >
          <p className="text-sm text-error">{t('membersLoadFailed')}</p>
          <Button
            variant="outline"
            onClick={() => query.refetch()}
            disabled={query.isFetching}
          >
            {t('reconnect')}
          </Button>
        </div>
      ) : query.data.members.length === 0 ? (
        <section className="flex flex-col items-center rounded-xl border border-dashed border-border px-6 py-14 text-center">
          <Users
            className="mb-4 size-8 text-muted-foreground"
            aria-hidden="true"
          />
          <h2 className="font-medium">{t('noMembers')}</h2>
          <p className="mt-2 max-w-md text-sm leading-6 text-muted-foreground">
            {t('noMembersDescription')}
          </p>
        </section>
      ) : (
        <TableViewport ref={viewport} aria-busy={query.isFetching}>
          <table className="w-full text-left text-sm">
            <thead className="border-b border-border text-muted-foreground">
              <tr>
                <th scope="col" className="px-5 py-3 font-medium">
                  {t('username')}
                </th>
                <th scope="col" className="px-5 py-3 font-medium">
                  {t('memberStatus')}
                </th>
                <th scope="col" className="px-5 py-3 font-medium">
                  {t('role')}
                </th>
                <th scope="col" className="px-5 py-3 font-medium">
                  {t('memberCreatedAt')}
                </th>
                <th scope="col" className="px-5 py-3 text-right font-medium">
                  {t('actions')}
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {query.data.members.map((member) => (
                <tr key={member.id}>
                  <td className="max-w-64 break-words px-5 py-4 font-medium">
                    {member.username}
                  </td>
                  <td className="px-5 py-4">
                    <Status kind={member.enabled ? 'success' : 'neutral'}>
                      {t(member.enabled ? 'active' : 'disabled')}
                    </Status>
                  </td>
                  <td className="px-5 py-4">
                    {t(
                      member.role === 'owner'
                        ? 'owner'
                        : member.role === 'admin'
                          ? 'administrator'
                          : 'member',
                    )}
                  </td>
                  <td className="whitespace-nowrap px-5 py-4 text-muted-foreground">
                    <time
                      dateTime={new Date(
                        member.created_at * 1000,
                      ).toISOString()}
                    >
                      {new Intl.DateTimeFormat(i18n.resolvedLanguage ?? 'en', {
                        timeZone,
                        dateStyle: 'medium',
                      }).format(member.created_at * 1000)}
                    </time>
                  </td>
                  <td className="px-5 py-4 text-right">
                    <div className="flex items-center justify-end gap-2">
                      {member.role === 'member' && (
                        <>
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={update.isPending}
                            aria-label={t(
                              member.enabled
                                ? 'disableMemberNamed'
                                : 'enableMemberNamed',
                              { username: member.username },
                            )}
                            onClick={() =>
                              update.mutate({
                                id: member.id,
                                enabled: !member.enabled,
                              })
                            }
                          >
                            {update.isPending &&
                              update.variables.id === member.id && (
                                <LoaderCircle
                                  className="motion-safe:animate-spin"
                                  aria-hidden="true"
                                />
                              )}
                            {t(
                              member.enabled ? 'disableMember' : 'enableMember',
                            )}
                          </Button>
                          <GroupAccess member={member} />
                        </>
                      )}
                      {member.role !== 'owner' &&
                        member.id !== session.data?.user?.id && (
                          <Button
                            variant="ghost"
                            size="sm"
                            aria-label={t('memberRoleChangeNamed', {
                              username: member.username,
                            })}
                            onClick={() => setMembershipTarget(member)}
                          >
                            {t('memberRoleChange')}
                          </Button>
                        )}
                      <MemberActions
                        member={member}
                        onPasswordReset={
                          canResetPasswords && member.role !== 'owner'
                            ? () => setResetName(member.username)
                            : undefined
                        }
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableViewport>
      )}
      {query.data && (
        <Pagination
          page={query.data.page}
          pageSize={query.data.page_size}
          total={query.data.total}
          disabled={query.isFetching || update.isPending}
          onPageChange={changePage}
          onPageSizeChange={changePageSize}
        />
      )}
      {creating && (
        <CreateMember
          onClose={() => setCreating(false)}
          onCreated={(member) => {
            setCreatedMember(member)
            setCreating(false)
            changePage(1)
            return client.invalidateQueries({ queryKey: ['members'] })
          }}
        />
      )}
      {membershipTarget && (
        <MembershipEditor
          member={membershipTarget === 'new' ? undefined : membershipTarget}
          onClose={() => setMembershipTarget(null)}
          onSaved={async (member) => {
            setAccessSaved(member)
            setMembershipTarget(null)
            changePage(1)
            await Promise.all([
              client.invalidateQueries({ queryKey: ['members'] }),
              client.invalidateQueries({ queryKey: ['auth'] }),
            ])
          }}
        />
      )}
    </div>
  )
}
