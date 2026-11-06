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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  ExternalLink,
  Layers3,
  Radio,
  Plus,
  RefreshCw,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { ProbeScope } from '@/features/channel-status/types'

import {
  deleteChannelProbe,
  getChannelProbes,
  orderChannelProbes,
  runChannelProbe,
} from './api'
import { ProbeDialog } from './components/probe-dialog'
import { ProbeTable } from './components/probe-table'
import type { ChannelProbe } from './types'

export function ChannelProbes() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editor, setEditor] = useState<{ probe: ChannelProbe | null } | null>(
    null
  )
  const [deleting, setDeleting] = useState<ChannelProbe | null>(null)
  const [search, setSearch] = useState('')
  const [scope, setScope] = useState<ProbeScope | 'all'>('all')
  const query = useQuery({
    queryKey: ['channel-probes'],
    queryFn: ({ signal }) => getChannelProbes(signal),
    refetchInterval: 10_000,
    meta: { errorToast: false },
  })
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['channel-probes'] })
    void queryClient.invalidateQueries({ queryKey: ['channel-status'] })
  }
  const remove = useMutation({
    mutationFn: deleteChannelProbe,
    onSuccess: () => {
      setDeleting(null)
      refresh()
      toast.success(t('Probe deleted'))
    },
  })
  const run = useMutation({
    mutationFn: runChannelProbe,
    onSuccess: () => {
      refresh()
      toast.success(t('Probe queued. Results update automatically.'))
    },
  })
  const order = useMutation({
    mutationFn: orderChannelProbes,
    onSuccess: () => {
      refresh()
      toast.success(t('Display order saved'))
    },
  })
  const rows = (query.data ?? []).filter(
    (probe) =>
      (scope === 'all' || (probe.scope || 'upstream') === scope) &&
      `${probe.public_name} ${(probe.models ?? [probe.model]).join(' ')} ${probe.group_name} ${probe.channel_id}`
        .toLowerCase()
        .includes(search.trim().toLowerCase())
  )
  const move = (probe: ChannelProbe, direction: -1 | 1) => {
    const target =
      rows[rows.findIndex((row) => row.id === probe.id) + direction]
    if (!target || order.isPending) return
    const ids = (query.data ?? []).map((row) => row.id)
    const from = ids.indexOf(probe.id)
    const to = ids.indexOf(target.id)
    ;[ids[from], ids[to]] = [ids[to], ids[from]]
    order.mutate(ids)
  }

  return (
    <>
      <SectionPageLayout stackActionsOnMobile>
        <SectionPageLayout.Title>{t('Channel Probes')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button variant='outline' render={<Link to='/model-status' />}>
            <ExternalLink className='size-4' />
            {t('Status page')}
          </Button>
          <Button onClick={() => setEditor({ probe: null })}>
            <Plus className='size-4' />
            {t('Add probe')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='space-y-4'>
            <Tabs
              value={scope}
              onValueChange={(value) => setScope(value as ProbeScope | 'all')}
            >
              <TabsList className='h-10' aria-label={t('Probe type')}>
                <TabsTrigger value='all' className='px-4'>
                  {t('All probes')}
                </TabsTrigger>
                <TabsTrigger value='group' className='px-4'>
                  <Layers3 className='size-4' />
                  {t('Group probes')}
                </TabsTrigger>
                <TabsTrigger value='upstream' className='px-4'>
                  <Radio className='size-4' />
                  {t('Upstream probes')}
                </TabsTrigger>
              </TabsList>
            </Tabs>
            <div className='flex items-center justify-between gap-3'>
              <Input
                className='max-w-sm'
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('Search name, model, group or channel ID')}
                aria-label={t('Search probes')}
              />
              <Button
                variant='outline'
                onClick={() => void query.refetch()}
                disabled={query.isFetching}
              >
                <RefreshCw className='size-4' />
                {t('Refresh')}
              </Button>
            </div>
            {query.isPending && <LoadingState />}
            {query.isError && (
              <ErrorState
                title={t('Failed to load channel probes')}
                onRetry={() => void query.refetch()}
              />
            )}
            {!query.isPending && !query.isError && rows.length === 0 && (
              <EmptyState
                icon={Activity}
                bordered
                title={
                  search ? t('No matching probes') : t('No channel probes yet')
                }
                action={
                  search ? (
                    <Button variant='outline' onClick={() => setSearch('')}>
                      {t('Clear search')}
                    </Button>
                  ) : (
                    <Button onClick={() => setEditor({ probe: null })}>
                      <Plus className='size-4' />
                      {t('Add probe')}
                    </Button>
                  )
                }
              />
            )}
            {!query.isPending && !query.isError && rows.length > 0 && (
              <ProbeTable
                probes={rows}
                busy={remove.isPending || run.isPending || order.isPending}
                runningId={run.isPending ? run.variables : undefined}
                onEdit={(probe) => setEditor({ probe })}
                onDelete={setDeleting}
                onRun={(probe) => run.mutate(probe.id)}
                onMove={move}
              />
            )}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
      {editor && (
        <ProbeDialog
          probe={editor.probe}
          defaultScope={scope === 'upstream' ? 'upstream' : 'group'}
          nextSortOrder={
            Math.max(
              0,
              ...(query.data ?? []).map((probe) => probe.sort_order ?? 0)
            ) + 1
          }
          onClose={() => setEditor(null)}
        />
      )}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => {
          if (!open && !remove.isPending) setDeleting(null)
        }}
        title={t('Delete channel probe?')}
        desc={t(
          'Delete this probe and its recent history? This does not delete the channel.'
        )}
        destructive
        confirmText={t('Delete')}
        isLoading={remove.isPending}
        handleConfirm={() => {
          if (deleting) remove.mutate(deleting.id)
        }}
      />
    </>
  )
}
