import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus } from 'lucide-react'
import {
  schemesOptions,
  availableAllocationPools,
  saveScheme,
  setSchemeEnabled,
  modeLabels,
  allocationErrorKey,
  allocationKeysOptions,
  type Scheme,
} from '@/lib/allocations'
import { DeleteScheme } from '@/components/DeleteScheme'
import { groupOptions, poolMembersOptions } from '@/lib/groups'
import { SchemeForm } from '@/components/SchemeForm'
import { AllocationPriceWarning } from '@/components/AllocationPriceWarning'
import { AllocationReport } from '@/components/AllocationReport'
import { Button } from '@/components/ui/Button'
import { Status } from '@/components/Status'
import { formatInstanceDate, useTimeZone } from '@/lib/timezone'

export function Allocations() {
  const { t, i18n } = useTranslation()
  const timeZone = useTimeZone()
  const query = useQuery(schemesOptions)
  const pools = useQuery(groupOptions)
  const client = useQueryClient()
  const [editing, setEditing] = useState<Scheme | null | undefined>()
  const [viewing, setViewing] = useState<number | null>(null)
  const [formPoolID, setFormPoolID] = useState(0)
  const saved = async () => {
    await Promise.all([
      client.invalidateQueries({ queryKey: ['allocations'] }),
      client.invalidateQueries({ queryKey: ['allocation'] }),
      client.invalidateQueries({ queryKey: ['own-allocations'] }),
      client.invalidateQueries({ queryKey: ['available-groups'] }),
      client.invalidateQueries({ queryKey: ['keys'] }),
      client.invalidateQueries({ queryKey: ['allocation-keys'] }),
    ])
    setEditing(undefined)
  }
  const mutation = useMutation({ mutationFn: saveScheme, onSuccess: saved })
  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) =>
      setSchemeEnabled(id, enabled),
    onSuccess: saved,
  })
  const freePools = availableAllocationPools(
    pools.data?.groups ?? [],
    query.data?.schemes ?? [],
  )
  const roster = useQuery({
    ...poolMembersOptions(formPoolID),
    enabled: editing !== undefined && formPoolID > 0,
  })
  const candidates = useQuery({
    ...allocationKeysOptions(formPoolID),
    enabled: editing !== undefined && formPoolID > 0,
  })
  const loading = query.isPending || pools.isPending
  const failed = query.isError || pools.isError
  const editingScheme = editing
    ? (query.data?.schemes.find((scheme) => scheme.id === editing.id) ??
      editing)
    : undefined
  const date = (n: number) =>
    formatInstanceDate(n * 1000, i18n.resolvedLanguage ?? 'en', timeZone, {
      dateStyle: 'short',
      timeStyle: 'medium',
    })
  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="page-title">{t('allocationSchemes')}</h1>
          <p className="page-description">{t('allocationsDescription')}</p>
        </div>
        {editing === undefined && viewing === null && (
          <Button
            disabled={loading || failed || !freePools.length}
            onClick={() => {
              mutation.reset()
              setFormPoolID(freePools[0].id)
              setEditing(null)
            }}
          >
            <Plus aria-hidden="true" />
            {t('allocationCreate')}
          </Button>
        )}
      </div>
      {!loading &&
        !failed &&
        editing === undefined &&
        viewing === null &&
        !freePools.length && (
          <p className="text-sm leading-6 text-muted-foreground">
            {t('allocationPrerequisite')}{' '}
            <Link className="underline underline-offset-4" to="/groups">
              {t('accountGroups')}
            </Link>
          </p>
        )}
      {toggle.isError && (
        <p role="alert" className="text-sm text-error">
          {t(allocationErrorKey(toggle.error))}
        </p>
      )}
      {loading ? (
        <p role="status">{t('allocationLoading')}</p>
      ) : failed ? (
        <div role="alert" className="space-y-3">
          <p>{t('allocationFailed')}</p>
          <Button
            variant="outline"
            onClick={() => Promise.all([query.refetch(), pools.refetch()])}
          >
            {t('reconnect')}
          </Button>
        </div>
      ) : editing !== undefined ? (
        <section className="max-w-3xl space-y-5">
          <h2 className="font-medium">
            {t(editing ? 'allocationEdit' : 'allocationCreate')}
          </h2>
          <SchemeForm
            key={editing?.id ?? 'new'}
            groups={editing ? pools.data.groups : freePools}
            groupID={formPoolID}
            onGroupChange={setFormPoolID}
            members={roster.data?.members ?? []}
            keys={candidates.data?.keys ?? []}
            scheme={editingScheme}
            onSubmit={(input) => mutation.mutate({ ...input, id: editing?.id })}
            onCancel={() => setEditing(undefined)}
            pending={
              mutation.isPending ||
              roster.isPending ||
              roster.isError ||
              candidates.isPending ||
              candidates.isError
            }
          />
          {candidates.isError && (
            <div
              role="alert"
              className="flex items-center gap-3 text-sm text-error"
            >
              {t('allocationKeysFailed')}
              <Button
                variant="outline"
                size="sm"
                onClick={() => candidates.refetch()}
              >
                {t('reconnect')}
              </Button>
            </div>
          )}
          {roster.isError && (
            <div
              role="alert"
              className="flex items-center gap-3 text-sm text-error"
            >
              {t('allocationRosterFailed')}
              <Button
                variant="outline"
                size="sm"
                onClick={() => roster.refetch()}
              >
                {t('reconnect')}
              </Button>
            </div>
          )}
          {!roster.isPending &&
            !roster.isError &&
            roster.data?.members.length === 0 && (
              <p className="text-sm text-muted-foreground">
                {t('allocationNoGrantedMembers')}
              </p>
            )}
          {mutation.isError && (
            <p role="alert" className="text-sm text-error">
              {t(allocationErrorKey(mutation.error))}
            </p>
          )}
        </section>
      ) : viewing !== null ? (
        <>
          <Button variant="outline" onClick={() => setViewing(null)}>
            {t('allocationBack')}
          </Button>
          <AllocationReport id={viewing} />
        </>
      ) : query.data.schemes.length === 0 ? (
        <div className="space-y-4 py-8">
          <p className="text-sm text-muted-foreground">
            {t('allocationsEmpty')}
          </p>
          <Button asChild variant="outline">
            <Link to="/groups">{t('accountGroups')}</Link>
          </Button>
        </div>
      ) : (
        <div className="divide-y divide-border rounded-xl border border-border">
          {query.data.schemes.map((s) => (
            <section
              key={s.id}
              className="flex flex-wrap items-center justify-between gap-4 p-5"
            >
              <div className="min-w-0 space-y-2">
                <div className="flex flex-wrap items-center gap-3">
                  <h2 className="break-words font-medium">{s.name}</h2>
                  <Status
                    kind={
                      s.enabled && s.effective_at <= query.dataUpdatedAt / 1000
                        ? 'success'
                        : 'neutral'
                    }
                  >
                    {t(
                      !s.enabled
                        ? 'disabled'
                        : s.effective_at > query.dataUpdatedAt / 1000
                          ? 'allocationWaiting'
                          : 'active',
                    )}
                  </Status>
                  <span className="text-sm text-muted-foreground">
                    {t(modeLabels[s.config.mode])}
                  </span>
                </div>
                <p className="break-words text-sm text-muted-foreground">
                  {s.group_name} ·{' '}
                  {s.config.mode === 'keys'
                    ? t('allocationKeySchedule', {
                        windows: s.config.windows?.length ?? 0,
                        keys: s.config.keys?.length ?? 0,
                      })
                    : s.config.period === 'durations'
                      ? t('allocationWindowSchedule', {
                          windows: s.config.windows?.length ?? 0,
                          zone: timeZone,
                        })
                      : s.config.period === 'day'
                        ? t('allocationDailySchedule', {
                            time: s.config.reset_time ?? '00:00',
                            zone: timeZone,
                          })
                        : t('allocationMonthlySchedule', {
                            day: s.config.reset_day ?? 1,
                            time: s.config.reset_time ?? '00:00',
                            zone: timeZone,
                          })}
                </p>
                {s.next && (
                  <p className="text-sm text-muted-foreground">
                    {t('allocationScheduled', {
                      mode: t(modeLabels[s.next.config.mode]),
                      date: date(s.next.effective_at),
                    })}{' '}
                    ·{' '}
                    {s.next.config.period === 'durations'
                      ? t('allocationWindowSchedule', {
                          windows: s.next.config.windows?.length ?? 0,
                          zone: timeZone,
                        })
                      : s.next.config.period === 'day'
                        ? t('allocationDailySchedule', {
                            time: s.next.config.reset_time ?? '00:00',
                            zone: timeZone,
                          })
                        : t('allocationMonthlySchedule', {
                            day: s.next.config.reset_day ?? 1,
                            time: s.next.config.reset_time ?? '00:00',
                            zone: timeZone,
                          })}
                  </p>
                )}
                {s.effective_at <= query.dataUpdatedAt / 1000 && (
                  <AllocationPriceWarning
                    coverage={s.price_coverage}
                    label={s.next ? t('allocationCurrentPricing') : undefined}
                    effectiveAt={s.edit_effective_at}
                  />
                )}
                {s.next && (
                  <AllocationPriceWarning
                    coverage={s.next.price_coverage}
                    label={t('allocationScheduledPricing')}
                    effectiveAt={s.next.effective_at}
                    timing="scheduled"
                  />
                )}
              </div>
              <div className="flex flex-wrap gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={toggle.isPending}
                  onClick={() =>
                    toggle.mutate({ id: s.id, enabled: !s.enabled })
                  }
                >
                  {t(s.enabled ? 'allocationPause' : 'allocationResume')}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setViewing(s.id)}
                >
                  {t('allocationView')}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  aria-label={t('allocationEditNamed', { name: s.name })}
                  onClick={() => {
                    mutation.reset()
                    setFormPoolID(s.group_id)
                    setEditing(s)
                  }}
                >
                  {t('allocationEdit')}
                </Button>
                {/* Only paused schemes can be deleted; the server enforces the same rule. */}
                {!s.enabled && (
                  <DeleteScheme id={s.id} name={s.name} onDeleted={saved} />
                )}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  )
}
