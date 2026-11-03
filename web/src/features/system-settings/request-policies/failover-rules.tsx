import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react'
import { useFieldArray, useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Textarea } from '@/components/ui/textarea'

import type { RoutingPolicyFormValues } from './routing-form'

export function FailoverRules(props: {
  enabled: boolean
  channelsDisabled: boolean
  options: { value: string; label: string }[]
}) {
  const { t } = useTranslation()
  const form = useFormContext<RoutingPolicyFormValues>()
  const rules = useFieldArray({
    control: form.control,
    name: 'channel_failover_setting.fallback_rules',
  })
  return (
    <div className='space-y-3'>
      {rules.fields.map((rule, index) => (
        <div
          key={rule.id}
          className='grid min-w-0 gap-3 rounded-lg border p-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]'
        >
          <FormField
            control={form.control}
            name={`channel_failover_setting.fallback_rules.${index}.model_regex`}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Model patterns (one per line)')}</FormLabel>
                <FormControl>
                  <Textarea
                    {...field}
                    rows={2}
                    disabled={!props.enabled}
                    className='font-mono'
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name={`channel_failover_setting.fallback_rules.${index}.channel_id`}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Backup channel')}</FormLabel>
                <FormControl>
                  <Combobox
                    options={
                      field.value > 0 &&
                      !props.options.some(
                        (option) => option.value === String(field.value)
                      )
                        ? [
                            {
                              value: String(field.value),
                              label: t('Channel #{{id}}', { id: field.value }),
                            },
                            ...props.options,
                          ]
                        : props.options
                    }
                    value={field.value > 0 ? String(field.value) : null}
                    onValueChange={(value) =>
                      field.onChange(Number(value || 0))
                    }
                    onBlur={field.onBlur}
                    ref={field.ref}
                    disabled={!props.enabled || props.channelsDisabled}
                    placeholder={t('Search channel name or ID')}
                    emptyText={t('No channels found')}
                    aria-label={t('Backup channel')}
                    className='w-full'
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <div className='flex items-start gap-1 sm:pt-6'>
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              aria-label={t('Move up')}
              title={t('Move up')}
              disabled={!props.enabled || index === 0}
              onClick={() => rules.move(index, index - 1)}
            >
              <ArrowUp />
            </Button>
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              aria-label={t('Move down')}
              title={t('Move down')}
              disabled={!props.enabled || index === rules.fields.length - 1}
              onClick={() => rules.move(index, index + 1)}
            >
              <ArrowDown />
            </Button>
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              aria-label={t('Delete')}
              title={t('Delete')}
              disabled={!props.enabled}
              onClick={() => rules.remove(index)}
            >
              <Trash2 />
            </Button>
          </div>
        </div>
      ))}
      <Button
        type='button'
        variant='outline'
        size='sm'
        disabled={!props.enabled || rules.fields.length >= 32}
        onClick={() => rules.append({ model_regex: '', channel_id: 0 })}
      >
        <Plus />
        {t('Add model backup')}
      </Button>
    </div>
  )
}
