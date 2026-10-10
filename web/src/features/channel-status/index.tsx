/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useQuery } from '@tanstack/react-query'
import { Activity, Layers3, Radio, RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { PublicLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { PageTransition } from '@/components/page-transition'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

import { getChannelStatus } from './api'
import { StatusCard } from './components/status-card'
import { StatusOverview } from './components/status-overview'
import { statusColors } from './lib/status'
import type { ProbeScope, StatusHours } from './types'

export function ChannelStatus() {
  const { t } = useTranslation()
  const [hours, setHours] = useState<StatusHours>(1)
  const [scope, setScope] = useState<ProbeScope | 'all'>('all')
  const query = useQuery({
    queryKey: ['channel-status', hours],
    queryFn: ({ signal }) => getChannelStatus(hours, signal),
    refetchInterval: 30_000,
    meta: { errorToast: false },
  })
  const probes = (query.data ?? []).filter(
    (probe) => scope === 'all' || probe.scope === scope
  )
  const occurrences = new Map<string, number>()
  const legend = [
    { status: 'green' as const, label: t('Responsive') },
    { status: 'yellow' as const, label: t('Elevated latency') },
    { status: 'red' as const, label: t('Slow or unavailable') },
    { status: 'timeout' as const, label: t('Timed out') },
    { status: 'unknown' as const, label: t('No samples') },
  ]

  return (
    <PublicLayout showMainContainer={false}>
      <main className='relative'>
        <div
          aria-hidden
          className='model-status-glow pointer-events-none absolute inset-x-0 top-0 h-[650px]'
        />
        <PageTransition className='relative mx-auto w-full max-w-[1280px] space-y-5 px-4 pt-20 pb-10 sm:px-6 sm:pt-[84px] xl:px-8'>
          <header className='flex items-end justify-between gap-4 pb-1'>
            <div>
              <div className='mb-2 inline-flex items-center gap-2 rounded-full border border-teal-600/15 bg-teal-500/5 px-3 py-1 text-[11px] font-medium tracking-wide text-teal-700 dark:text-teal-300'>
                <span className='size-1.5 rounded-full bg-teal-500' />
                CodeQ Real-time Status
              </div>
              <h1 className='text-3xl font-semibold tracking-tight sm:text-4xl'>
                {t('Model Status')}
              </h1>
              <p className='text-muted-foreground mt-2 text-sm sm:text-base'>
                {t('Availability and first-token latency at a glance.')}
              </p>
            </div>
            <Button
              variant='outline'
              className='bg-background/60 shrink-0'
              onClick={() => void query.refetch()}
              disabled={query.isFetching}
              aria-label={t('Refresh model status')}
            >
              <RefreshCw
                className={cn(
                  'size-4',
                  query.isFetching && 'motion-safe:animate-spin'
                )}
              />
              <span className='hidden sm:inline'>{t('Refresh')}</span>
            </Button>
          </header>
          {query.data && (
            <StatusOverview
              probes={probes}
              hours={hours}
              updatedAt={query.dataUpdatedAt}
              fetching={query.isFetching}
            />
          )}
          <div className='flex flex-wrap items-center justify-between gap-x-6 gap-y-4'>
            <div className='flex items-center gap-3'>
              <span className='text-muted-foreground hidden text-xs sm:inline'>
                {t('Time range')}
              </span>
              <Tabs
                value={String(hours)}
                onValueChange={(value) =>
                  setHours(Number(value) as StatusHours)
                }
              >
                <TabsList
                  className='bg-muted/70 h-10 rounded-full p-1'
                  aria-label={t('Time range')}
                >
                  {([1, 6, 12, 24] as const).map((value) => (
                    <TabsTrigger
                      key={value}
                      value={String(value)}
                      className='rounded-full px-4 text-xs'
                    >
                      {t('{{hours}} h', { hours: value })}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
            </div>
            <div className='text-muted-foreground flex flex-wrap gap-x-3 gap-y-2 text-[11px]'>
              {legend.map((item) => (
                <span
                  key={item.status}
                  className='inline-flex items-center gap-1.5'
                >
                  <span
                    className={cn(
                      'size-1.5 rounded-full',
                      statusColors[item.status]
                    )}
                  />
                  {item.label}
                </span>
              ))}
            </div>
          </div>
          <div className='flex items-center gap-4 border-b pb-3'>
            <Tabs
              value={scope}
              onValueChange={(value) => setScope(value as ProbeScope | 'all')}
            >
              <TabsList
                variant='line'
                aria-label={t('Probe type')}
                className='gap-3'
              >
                <TabsTrigger value='all' className='px-1'>
                  {t('All models')}
                </TabsTrigger>
                <TabsTrigger value='group' className='px-1'>
                  <Layers3 className='size-3.5' />
                  {t('Group probes')}
                </TabsTrigger>
                <TabsTrigger value='upstream' className='px-1'>
                  <Radio className='size-3.5' />
                  {t('Upstream probes')}
                </TabsTrigger>
              </TabsList>
            </Tabs>
            <span className='text-muted-foreground ml-auto text-xs tabular-nums'>
              {t('{{count}} models', { count: probes.length })}
            </span>
          </div>
          {query.isPending && (
            <LoadingState message={t('Loading model status...')} />
          )}
          {query.isError && (
            <ErrorState
              title={t('Model status is temporarily unavailable')}
              description={t('Please retry to get the latest measurements.')}
              onRetry={() => void query.refetch()}
            />
          )}
          {!query.isPending && !query.isError && probes.length === 0 && (
            <EmptyState
              icon={Activity}
              bordered
              title={t('No public models yet')}
              description={t('Published model probes will appear here.')}
            />
          )}
          {!query.isPending && !query.isError && probes.length > 0 && (
            <div className='space-y-2.5'>
              {probes.map((probe) => {
                const identity = JSON.stringify([
                  probe.scope,
                  probe.public_name,
                  probe.model,
                ])
                const occurrence = occurrences.get(identity) ?? 0
                occurrences.set(identity, occurrence + 1)
                return (
                  <StatusCard key={`${identity}-${occurrence}`} probe={probe} />
                )
              })}
            </div>
          )}
          <div className='text-muted-foreground flex items-center justify-center gap-2 pt-2 text-[11px]'>
            <span className='size-1.5 rounded-full bg-teal-500' />
            {t('Refreshes automatically every 30 seconds')}
          </div>
        </PageTransition>
      </main>
    </PublicLayout>
  )
}
