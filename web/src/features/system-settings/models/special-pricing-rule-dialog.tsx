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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'

import {
  specialPricingRuleSchema,
  type SpecialPricingRule,
} from '../billing/group-settings-schema'

const pricingRuleFormSchema = specialPricingRuleSchema.extend({
  price: z
    .string()
    .min(1)
    .transform(Number)
    .pipe(specialPricingRuleSchema.shape.price),
})

type PricingRuleDraft = Omit<SpecialPricingRule, 'price'> & { price: string }

export function SpecialPricingRuleDialog(props: {
  rule?: SpecialPricingRule
  userGroupOptions: string[]
  onClose: () => void
  onApply: (rule: SpecialPricingRule) => boolean
}) {
  const { t } = useTranslation()
  const form = useForm<PricingRuleDraft>({
    resolver: zodResolver(
      pricingRuleFormSchema,
      {
        error: () => t('Invalid parameters'),
      },
      { raw: true }
    ),
    defaultValues: props.rule
      ? { ...props.rule, price: String(props.rule.price) }
      : {
          user_group: '*',
          model: '',
          price: '0',
          billing_unit: 'image',
        },
  })
  const submit = form.handleSubmit((draft) => {
    const rule = { ...draft, price: Number(draft.price) }
    if (
      rule.user_group !== '*' &&
      !props.userGroupOptions.includes(rule.user_group)
    ) {
      form.setError('user_group', { message: t('User group does not exist') })
      return
    }
    if (!props.onApply(rule)) {
      form.setError('model', {
        message: t(
          'A pricing rule already exists for this user group and model pattern.'
        ),
      })
    }
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={props.rule ? t('Edit pricing rule') : t('Add pricing rule')}
      contentClassName='sm:max-w-lg'
      description={t(
        'Specific user groups take priority over the fallback. Exact model names take priority over wildcards.'
      )}
      footer={
        <>
          <Button variant='outline' onClick={props.onClose}>
            {t('Cancel')}
          </Button>
          <Button onClick={submit}>{t('Apply')}</Button>
        </>
      }
    >
      <Form {...form}>
        <form onSubmit={submit} className='space-y-5'>
          <FormField
            control={form.control}
            name='user_group'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('User group')}</FormLabel>
                <FormControl>
                  <Combobox
                    options={[
                      { value: '*', label: t('All users (fallback)') },
                      ...props.userGroupOptions.map((name) => ({
                        value: name,
                        label: name,
                      })),
                    ]}
                    value={field.value}
                    onValueChange={(value) => field.onChange(value ?? '')}
                    className='w-full'
                    aria-label={t('User group')}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='model'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Model pattern')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    placeholder='gpt-image-*'
                    autoComplete='off'
                  />
                </FormControl>
                <p className='text-muted-foreground text-sm'>
                  {t('Use * to match any characters, for example gpt-image-*.')}
                </p>
                <FormMessage />
              </FormItem>
            )}
          />
          <div className='grid gap-4 sm:grid-cols-2'>
            <FormField
              control={form.control}
              name='billing_unit'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Billing unit')}</FormLabel>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <FormControl>
                      <SelectTrigger className='w-full'>
                        <SelectValue>
                          {field.value === 'image'
                            ? t('Per image')
                            : t('Per request')}
                        </SelectValue>
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      <SelectItem value='image'>{t('Per image')}</SelectItem>
                      <SelectItem value='request'>
                        {t('Per request')}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='price'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Unit price (USD)')}</FormLabel>
                  <FormControl>
                    <PricingAmountInput
                      value={field.value}
                      onChange={field.onChange}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
          <p className='text-muted-foreground rounded-md border px-3 py-2 text-sm'>
            {t(
              'A price of 0 is free. These prices apply to all multiplier groups.'
            )}
          </p>
          <Button
            type='submit'
            className='hidden'
            hidden
            aria-hidden='true'
            tabIndex={-1}
          >
            {t('Apply')}
          </Button>
        </form>
      </Form>
    </Dialog>
  )
}
