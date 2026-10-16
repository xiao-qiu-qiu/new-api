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
import { InformationCircleIcon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import i18next from 'i18next'
import type { BaseSyntheticEvent } from 'react'
import { useWatch, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import { FormDirtyIndicator } from '../components/form-dirty-indicator'
import { FormNavigationGuard } from '../components/form-navigation-guard'
import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useSettingsForm } from '../hooks/use-settings-form'
import { useUpdateOption } from '../hooks/use-update-option'

const streamBillingSchema = z.object({
  quota_setting: z.object({
    stream_disconnect_mode: z.enum(['cancel', 'drain']),
    stream_drain_timeout_seconds: z.coerce
      .number({
        error: () => i18next.t('Enter a whole number from 1 to 3600'),
      })
      .int({ error: () => i18next.t('Enter a whole number from 1 to 3600') })
      .min(1, { error: () => i18next.t('Enter a whole number from 1 to 3600') })
      .max(3600, {
        error: () => i18next.t('Enter a whole number from 1 to 3600'),
      }),
  }),
})

type StreamBillingValues = z.infer<typeof streamBillingSchema>

export function StreamBillingSection(props: {
  defaultValues: StreamBillingValues
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const { form, handleSubmit, isDirty, isSubmitting } =
    useSettingsForm<StreamBillingValues>({
      resolver: zodResolver(
        streamBillingSchema
      ) as Resolver<StreamBillingValues>,
      defaultValues: props.defaultValues,
      onSubmit: async (_data, changedFields) => {
        // Apply a changed timeout before enabling collection for new requests.
        for (const key of [
          'quota_setting.stream_drain_timeout_seconds',
          'quota_setting.stream_disconnect_mode',
        ]) {
          const value = changedFields[key]
          if (value !== undefined) {
            await updateOption.mutateAsync({
              key,
              value: value as string | number,
            })
          }
        }
      },
    })
  const mode = useWatch({
    control: form.control,
    name: 'quota_setting.stream_disconnect_mode',
  })
  const isSaving = updateOption.isPending || isSubmitting
  const saveSettings = (event?: BaseSyntheticEvent) => {
    void handleSubmit(event).catch(() => {
      // useUpdateOption displays the error; retain dirty values for retry.
    })
  }
  const modeDescription = t(
    'For HTTP streams, disconnect immediately and estimate missing usage, or keep reading upstream usage before billing. Collection includes tokens generated after the client disconnects; missing usage is estimated on timeout.'
  )

  return (
    <SettingsSection title={t('Stream Billing')}>
      <FormNavigationGuard when={isDirty} />
      <Form {...form}>
        <SettingsForm onSubmit={saveSettings}>
          <SettingsPageFormActions onSave={saveSettings} isSaving={isSaving} />
          <FormDirtyIndicator isDirty={isDirty} />
          <FormField
            control={form.control}
            name='quota_setting.stream_disconnect_mode'
            render={({ field }) => (
              <FormItem>
                <div className='flex items-center gap-1'>
                  <FormLabel>{t('Billing after disconnect')}</FormLabel>
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-xs'
                          aria-label={t('Billing mode details')}
                        />
                      }
                    >
                      <HugeiconsIcon
                        icon={InformationCircleIcon}
                        className='size-4'
                        aria-hidden='true'
                      />
                    </TooltipTrigger>
                    <TooltipContent>{modeDescription}</TooltipContent>
                  </Tooltip>
                </div>
                <Select
                  items={[
                    { value: 'cancel', label: t('Disconnect immediately') },
                    { value: 'drain', label: t('Collect upstream usage') },
                  ]}
                  value={field.value}
                  onValueChange={field.onChange}
                  disabled={isSaving}
                >
                  <FormControl>
                    <SelectTrigger
                      className='w-full'
                      onBlur={field.onBlur}
                      ref={field.ref}
                    >
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent alignItemWithTrigger={false}>
                    <SelectGroup>
                      <SelectItem value='cancel'>
                        {t('Disconnect immediately')}
                      </SelectItem>
                      <SelectItem value='drain'>
                        {t('Collect upstream usage')}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <FormDescription className='sr-only'>
                  {modeDescription}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='quota_setting.stream_drain_timeout_seconds'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Collection timeout (seconds)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    max={3600}
                    step={1}
                    {...field}
                    disabled={mode !== 'drain' || isSaving}
                    title={t(
                      'Maximum time to collect usage after disconnection. Defaults to 300 seconds.'
                    )}
                  />
                </FormControl>
                <FormDescription className='sr-only'>
                  {t(
                    'Maximum time to collect usage after disconnection. Defaults to 300 seconds.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
