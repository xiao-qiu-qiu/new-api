import { useQuery } from '@tanstack/react-query'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Accordion } from '@/components/ui/accordion'
import { Combobox } from '@/components/ui/combobox'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { getProbeChannelOptions } from '@/features/channel-probes/api'

import { SettingsAccordion } from '../components/settings-accordion'
import { SettingsCard } from '../components/settings-card'
import {
  SettingsFormGrid,
  SettingsSwitchField,
} from '../components/settings-form-layout'
import { safeNumberFieldProps } from '../utils/numeric-field'
import type { RoutingPolicyFormValues } from './routing-form'

export function FailoverSection() {
  const { t } = useTranslation()
  const form = useFormContext<RoutingPolicyFormValues>()
  const enabled = useWatch({
    control: form.control,
    name: 'channel_failover_setting.enabled',
  })
  const channels = useQuery({
    queryKey: ['channel-probes', 'channel-options'],
    queryFn: getProbeChannelOptions,
    staleTime: 60_000,
    enabled,
    meta: { errorToast: false },
  })
  const options = (channels.data ?? []).map((channel) => ({
    value: String(channel.id),
    label: `${channel.name} (#${channel.id})`,
  }))

  return (
    <SettingsCard title={t('Primary and backup routing')}>
      <FormField
        control={form.control}
        name='channel_failover_setting.enabled'
        render={({ field }) => (
          <SettingsSwitchField
            controlId='channel_failover_setting.enabled'
            checked={field.value}
            onCheckedChange={field.onChange}
            label={t('Automatic failover and recovery')}
            description={t(
              'Use the backup when Sub2API has no available accounts, and return when it recovers.'
            )}
          />
        )}
      />
      {enabled && channels.isError ? (
        <ErrorState
          title={t('Failed to load channels')}
          onRetry={() => void channels.refetch()}
          className='min-h-0 py-4'
        />
      ) : null}
      <SettingsFormGrid className='mt-4'>
        {(
          [
            {
              name: 'channel_failover_setting.primary_channel_id',
              label: t('Primary channel (Sub2API)'),
            },
            {
              name: 'channel_failover_setting.fallback_channel_id',
              label: t('Backup channel'),
            },
          ] as const
        ).map((item) => (
          <FormField
            key={item.name}
            control={form.control}
            name={item.name}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{item.label}</FormLabel>
                <FormControl>
                  <Combobox
                    options={
                      field.value > 0 &&
                      !options.some(
                        (option) => option.value === String(field.value)
                      )
                        ? [
                            {
                              value: String(field.value),
                              label: t('Channel #{{id}}', { id: field.value }),
                            },
                            ...options,
                          ]
                        : options
                    }
                    value={field.value > 0 ? String(field.value) : null}
                    onValueChange={(value) =>
                      field.onChange(Number(value || 0))
                    }
                    onBlur={field.onBlur}
                    ref={field.ref}
                    disabled={
                      !enabled || channels.isPending || channels.isError
                    }
                    placeholder={
                      enabled && channels.isPending
                        ? t('Loading channels...')
                        : t('Search channel name or ID')
                    }
                    emptyText={t('No channels found')}
                    aria-label={item.label}
                    className='w-full'
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        ))}
        <FormField
          control={form.control}
          name='channel_failover_setting.model_regex'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Model patterns (one per line)')}</FormLabel>
              <FormControl>
                <Textarea
                  {...field}
                  disabled={!enabled}
                  placeholder={t('Leave empty to include all supported models')}
                  className='font-mono'
                  rows={2}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      </SettingsFormGrid>
      <Accordion className='mt-4'>
        <SettingsAccordion
          value='availability-checks'
          title={t('Availability checks')}
          keepMounted
        >
          <SettingsFormGrid>
            {(
              [
                {
                  name: 'channel_failover_setting.poll_interval_seconds',
                  label: t('Check interval (seconds)'),
                  max: 60,
                },
                {
                  name: 'channel_failover_setting.recovery_successes',
                  label: t('Consecutive recovery confirmations'),
                  max: 10,
                },
              ] as const
            ).map((item) => (
              <FormField
                key={item.name}
                control={form.control}
                name={item.name}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{item.label}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={item.max}
                        step={1}
                        disabled={!enabled}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            ))}
          </SettingsFormGrid>
        </SettingsAccordion>
      </Accordion>
    </SettingsCard>
  )
}
