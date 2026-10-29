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
import { useId } from 'react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Checkbox } from '@/components/ui/checkbox'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { supportsOpenAITierPolicy } from '@/lib/openai-service-tier'
import { cn } from '@/lib/utils'

import type { ChannelFormValues } from '../lib/channel-form'
import { parseModelsString } from '../lib/model-mapping-validation'
import type { ChannelServiceTier } from '../types'

export function ChannelServiceTiersSetting(props: { disabled?: boolean }) {
  const { t } = useTranslation()
  const form = useFormContext<ChannelFormValues>()
  const id = useId()
  const [models, modelMapping, paramOverride] = useWatch({
    control: form.control,
    name: ['models', 'model_mapping', 'param_override'],
  })
  let mapping: Record<string, unknown> = {}
  let overrideModel: string | undefined
  try {
    const parsed: unknown = JSON.parse(modelMapping || '{}')
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      mapping = parsed as Record<string, unknown>
    }
  } catch {
    // The model-mapping editor owns validation of an unfinished JSON draft.
  }
  try {
    const parsed: unknown = JSON.parse(paramOverride || '{}')
    if (
      parsed &&
      typeof parsed === 'object' &&
      'model' in parsed &&
      typeof parsed.model === 'string'
    ) {
      overrideModel = parsed.model
    }
  } catch {
    // Keep the setting available while request overrides are being edited.
  }
  const hasGPTModel = parseModelsString(models || '').some((model) => {
    const visited = new Set([model])
    let upstreamModel = model
    while (typeof mapping[upstreamModel] === 'string') {
      const mapped = mapping[upstreamModel] as string
      if (!mapped || visited.has(mapped)) break
      visited.add(mapped)
      upstreamModel = mapped
    }
    return supportsOpenAITierPolicy(overrideModel || upstreamModel)
  })
  if (!hasGPTModel) return null

  const options: { value: ChannelServiceTier; label: string }[] = [
    { value: 'default', label: t('Standard') },
    { value: 'priority', label: t('Fast tier') },
    { value: 'ultrafast', label: t('Ultrafast tier') },
    { value: 'flex', label: t('Flex tier') },
  ]

  return (
    <FormField
      control={form.control}
      name='supported_service_tiers'
      render={({ field }) => {
        const selected = field.value || []
        const limited = selected.length > 0
        return (
          <FormItem className='border-border/70 rounded-lg border'>
            <div className='flex items-center justify-between gap-4 p-3'>
              <div className='space-y-1'>
                <FormLabel>{t('Limit GPT speed tiers')}</FormLabel>
                <FormDescription className='text-xs leading-5'>
                  {t(
                    'Route GPT text requests only to channels that support the requested speed tier.'
                  )}
                </FormDescription>
              </div>
              <FormControl>
                <Switch
                  checked={limited}
                  onCheckedChange={(checked) =>
                    field.onChange(checked ? ['default', 'priority'] : [])
                  }
                  onBlur={field.onBlur}
                  name={field.name}
                  ref={field.ref}
                  disabled={props.disabled}
                />
              </FormControl>
            </div>
            {limited && (
              <div className='space-y-3 border-t p-3'>
                <div
                  role='group'
                  aria-label={t('Supported GPT speed tiers')}
                  className='grid grid-cols-2 gap-2 sm:grid-cols-4'
                >
                  {options.map((option) => {
                    const checked = selected.includes(option.value)
                    return (
                      <Label
                        key={option.value}
                        htmlFor={`${id}-${option.value}`}
                        className={cn(
                          'border-border/70 hover:bg-muted/40 flex min-h-10 cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-xs',
                          checked && 'border-primary/40 bg-primary/5',
                          props.disabled && 'cursor-not-allowed opacity-60'
                        )}
                      >
                        <Checkbox
                          id={`${id}-${option.value}`}
                          checked={checked}
                          disabled={
                            props.disabled || (checked && selected.length === 1)
                          }
                          onCheckedChange={(next) =>
                            field.onChange(
                              next
                                ? [...selected, option.value]
                                : selected.filter(
                                    (value) => value !== option.value
                                  )
                            )
                          }
                          onBlur={field.onBlur}
                        />
                        {option.label}
                      </Label>
                    )
                  })}
                </div>
                <FormDescription className='text-xs leading-5'>
                  {t(
                    'Select at least one tier. Matching tiers take precedence over channel priority and weight, and are forwarded upstream.'
                  )}
                </FormDescription>
              </div>
            )}
            <FormMessage className='px-3 pb-3' />
          </FormItem>
        )
      }}
    />
  )
}
