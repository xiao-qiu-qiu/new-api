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
import { Layers3, Radio, RefreshCw } from 'lucide-react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { fetchUpstreamModels } from '@/features/channels/api'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { getProbeChannelOptions, getProbeGroups } from '../api'
import type { ProbeConfig } from '../types'

export function ProbeTargetFields(props: { disabled: boolean }) {
  const { t } = useTranslation()
  const form = useFormContext<ProbeConfig>()
  const [scope, channelId, groupName, selected] = useWatch({
    control: form.control,
    name: ['scope', 'channel_id', 'group_name', 'models'],
  })
  const channels = useQuery({
    queryKey: ['channel-probes', 'channel-options'],
    queryFn: getProbeChannelOptions,
    staleTime: 60_000,
    enabled: scope === 'upstream',
    meta: { errorToast: false },
  })
  const groups = useQuery({
    queryKey: ['channel-probes', 'group-options'],
    queryFn: ({ signal }) => getProbeGroups(signal),
    enabled: scope === 'group',
    staleTime: 30_000,
    meta: { errorToast: false },
  })
  const upstream = useQuery({
    queryKey: ['channel-probes', 'upstream-models', channelId],
    queryFn: async () =>
      requireServerSuccess(await fetchUpstreamModels(channelId)).data ?? [],
    enabled: scope === 'upstream' && channelId > 0,
    staleTime: 60_000,
    retry: false,
    meta: { errorToast: false },
  })
  const channel = (channels.data ?? []).find((item) => item.id === channelId)
  const channelOptions = (channels.data ?? []).map((item) => ({
    value: String(item.id),
    label: `${item.name} (#${item.id})`,
  }))
  if (channelId > 0 && !channel) {
    channelOptions.unshift({
      value: String(channelId),
      label: t('Channel #{{id}}', { id: channelId }),
    })
  }
  const groupOptions = (groups.data ?? []).map((item) => ({
    value: item.name,
    label: item.name,
  }))
  if (groupName && !groupOptions.some((item) => item.value === groupName)) {
    groupOptions.unshift({ value: groupName, label: groupName })
  }
  const configured = (channel?.models ?? '')
    .split(',')
    .map((model) => model.trim())
    .filter(Boolean)
  const models =
    scope === 'group'
      ? ((groups.data ?? []).find((item) => item.name === groupName)?.models ??
        [])
      : [...new Set([...configured, ...(upstream.data ?? [])])]
  const targetReady = scope === 'group' ? !!groupName : channelId > 0
  const loadingModels =
    scope === 'group' ? groups.isFetching : upstream.isFetching
  const modelError = scope === 'group' ? groups.error : upstream.error
  const resetModels = () => {
    form.setValue('models', [], { shouldDirty: true })
    form.setValue('model', '', { shouldDirty: true })
    form.clearErrors('models')
  }

  return (
    <>
      <FormField
        control={form.control}
        name='scope'
        render={({ field }) => (
          <FormItem className='sm:col-span-2'>
            <FormLabel>{t('Probe type')}</FormLabel>
            <FormControl>
              <Tabs
                value={field.value}
                onValueChange={(value) => {
                  if (value === field.value) return
                  field.onChange(value)
                  resetModels()
                }}
              >
                <TabsList className='h-11 w-full' aria-label={t('Probe type')}>
                  <TabsTrigger value='group' disabled={props.disabled}>
                    <Layers3 className='size-4' />
                    {t('Group probe')}
                  </TabsTrigger>
                  <TabsTrigger value='upstream' disabled={props.disabled}>
                    <Radio className='size-4' />
                    {t('Upstream probe')}
                  </TabsTrigger>
                </TabsList>
              </Tabs>
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      {scope === 'group' ? (
        <FormField
          control={form.control}
          name='group_name'
          render={({ field }) => (
            <FormItem className='sm:col-span-2'>
              <FormLabel>{t('Group')}</FormLabel>
              <FormControl>
                <Combobox
                  options={groupOptions}
                  value={field.value || null}
                  onValueChange={(value) => {
                    if (value && value !== field.value) {
                      field.onChange(value)
                      resetModels()
                    }
                  }}
                  onBlur={field.onBlur}
                  ref={field.ref}
                  disabled={
                    props.disabled || groups.isPending || groups.isError
                  }
                  placeholder={t('Select a group')}
                  aria-label={t('Group')}
                  className='w-full'
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      ) : (
        <FormField
          control={form.control}
          name='channel_id'
          render={({ field }) => (
            <FormItem className='sm:col-span-2'>
              <FormLabel>{t('Channel')}</FormLabel>
              <FormControl>
                <Combobox
                  options={channelOptions}
                  value={field.value > 0 ? String(field.value) : null}
                  onValueChange={(value) => {
                    if (value && Number(value) !== field.value) {
                      field.onChange(Number(value))
                      resetModels()
                    }
                  }}
                  onBlur={field.onBlur}
                  ref={field.ref}
                  disabled={
                    props.disabled || channels.isPending || channels.isError
                  }
                  placeholder={
                    channels.isPending
                      ? t('Loading channels...')
                      : t('Search channel name or ID')
                  }
                  emptyText={t('No channels found')}
                  aria-label={t('Channel')}
                  className='w-full'
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      )}
      {scope === 'upstream' && channels.isError && (
        <Alert variant='destructive' className='sm:col-span-2'>
          <AlertDescription className='flex items-center justify-between gap-2'>
            {t('Failed to load channels')}
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => void channels.refetch()}
            >
              {t('Retry')}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      <FormField
        control={form.control}
        name='models'
        render={({ field }) => (
          <FormItem className='sm:col-span-2'>
            <div className='flex items-center justify-between gap-2'>
              <FormLabel>{t('Models to monitor')}</FormLabel>
              <Button
                type='button'
                size='sm'
                variant='ghost'
                className='h-7 gap-1.5 text-xs'
                disabled={
                  props.disabled ||
                  loadingModels ||
                  (scope === 'upstream' && !targetReady)
                }
                onClick={() => {
                  if (scope === 'group') void groups.refetch()
                  else void upstream.refetch()
                }}
              >
                <RefreshCw
                  className={cn(
                    'size-3',
                    loadingModels && 'motion-safe:animate-spin'
                  )}
                />
                {loadingModels ? t('Fetching models...') : t('Fetch models')}
              </Button>
            </div>
            <FormControl>
              <MultiSelect
                options={models.map((model) => ({
                  value: model,
                  label: model,
                }))}
                selected={field.value}
                onChange={field.onChange}
                allowCreate={scope === 'upstream'}
                disabled={props.disabled || !targetReady}
                maxVisibleChips={6}
                placeholder={
                  targetReady
                    ? t('Search and select multiple models')
                    : t('Select a target first')
                }
              />
            </FormControl>
            <div className='text-muted-foreground flex items-center justify-between text-xs'>
              <span>
                {t('{{selected}} selected · {{count}} available', {
                  selected: selected.length,
                  count: models.length,
                })}
              </span>
              <div className='flex gap-2'>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='h-6 px-1 text-xs'
                  disabled={
                    props.disabled || !models.length || models.length > 32
                  }
                  onClick={() => field.onChange(models)}
                >
                  {t('Select all')}
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='h-6 px-1 text-xs'
                  disabled={props.disabled || !selected.length}
                  onClick={() => field.onChange([])}
                >
                  {t('Clear')}
                </Button>
              </div>
            </div>
            <FormMessage />
            {modelError && (
              <Alert variant='destructive'>
                <AlertDescription>
                  {getServerErrorMessage(
                    modelError,
                    t('Failed to fetch models')
                  )}
                  {scope === 'upstream' && (
                    <span className='mt-1 block'>
                      {t(
                        'Configured models are still available. You can also enter a model manually.'
                      )}
                    </span>
                  )}
                </AlertDescription>
              </Alert>
            )}
          </FormItem>
        )}
      />
    </>
  )
}
