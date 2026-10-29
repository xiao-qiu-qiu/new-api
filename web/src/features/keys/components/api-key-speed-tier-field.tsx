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
import { ArrowRightLeft, Gauge, Rocket, Zap } from 'lucide-react'
import { useId } from 'react'
import type { Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  SideDrawerSection,
  SideDrawerSectionHeader,
} from '@/components/drawer-layout'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { cn } from '@/lib/utils'

import type { ApiKeyFormValues } from '../lib'

export function ApiKeySpeedTierField(props: {
  control: Control<ApiKeyFormValues>
}) {
  const { t } = useTranslation()
  const id = useId()
  const options = [
    {
      value: 'follow',
      label: t('Follow request'),
      description: t('Use the client-selected tier'),
      icon: ArrowRightLeft,
    },
    {
      value: 'priority',
      label: t('Lock Fast'),
      description: t('Always request the Fast tier'),
      icon: Zap,
    },
    {
      value: 'ultrafast',
      label: t('Lock UF'),
      description: t('Always request the Ultrafast tier'),
      icon: Rocket,
    },
  ]

  return (
    <SideDrawerSection>
      <SideDrawerSectionHeader
        title={t('GPT speed tier')}
        description={t('Applies only to GPT text models using this key')}
        icon={<Gauge className='size-4' aria-hidden='true' />}
        iconTone='info'
      />
      <FormField
        control={props.control}
        name='openai_tier_policy'
        render={({ field }) => (
          <FormItem>
            <FormLabel className='sr-only'>{t('GPT speed tier')}</FormLabel>
            <FormControl>
              <RadioGroup
                aria-label={t('GPT speed tier')}
                value={field.value}
                onValueChange={field.onChange}
                onBlur={field.onBlur}
                className='gap-2 sm:grid-cols-3'
              >
                {options.map((option) => (
                  <Label
                    key={option.value}
                    htmlFor={`${id}-${option.value}`}
                    className={cn(
                      'border-border/70 hover:bg-muted/40 grid min-h-16 cursor-pointer grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2 gap-y-2 rounded-lg border p-3 font-normal transition-colors sm:min-h-28 sm:grid-cols-[1fr_auto]',
                      field.value === option.value &&
                        'border-primary/60 bg-primary/5 hover:bg-primary/10'
                    )}
                  >
                    <option.icon
                      className={cn(
                        'text-muted-foreground size-4 shrink-0',
                        field.value === option.value && 'text-primary'
                      )}
                      aria-hidden='true'
                    />
                    <span className='flex min-w-0 flex-col gap-1.5 sm:col-span-2 sm:row-start-2'>
                      <span
                        id={`${id}-${option.value}-label`}
                        className='font-medium'
                      >
                        {option.label}
                      </span>
                      <span
                        id={`${id}-${option.value}-description`}
                        className='text-muted-foreground text-xs leading-4'
                      >
                        {option.description}
                      </span>
                    </span>
                    <RadioGroupItem
                      id={`${id}-${option.value}`}
                      value={option.value}
                      aria-labelledby={`${id}-${option.value}-label`}
                      aria-describedby={`${id}-${option.value}-description`}
                      className='ml-auto sm:col-start-2 sm:row-start-1'
                    />
                  </Label>
                ))}
              </RadioGroup>
            </FormControl>
            <FormDescription className='text-xs leading-5'>
              {t(
                'Locked tiers override the client selection. Charges follow the effective tier and model pricing.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
    </SideDrawerSection>
  )
}
