import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { LoaderCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { allocationErrorKey, deleteScheme } from '@/lib/allocations'
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

export function DeleteScheme({
  id,
  name,
  onDeleted,
}: {
  id: number
  name: string
  onDeleted: () => Promise<void>
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const mutation = useMutation({
    mutationFn: () => deleteScheme(id),
    onSuccess: () => {
      setOpen(false)
      return onDeleted()
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
          aria-label={t('allocationDeleteNamed', { name })}
        >
          {t('allocationDelete')}
        </Button>
      </DialogTrigger>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{t('allocationDeleteTitle')}</DialogTitle>
          <DialogDescription>
            {t('allocationDeleteDescription', { name })}
          </DialogDescription>
        </DialogHeader>
        {mutation.isError && (
          <p role="alert" className="text-sm text-error">
            {t(allocationErrorKey(mutation.error))}
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
            {t('allocationDeleteConfirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
