import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Eye, EyeOff, KeyRound, LoaderCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { authOptions } from '@/lib/auth'
import { keyOptions, keyState, type APIKey, type KeyPage } from '@/lib/keys'
import { CreateKey } from '@/components/CreateKey'
import { CatalogDialog } from '@/components/CatalogDialog'
import { CopyKey } from '@/components/CopyKey'
import { ClientGuide } from '@/components/ClientGuide'
import { CCSwitchImport } from '@/components/CCSwitchImport'
import { RevokeKey } from '@/components/RevokeKey'
import { EditKey } from '@/components/EditKey'
import { Status } from '@/components/Status'
import { Button } from '@/components/ui/Button'
import { formatInstanceDate, useTimeZone } from '@/lib/timezone'

export function Keys() {
  const { data } = useQuery(authOptions())
  if (!data?.user) return null
  // A cross-tab identity change must unmount dialogs as well as clear query data, including any displayed secret.
  return <KeyManager key={data.user.id} userID={data.user.id} />
}

function KeyManager({ userID }: { userID: number }) {
  const { t, i18n } = useTranslation()
  const timeZone = useTimeZone()
  const client = useQueryClient()
  const heading = useRef<HTMLHeadingElement>(null)
  const [cursors, setCursors] = useState([0])
  const [showRevoked, setShowRevoked] = useState(false)
  const [revokedNotice, setRevokedNotice] = useState(false)
  const query = useQuery(
    keyOptions(client, userID, cursors[cursors.length - 1], showRevoked),
  )
  const [clock, setClock] = useState(() => Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setClock(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  const now = query.data
    ? query.data.server_time + Math.max(0, clock - query.dataUpdatedAt) / 1000
    : clock / 1000
  const invalidate = async () => {
    await Promise.all([
      client.invalidateQueries({ queryKey: ['keys', userID] }),
      client.invalidateQueries({ queryKey: ['connection', userID] }),
      client.invalidateQueries({ queryKey: ['system'] }),
    ])
  }
  const onRevoked = async (revokedKey: APIKey) => {
    const filter = { queryKey: ['keys', userID] }
    // Cancel older reads before publishing the confirmed mutation, then refill filtered pages.
    await client.cancelQueries(filter)
    for (const [queryKey, page] of client.getQueriesData<KeyPage>(filter)) {
      if (!page) continue
      const included = queryKey[3] === true
      client.setQueryData<KeyPage>(queryKey, {
        ...page,
        revoked_count: page.revoked_count + 1,
        keys: included
          ? page.keys.map((key) =>
              key.id === revokedKey.id ? revokedKey : key,
            )
          : page.keys.filter((key) => key.id !== revokedKey.id),
      })
    }
    setRevokedNotice(!showRevoked)
    setCursors([0])
    await invalidate()
  }
  const dates = new Intl.DateTimeFormat(i18n.resolvedLanguage ?? 'en', {
    timeZone,
    dateStyle: 'medium',
  })
  const allRevoked = !showRevoked && (query.data?.revoked_count ?? 0) > 0
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 ref={heading} tabIndex={-1} className="page-title outline-none">
            {t('apiKeys')}
          </h1>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <ClientGuide userID={userID} />
          <CreateKey
            userID={userID}
            onCreated={() => {
              setCursors([0])
              return invalidate()
            }}
          />
        </div>
      </div>
      <p
        data-tour="access"
        tabIndex={-1}
        className="text-sm leading-6 text-muted-foreground"
      >
        {t('gatewayKeysNotice')}
      </p>
      {revokedNotice && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('keyRevokedNotice')}
        </p>
      )}
      {query.data && (
        <div className="flex justify-end">
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground aria-pressed:text-foreground"
            aria-pressed={showRevoked}
            disabled={
              query.isFetching ||
              (!showRevoked && query.data.revoked_count === 0)
            }
            onClick={() => {
              setCursors([0])
              setRevokedNotice(false)
              setShowRevoked((value) => !value)
            }}
          >
            {showRevoked ? (
              <EyeOff aria-hidden="true" />
            ) : (
              <Eye aria-hidden="true" />
            )}
            {t('showRevokedKeys', { count: query.data.revoked_count })}
          </Button>
        </div>
      )}
      {query.isPending ? (
        <p
          role="status"
          className="flex items-center gap-2 py-10 text-sm text-muted-foreground"
        >
          <LoaderCircle
            className="size-4 motion-safe:animate-spin"
            aria-hidden="true"
          />
          {t('loadingKeys')}
        </p>
      ) : query.isError ? (
        <div
          role="alert"
          className="space-y-4 rounded-xl border border-border p-6"
        >
          <p className="text-sm text-error">{t('keysLoadFailed')}</p>
          <Button
            variant="outline"
            onClick={() => query.refetch()}
            disabled={query.isFetching}
          >
            {t('reconnect')}
          </Button>
        </div>
      ) : query.data.keys.length === 0 ? (
        <section className="flex flex-col items-center rounded-xl border border-dashed border-border p-10 text-center">
          <KeyRound
            className="mb-4 size-8 text-muted-foreground"
            aria-hidden="true"
          />
          <h2 className="font-medium">
            {allRevoked
              ? t('noUsableKeys', { count: query.data.revoked_count })
              : t('noKeys')}
          </h2>
          <p className="mt-2 text-sm text-muted-foreground">
            {t(allRevoked ? 'noUsableKeysDescription' : 'noKeysDescription')}
          </p>
          {allRevoked && (
            <Button
              className="mt-4"
              variant="outline"
              disabled={query.isFetching}
              onClick={() => {
                setCursors([0])
                setRevokedNotice(false)
                setShowRevoked(true)
              }}
            >
              {t('showRevoked')}
            </Button>
          )}
        </section>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border bg-card">
          <table className="w-full text-left text-sm">
            <thead className="border-b border-border text-muted-foreground">
              <tr>
                <th scope="col" className="px-5 py-3 font-medium">
                  {t('keyName')}
                </th>
                <th scope="col" className="px-5 py-3 font-medium">
                  {t('memberStatus')}
                </th>
                <th scope="col" className="px-5 py-3 font-medium">
                  {t('keyLastUsed')}
                </th>
                <th scope="col" className="px-5 py-3 text-right font-medium">
                  {t('actions')}
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {query.data.keys.map((key) => (
                <tr key={key.id}>
                  <td className="min-w-48 max-w-64 px-5 py-4">
                    <p className="break-words font-medium">{key.name}</p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t('keyGroupName', {
                        name: key.group_name,
                      })}
                    </p>
                    <CopyKey
                      key={String(key.copyable)}
                      value={key}
                      userID={userID}
                    />
                    <div className="-ml-2 mt-1">
                      <CatalogDialog
                        target={{ kind: 'key', id: key.id }}
                        name={key.name}
                        disabled={keyState(key, now) !== 'active'}
                      />
                    </div>
                  </td>
                  <td className="px-5 py-4">
                    <Status
                      kind={
                        keyState(key, now) === 'active'
                          ? 'success'
                          : ['keyExpired', 'keyGroupUnavailable'].includes(
                                keyState(key, now),
                              )
                            ? 'warning'
                            : 'neutral'
                      }
                    >
                      {t(keyState(key, now))}
                    </Status>
                    <p
                      className="mt-2 whitespace-nowrap text-xs text-muted-foreground"
                      title={
                        key.expires_at === null
                          ? undefined
                          : formatInstanceDate(
                              key.expires_at * 1000,
                              i18n.resolvedLanguage ?? 'en',
                              timeZone,
                              { dateStyle: 'short', timeStyle: 'medium' },
                            )
                      }
                    >
                      {key.expires_at === null
                        ? t('keyNeverExpires')
                        : t('keyExpiresOn', {
                            date: dates.format(key.expires_at * 1000),
                          })}
                    </p>
                  </td>
                  <td className="whitespace-nowrap px-5 py-4 text-muted-foreground">
                    {key.last_used_at === null
                      ? t('neverUsed')
                      : dates.format(key.last_used_at * 1000)}
                  </td>
                  <td className="px-5 py-4 text-right">
                    <div className="flex justify-end gap-2">
                      <ClientGuide
                        value={key}
                        userID={userID}
                        disabled={keyState(key, now) !== 'active'}
                      />
                      <CCSwitchImport
                        value={key}
                        userID={userID}
                        usable={keyState(key, now) === 'active'}
                      />
                      <EditKey value={key} onSaved={invalidate} />
                      <RevokeKey
                        value={key}
                        onRevoked={onRevoked}
                        focusAfterRevoke={() => heading.current?.focus()}
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {query.data && (cursors.length > 1 || query.data.next_cursor !== 0) && (
        <div className="flex justify-end gap-2">
          <Button
            variant="outline"
            disabled={cursors.length === 1 || query.isFetching}
            onClick={() => setCursors((value) => value.slice(0, -1))}
          >
            {t('previousPage')}
          </Button>
          <Button
            variant="outline"
            disabled={!query.data.next_cursor || query.isFetching}
            onClick={() =>
              setCursors((value) => [...value, query.data.next_cursor])
            }
          >
            {t('nextPage')}
          </Button>
        </div>
      )}
    </div>
  )
}
