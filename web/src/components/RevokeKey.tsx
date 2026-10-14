import { useRef, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { LoaderCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { revokeKey, type APIKey } from '@/lib/keys'
import { Button } from './ui/Button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from './ui/Dialog'

export function RevokeKey({
  value,
  onRevoked,
  focusAfterRevoke,
}: {
  value: APIKey
  onRevoked: (key: APIKey) => Promise<void>
  focusAfterRevoke: () => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const completed = useRef(false)
  const mutation = useMutation({
    mutationFn: () => revokeKey(value.id),
    onSuccess: (key) => {
      completed.current = true
      setOpen(false)
      return onRevoked(key)
    },
  })
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!mutation.isPending) {
          mutation.reset()
          setOpen(next)
        }
      }}
    >
      <DialogTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          disabled={value.revoked_at !== null}
          aria-label={t('revokeKeyNamed', { name: value.name })}
        >
          {t('revoke')}
        </Button>
      </DialogTrigger>
      <DialogContent
        showCloseButton={false}
        onCloseAutoFocus={(event) => {
          if (completed.current) {
            event.preventDefault()
            focusAfterRevoke()
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>{t('revokeKeyTitle')}</DialogTitle>
          <DialogDescription>
            {t('revokeKeyDescription', { name: value.name })}
          </DialogDescription>
        </DialogHeader>
        <code className="text-sm text-muted-foreground">{value.prefix}…</code>
        {mutation.isError && (
          <p role="alert" className="text-sm text-error">
            {t('keyRevokeFailed')}
          </p>
        )}
        <DialogFooter>
          <Button
            variant="outline"
            disabled={mutation.isPending}
            onClick={() => setOpen(false)}
          >
            {t('cancel')}
          </Button>
          <Button
            variant="destructive"
            disabled={mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {mutation.isPending && (
              <LoaderCircle
                className="motion-safe:animate-spin"
                aria-hidden="true"
              />
            )}
            {t('revokeKey')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
