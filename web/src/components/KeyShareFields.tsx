import { useTranslation } from 'react-i18next'
import type { AllocationKeyCandidate } from '@/lib/allocations'
import { Input } from './ui/Input'

export type KeyShareWindow = { id: string; label: string }

export function KeyShareFields({
  keys,
  windows,
  selected,
  values,
  onSelect,
  onChange,
}: {
  keys: AllocationKeyCandidate[]
  windows: KeyShareWindow[]
  selected: Record<number, boolean>
  values: Record<number, Record<string, string>>
  onSelect: (keys: number[], selected: boolean) => void
  onChange: (key: number, window: string, value: string) => void
}) {
  const { t } = useTranslation()
  return (
    <section className="space-y-4 border-t border-border pt-7">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 className="text-base font-semibold">
          {t('allocationKeysSection')}
        </h3>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={keys.length > 0 && keys.every((key) => selected[key.id])}
            disabled={!keys.length}
            onChange={(event) =>
              onSelect(
                keys.map((key) => key.id),
                event.target.checked,
              )
            }
            className="size-4 accent-primary"
          />
          {t('allocationSelectAllKeys')}
        </label>
      </div>
      <p className="text-sm leading-6 text-muted-foreground">
        {t('allocationKeysHint')}
      </p>
      {keys.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t('allocationKeysEmpty')}
        </p>
      ) : (
        <div className="divide-y divide-border">
          {keys.map((key) => {
            const name = `${key.name} (${key.prefix}…)`
            return (
              <div key={key.id} className="space-y-3 py-3">
                <label className="flex min-w-0 items-start gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={!!selected[key.id]}
                    onChange={(event) =>
                      onSelect([key.id], event.target.checked)
                    }
                    aria-label={t('allocationIncludeKey', { name })}
                    className="mt-0.5 size-4 shrink-0 accent-primary"
                  />
                  <span className="min-w-0 space-y-0.5">
                    <span className="block break-words font-medium">
                      {name}
                    </span>
                    <span className="block text-xs text-muted-foreground">
                      {t('allocationKeyOwner', { owner: key.owner })}
                    </span>
                  </span>
                </label>
                {selected[key.id] && windows.length > 0 && (
                  <div className="grid gap-3 ps-6 sm:grid-cols-2">
                    {windows.map((window) => (
                      <label key={window.id} className="space-y-2 text-sm">
                        <span className="block text-muted-foreground">
                          {window.label}
                        </span>
                        <span className="flex items-center gap-2">
                          <Input
                            aria-label={t('allocationKeyShareFor', {
                              duration: window.label,
                              name,
                            })}
                            value={values[key.id]?.[window.id] ?? ''}
                            onChange={(event) =>
                              onChange(key.id, window.id, event.target.value)
                            }
                            inputMode="decimal"
                            className="min-w-0 text-right tabular-nums"
                          />
                          <span className="w-4 text-muted-foreground">%</span>
                        </span>
                      </label>
                    ))}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}
