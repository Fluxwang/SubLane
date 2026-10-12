import { useQuery } from '@tanstack/react-query'
import { Outlet, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { ClientVersion } from '@/components/ClientVersion'
import { TimeZoneSettings } from '@/components/TimeZoneSettings'
import { WorkspaceAlerts } from '@/components/WorkspaceAlerts'
import { authOptions } from '@/lib/auth'

export function System() {
  const { t } = useTranslation()
  const workspaceOnly = useRouterState({
    select: (state) =>
      state.location.pathname.replace(/\/+$/, '') === '/admin/settings/alerts',
  })
  return (
    <div className="max-w-3xl">
      <h1 className="page-title">{t('systemSettings')}</h1>
      <p className="page-description">
        {t(
          workspaceOnly
            ? 'workspaceSettingsDescription'
            : 'systemSettingsDescription',
        )}
      </p>
      <Outlet />
    </div>
  )
}

export function AlertSettings() {
  const { data } = useQuery(authOptions())
  return data?.user?.role === 'admin' ? (
    <div className="mt-8">
      <WorkspaceAlerts key={data.user.id} userID={data.user.id} />
    </div>
  ) : null
}

export function CodexSettings() {
  const { data } = useQuery(authOptions())
  return data?.user?.role === 'admin' ? (
    <ClientVersion key={data.user.id} userID={data.user.id} />
  ) : null
}

export function GrokSettings() {
  const { data } = useQuery(authOptions())
  return data?.user?.role === 'admin' && data.user.id === 1 ? (
    <ClientVersion key={data.user.id} userID={data.user.id} provider="grok" />
  ) : null
}

export function TimeZonePage() {
  const { data } = useQuery(authOptions())
  return data?.user?.role === 'admin' && data.user.id === 1 ? (
    <TimeZoneSettings key={data.user.id} userID={data.user.id} />
  ) : null
}
