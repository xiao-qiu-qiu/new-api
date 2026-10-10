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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Info } from 'lucide-react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import type { ProbeScope } from '@/features/channel-status/types'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { saveChannelProbe } from '../api'
import { defaultProbe, probeSchema } from '../lib/schema'
import type { ChannelProbe, ProbeConfig } from '../types'
import { ProbeTargetFields } from './probe-target-fields'

export function ProbeDialog(props: {
  probe: ChannelProbe | null
  defaultScope?: ProbeScope
  nextSortOrder?: number
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useForm<ProbeConfig>({
    resolver: zodResolver(probeSchema),
    defaultValues: props.probe
      ? {
          ...defaultProbe,
          ...props.probe,
          scope: props.probe.scope || 'upstream',
          models: props.probe.models?.length
            ? props.probe.models
            : [props.probe.model],
        }
      : {
          ...defaultProbe,
          scope: props.defaultScope ?? 'group',
          sort_order: props.nextSortOrder ?? 0,
        },
  })
  const mutation = useMutation({
    mutationFn: (values: ProbeConfig) =>
      saveChannelProbe(values, props.probe?.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['channel-probes'] })
      void queryClient.invalidateQueries({ queryKey: ['channel-status'] })
      toast.success(t('Probe saved'))
      props.onClose()
    },
    onError: (error) => {
      form.setError('root', {
        message: getServerErrorMessage(error, t('Failed to save probe')),
      })
      handleServerError(error, t('Failed to save probe'))
    },
  })
  const numbers = [
    { name: 'sort_order', label: t('Display order'), min: 0, max: 1000000 },
    {
      name: 'interval_seconds',
      label: t('Interval (seconds)'),
      min: 30,
      max: 86400,
    },
    {
      name: 'timeout_seconds',
      label: t('Timeout (seconds)'),
      min: 5,
      max: 300,
    },
    { name: 'green_ms', label: t('Green threshold (ms)'), min: 1, max: 300000 },
    {
      name: 'yellow_ms',
      label: t('Yellow threshold (ms)'),
      min: 1,
      max: 300000,
    },
  ] as const
  const switches = [
    {
      name: 'enabled',
      label: t('Enabled'),
      hint: t('Enable collection for every selected model.'),
    },
    {
      name: 'public',
      label: t('Publish on status page'),
      hint: t('Only the public name, model and measurements are published.'),
    },
    {
      name: 'active_enabled',
      label: t('Active probes'),
      hint: t(
        'Send scheduled streaming requests to measure time to first token. Probes may consume upstream quota.'
      ),
    },
    {
      name: 'passive_enabled',
      label: t('Passive observations'),
      hint: t(
        'Collect measurements from real requests without sending additional traffic.'
      ),
    },
  ] as const

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) props.onClose()
      }}
      title={props.probe ? t('Edit probe') : t('Add probe')}
      footer={
        <>
          <Button
            variant='outline'
            disabled={mutation.isPending}
            onClick={props.onClose}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='channel-probe-form'
            disabled={mutation.isPending}
          >
            {mutation.isPending ? t('Saving...') : t('Save')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id='channel-probe-form'
          onSubmit={form.handleSubmit((values) => {
            form.clearErrors('root')
            mutation.mutate(values)
          })}
          className='space-y-6'
        >
          {form.formState.errors.root?.message && (
            <Alert variant='destructive'>
              <AlertDescription>
                {form.formState.errors.root.message}
              </AlertDescription>
            </Alert>
          )}
          <fieldset disabled={mutation.isPending} className='space-y-6'>
            <div className='grid gap-4 sm:grid-cols-2'>
              <ProbeTargetFields disabled={mutation.isPending} />
              <FormField
                control={form.control}
                name='public_name'
                render={({ field }) => (
                  <FormItem className='sm:col-span-2'>
                    <FormLabel>{t('Public name')}</FormLabel>
                    <FormControl>
                      <Input {...field} maxLength={128} autoComplete='off' />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              {numbers.map((item) => (
                <FormField
                  key={item.name}
                  control={form.control}
                  name={item.name}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{item.label}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='number'
                          min={item.min}
                          max={item.max}
                          step={1}
                          value={Number.isNaN(field.value) ? '' : field.value}
                          onChange={(event) =>
                            field.onChange(event.target.valueAsNumber)
                          }
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}
            </div>
            <div className='divide-y rounded-lg border px-4'>
              {switches.map((item) => (
                <FormField
                  key={item.name}
                  control={form.control}
                  name={item.name}
                  render={({ field }) => (
                    <FormItem className='flex items-center justify-between gap-3 py-3'>
                      <div className='flex items-center gap-1'>
                        <FormLabel>{item.label}</FormLabel>
                        <Tooltip>
                          <TooltipTrigger
                            render={
                              <Button
                                type='button'
                                variant='ghost'
                                size='icon'
                                className='size-7'
                                aria-label={item.label}
                              />
                            }
                          >
                            <Info className='size-3.5' />
                          </TooltipTrigger>
                          <TooltipContent>{item.hint}</TooltipContent>
                        </Tooltip>
                      </div>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}
            </div>
          </fieldset>
        </form>
      </Form>
    </Dialog>
  )
}
