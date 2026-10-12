import { useState, type FormEvent } from 'react'
import { useMutation } from '@tanstack/react-query'
import { LoaderCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { Account } from '@/lib/accounts'
import {
  reasonKeys,
  resumeAccount,
  setConcurrency,
  type AccountRuntime,
} from '@/lib/runtime'
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

export function AccountLimits({
  account,
  runtime,
  onClose,
  onChanged,
  restoreFocus,
  updateConcurrency = setConcurrency,
  resumeResource = resumeAccount,
}: {
  account: Pick<Account, 'id' | 'name' | 'max_concurrency'>
  updateConcurrency?: (input: {
    id: string
    max_concurrency: number
  }) => Promise<unknown>
  resumeResource?: (id: string) => Promise<unknown>
  runtime?: AccountRuntime
  onClose: () => void
  onChanged: () => Promise<void>
  restoreFocus: () => void
}) {
  const { t } = useTranslation()
  const [limit, setLimit] = useState(
    String(runtime?.max_concurrency ?? account.max_concurrency),
  )
  const [invalid, setInvalid] = useState(false)
  const [resumed, setResumed] = useState(false)
  const update = useMutation({
    mutationFn: updateConcurrency,
    onSuccess: async () => {
      await onChanged()
      onClose()
    },
  })
  const resume = useMutation({
    mutationFn: resumeResource,
    onSuccess: async () => {
      setResumed(true)
      await onChanged()
    },
  })
  const busy = update.isPending || resume.isPending
  const close = () => {
    if (!busy) onClose()
  }
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (busy) return
    const value = Number(limit)
    const valid = Number.isInteger(value) && value >= 1 && value <= 1000
    setInvalid(!valid)
    if (valid) update.mutate({ id: account.id, max_concurrency: value })
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) close()
      }}
    >
      <DialogContent
        showCloseButton={false}
        onInteractOutside={(event) => {
          if (busy) event.preventDefault()
        }}
        onEscapeKeyDown={(event) => {
          if (busy) event.preventDefault()
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault()
          restoreFocus()
        }}
      >
        <DialogHeader>
          <DialogTitle>{t('accountScheduling')}</DialogTitle>
          <DialogDescription>{account.name}</DialogDescription>
        </DialogHeader>
        <form className="space-y-5" onSubmit={submit} noValidate>
          <div className="space-y-2">
            <label
              htmlFor="account-concurrency"
              className="text-sm font-medium"
            >
              {t('accountConcurrencyLimit')}
            </label>
            <Input
              id="account-concurrency"
              type="number"
              min={1}
              max={1000}
              step={1}
              value={limit}
              onChange={(event) => setLimit(event.target.value)}
              aria-invalid={invalid}
              disabled={busy}
            />
            <p className="text-xs leading-5 text-muted-foreground">
              {t('accountConcurrencyHint')}
            </p>
            {invalid && (
              <p role="alert" className="text-sm text-error">
                {t('accountConcurrencyInvalid')}
              </p>
            )}
          </div>
          {runtime && (runtime.failures > 0 || runtime.limited_models > 0) && (
            <div className="space-y-3 border-t border-border pt-4">
              {runtime.failures > 0 && (
                <p className="text-sm">
                  {t('lastAccountFailure')}:{' '}
                  {t(reasonKeys[runtime.reason] ?? 'reasonUnknown')}
                </p>
              )}
              {runtime.limited_models > 0 && (
                <p className="text-sm">
                  {t('accountModelLimits', { count: runtime.limited_models })}
                </p>
              )}
              <p className="text-xs leading-5 text-muted-foreground">
                {t('resumeAccountHint')}
              </p>
              {(runtime.cooldown_until > 0 || runtime.limited_models > 0) && (
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => resume.mutate(account.id)}
                >
                  {t('resumeAccount')}
                </Button>
              )}
            </div>
          )}
          {resumed && (
            <p role="status" className="text-sm text-success">
              {t('accountResumed')}
            </p>
          )}
          {(update.isError || resume.isError) && (
            <p role="alert" className="text-sm text-error">
              {t('accountSchedulingFailed')}
            </p>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={close}
              disabled={busy}
            >
              {t('cancel')}
            </Button>
            <Button type="submit" disabled={busy}>
              {update.isPending && (
                <LoaderCircle
                  aria-hidden="true"
                  className="motion-safe:animate-spin"
                />
              )}
              {t('saveAccountScheduling')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
