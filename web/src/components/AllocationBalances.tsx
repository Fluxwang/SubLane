import { useTranslation } from 'react-i18next'
import { type AllocationDetail, allocationValue } from '@/lib/allocations'
import { Status } from './Status'
import { formatInstanceDate, useTimeZone } from '@/lib/timezone'

export function AllocationBalances({ detail }: { detail: AllocationDetail }) {
  const { t, i18n } = useTranslation()
  const timeZone = useTimeZone()
  const date = (n: number) =>
    formatInstanceDate(n * 1000, i18n.resolvedLanguage ?? 'en', timeZone, {
      dateStyle: 'short',
      timeStyle: 'medium',
    })
  const keyMode = detail.config.mode === 'keys'
  // Key-share balances belong to keys; every other mode reports per member.
  const members = new Map<number, AllocationDetail['balances']>()
  for (const balance of detail.balances) {
    const recipient = keyMode ? (balance.key_id ?? 0) : balance.user_id
    const windows = members.get(recipient) ?? []
    windows.push(balance)
    members.set(recipient, windows)
  }
  return (
    <div className="space-y-4">
      {detail.config.mode === 'ratio' && (
        <p className="text-sm leading-6 text-muted-foreground">
          {t(
            detail.config.ratio_unit === 'amount'
              ? 'allocationRatioAmountBalanceHint'
              : 'allocationRatioTokensBalanceHint',
          )}
        </p>
      )}
      {!detail.available && (
        <p className="text-sm text-muted-foreground">
          {t(keyMode ? 'allocationKeysNotLimiting' : 'allocationUnavailable')}
        </p>
      )}
      {detail.balances.length === 0 ? (
        <p className="py-4 text-sm text-muted-foreground">
          {t(keyMode ? 'allocationKeyNoBalances' : 'allocationNoBalances')}
        </p>
      ) : (
        <div className="divide-y divide-border">
          {[...members].map(([userID, windows]) => {
            const b = windows[0]
            const state = !detail.available
              ? keyMode
                ? 'allocationNotLimitingShort'
                : 'allocationUnavailableShort'
              : windows.some((window) => window.admission === 'exhausted')
                ? 'allocationExhausted'
                : windows.some((window) => window.admission === 'risk_limited')
                  ? 'allocationRiskPaused'
                  : 'active'
            return (
              <section key={userID} className="space-y-3 py-4">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <h3 className="break-words text-sm font-medium">
                    {keyMode ? `${b.key_name} (${b.key_prefix}…)` : b.username}
                  </h3>
                  <Status
                    kind={
                      !detail.available
                        ? 'neutral'
                        : state === 'allocationRiskPaused'
                          ? 'warning'
                          : state === 'allocationExhausted'
                            ? 'error'
                            : 'success'
                    }
                  >
                    {t(state)}
                  </Status>
                </div>
                {windows.map((window, index) => {
                  const unit = window.mode === 'amount' ? 'USD' : 'M'
                  return (
                    <div
                      key={`${window.window_kind}-${window.window_seconds}`}
                      className={
                        index > 0
                          ? 'space-y-3 border-t border-border pt-3'
                          : 'space-y-3'
                      }
                    >
                      {(windows.length > 1 ||
                        detail.config.mode === 'windows' ||
                        keyMode) && (
                        <div className="flex flex-wrap items-center justify-between gap-2 text-sm font-medium">
                          <h4>
                            {t(
                              window.window_seconds % 86400 === 0
                                ? 'allocationDurationDays'
                                : 'allocationDurationHours',
                              {
                                value:
                                  window.window_seconds % 86400 === 0
                                    ? window.window_seconds / 86400
                                    : window.window_seconds / 3600,
                              },
                            )}
                            {keyMode &&
                              ` · ${t('allocationKeyShareValue', {
                                share: allocationValue(
                                  window.share ?? 0,
                                  'ratio',
                                ),
                              })}`}
                          </h4>
                          {(window.admission === 'exhausted' ||
                            window.admission === 'risk_limited') && (
                            <span
                              className={
                                window.admission === 'exhausted'
                                  ? 'text-error'
                                  : 'text-warning'
                              }
                            >
                              {t(
                                window.admission === 'exhausted'
                                  ? 'allocationWindowReached'
                                  : 'allocationWindowRisk',
                              )}
                            </span>
                          )}
                        </div>
                      )}
                      <dl className="grid grid-cols-3 gap-3 text-sm">
                        <div>
                          <dt className="text-muted-foreground">
                            {t('allocationLimit')}
                          </dt>
                          <dd className="mt-1 tabular-nums">
                            {window.admission === 'unlimited'
                              ? t('allocationUnlimited')
                              : `${allocationValue(window.limit, window.mode)} ${unit}`}
                          </dd>
                        </div>
                        <div>
                          <dt className="text-muted-foreground">
                            {t('allocationUsed')}
                          </dt>
                          <dd className="mt-1 tabular-nums">
                            {allocationValue(window.used, window.mode)} {unit}
                          </dd>
                        </div>
                        <div>
                          <dt className="text-muted-foreground">
                            {t('allocationRemaining')}
                          </dt>
                          <dd
                            className="mt-1 tabular-nums"
                            aria-label={
                              window.admission === 'unlimited'
                                ? t('allocationUnlimited')
                                : undefined
                            }
                          >
                            {window.admission === 'unlimited'
                              ? '—'
                              : `${allocationValue(Math.max(0, window.limit - window.used), window.mode)} ${unit}`}
                          </dd>
                        </div>
                      </dl>
                      {(window.in_flight > 0 || window.pending_current > 0) &&
                        window.admission === 'unlimited' && (
                          <p className="text-sm leading-6 text-muted-foreground">
                            {t('allocationUnmeteredExposure', {
                              inFlight: window.in_flight,
                              pending: window.pending_current,
                            })}
                          </p>
                        )}
                      {(window.in_flight > 0 || window.pending_current > 0) &&
                        window.admission !== 'unlimited' && (
                          <div className="space-y-1 text-sm leading-6 text-muted-foreground">
                            {window.in_flight > 0 && (
                              <p>
                                {t('allocationRiskExposure', {
                                  inFlight: window.in_flight,
                                  reserved: allocationValue(
                                    window.reserved,
                                    window.mode,
                                  ),
                                  unit,
                                })}
                              </p>
                            )}
                            {window.pending_current > 0 && (
                              <p>
                                {t('allocationPendingCurrent', {
                                  pending: window.pending_current,
                                })}
                              </p>
                            )}
                            <p>
                              {t('allocationAdmissionRoom', {
                                room: allocationValue(
                                  window.admission_room,
                                  window.mode,
                                ),
                                unit,
                              })}
                            </p>
                          </div>
                        )}
                      {detail.available &&
                        window.admission === 'risk_limited' && (
                          <p className="text-sm leading-6 text-warning">
                            {t('allocationRiskLimit')}
                          </p>
                        )}
                      <p className="text-xs leading-5 text-muted-foreground">
                        {keyMode
                          ? window.reset_at > 0
                            ? t('allocationRecoversAt', {
                                date: date(window.reset_at),
                              })
                            : t('allocationTrailingWindow')
                          : detail.config.period === 'durations'
                            ? timeZone
                            : detail.config.period === 'day'
                              ? t('allocationDailySchedule', {
                                  time: detail.config.reset_time ?? '00:00',
                                  zone: timeZone,
                                })
                              : t('allocationMonthlySchedule', {
                                  day: detail.config.reset_day ?? 1,
                                  time: detail.config.reset_time ?? '00:00',
                                  zone: timeZone,
                                })}
                        {!keyMode &&
                          ` · ${t('allocationResetAt', { date: date(window.reset_at) })}`}
                        {window.mode === 'amount' &&
                          ` · ${t('allocationActualTokens', { tokens: allocationValue(window.tokens, 'tokens') })}`}
                      </p>
                    </div>
                  )
                })}
                {detail.config.mode === 'windows' && b.pending > 0 && (
                  <p className="text-sm leading-6 text-muted-foreground">
                    {t('allocationWindowPendingHint', { pending: b.pending })}
                  </p>
                )}
                {detail.config.mode !== 'windows' &&
                  b.pending > b.pending_current && (
                    <p className="text-sm leading-6 text-muted-foreground">
                      {t('allocationOlderPending', {
                        older: b.pending - b.pending_current,
                      })}
                    </p>
                  )}
              </section>
            )
          })}
        </div>
      )}
    </div>
  )
}
