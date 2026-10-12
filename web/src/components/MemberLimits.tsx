import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { memberLimitOptions, setMemberLimits } from '@/lib/limits'
import type { Member } from '@/lib/members'
import { Button } from './ui/Button'
import { Input } from './ui/Input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from './ui/Dialog'

export function MemberLimits({
  member,
  onClose,
  returnFocus,
}: {
  member: Member
  onClose: () => void
  returnFocus: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const query = useQuery(memberLimitOptions(member.id))
  const [invalid, setInvalid] = useState(false)
  const mutation = useMutation({
    mutationFn: setMemberLimits,
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ['member-limits', member.id] })
      onClose()
    },
  })
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (mutation.isPending) return
    const data = new FormData(event.currentTarget)
    const rate = String(data.get('rate') ?? ''),
      concurrency = String(data.get('concurrency') ?? '')
    const rpm = Number(rate),
      max = Number(concurrency)
    const bad =
      !rate ||
      !concurrency ||
      !Number.isInteger(rpm) ||
      rpm < 0 ||
      rpm > 6000 ||
      !Number.isInteger(max) ||
      max < 0 ||
      max > 1000
    setInvalid(Boolean(bad))
    if (bad) return
    mutation.mutate({
      id: member.id,
      requests_per_minute: rpm,
      max_concurrency: max,
    })
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) onClose()
      }}
    >
      <DialogContent
        showCloseButton={!mutation.isPending}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          returnFocus()
        }}
      >
        <DialogHeader>
          <DialogTitle>
            {t('memberLimitsFor', { username: member.username })}
          </DialogTitle>
          <DialogDescription>{t('memberLimitsDescription')}</DialogDescription>
        </DialogHeader>
        {query.isPending ? (
          <p role="status" className="text-sm text-muted-foreground">
            {t('loadingMemberLimits')}
          </p>
        ) : query.isError ? (
          <div role="alert" className="space-y-3">
            <p className="text-sm text-error">{t('memberLimitsFailed')}</p>
            <Button variant="outline" onClick={() => query.refetch()}>
              {t('reconnect')}
            </Button>
          </div>
        ) : (
          <form noValidate onSubmit={submit} className="space-y-5">
            <div className="space-y-2">
              <label htmlFor="member-rpm" className="text-sm font-medium">
                {t('requestsPerMinute')}
              </label>
              <Input
                id="member-rpm"
                name="rate"
                type="number"
                min={0}
                max={6000}
                step={1}
                defaultValue={query.data.requests_per_minute}
                disabled={mutation.isPending}
                aria-describedby="member-limits-hint"
              />
            </div>
            <div className="space-y-2">
              <label
                htmlFor="member-concurrency"
                className="text-sm font-medium"
              >
                {t('memberConcurrency')}
              </label>
              <Input
                id="member-concurrency"
                name="concurrency"
                type="number"
                min={0}
                max={1000}
                step={1}
                defaultValue={query.data.max_concurrency}
                disabled={mutation.isPending}
                aria-describedby="member-limits-hint"
              />
            </div>
            <p
              id="member-limits-hint"
              className="text-sm text-muted-foreground"
            >
              {t('memberLimitsHint')}
            </p>
            {(invalid || mutation.isError) && (
              <p role="alert" className="text-sm text-error">
                {t(invalid ? 'memberLimitsInvalid' : 'memberLimitsFailed')}
              </p>
            )}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={onClose}
                disabled={mutation.isPending}
              >
                {t('cancel')}
              </Button>
              <Button type="submit" disabled={mutation.isPending}>
                {t('saveChanges')}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
