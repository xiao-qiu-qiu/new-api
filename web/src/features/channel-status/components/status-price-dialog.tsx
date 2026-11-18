import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { Calculator } from 'lucide-react'
import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toIntlLocale } from '@/i18n/languages'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'

import { estimateChannelPrice } from '../api'
import type { PriceEstimateInput } from '../types'

const maxEstimateTokens = 10_000_000
const tokenCount = z.number().int().min(0).max(maxEstimateTokens)
const estimateSchema = z
  .object({
    input_tokens: tokenCount,
    output_tokens: tokenCount,
    cache_read_tokens: tokenCount,
    service_tier: z.enum(['default', 'flex', 'priority', 'ultrafast']),
  })
  .refine((value) => value.cache_read_tokens <= value.input_tokens, {
    path: ['cache_read_tokens'],
    message: 'Cached tokens cannot exceed input tokens',
  })

export function StatusPriceDialog(props: { probeId: number; model: string }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const formId = useId()
  const form = useForm<PriceEstimateInput>({
    resolver: zodResolver(estimateSchema),
    defaultValues: {
      input_tokens: 10000,
      output_tokens: 500,
      cache_read_tokens: 0,
      service_tier: 'default',
    },
  })
  const estimate = useMutation({
    mutationFn: (usage: PriceEstimateInput) =>
      estimateChannelPrice(props.probeId, props.model, usage),
    meta: { errorToast: false },
  })
  const tokenFields = [
    { name: 'input_tokens', label: t('Input tokens (including cache)') },
    { name: 'cache_read_tokens', label: t('Cached tokens') },
    { name: 'output_tokens', label: t('Output tokens') },
  ] as const

  return (
    <Dialog
      title={t('Estimate request cost')}
      description={props.model}
      contentClassName='sm:max-w-md'
      onOpenChange={(open) => {
        if (!open) {
          estimate.reset()
          form.reset()
        }
      }}
      trigger={
        <Button
          type='button'
          variant='ghost'
          size='xs'
          className='text-muted-foreground gap-1.5 px-2'
        >
          <Calculator className='size-3.5' />
          {t('Estimate request cost')}
        </Button>
      }
      footer={
        <Button type='submit' form={formId} disabled={estimate.isPending}>
          {estimate.isPending
            ? t('Calculating...')
            : t('Estimate request cost')}
        </Button>
      }
    >
      <Form {...form}>
        <form
          id={formId}
          onSubmit={form.handleSubmit((values) => estimate.mutate(values))}
          className='space-y-4'
        >
          <fieldset
            disabled={estimate.isPending}
            className='grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2'
          >
            {tokenFields.map((item) => (
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
                        min={0}
                        max={maxEstimateTokens}
                        step={1}
                        required
                        value={Number.isNaN(field.value) ? '' : field.value}
                        onChange={(event) => {
                          estimate.reset()
                          field.onChange(event.target.valueAsNumber)
                        }}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            ))}
            <FormField
              control={form.control}
              name='service_tier'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Service Tier')}</FormLabel>
                  <Select
                    value={field.value}
                    disabled={estimate.isPending}
                    onValueChange={(tier) => {
                      if (tier === null) return
                      estimate.reset()
                      field.onChange(tier)
                    }}
                  >
                    <FormControl>
                      <SelectTrigger className='w-full'>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      <SelectGroup>
                        <SelectItem value='default'>{t('Standard')}</SelectItem>
                        <SelectItem value='flex'>{t('Flex tier')}</SelectItem>
                        <SelectItem value='priority'>
                          {t('Priority')}
                        </SelectItem>
                        <SelectItem value='ultrafast'>
                          {t('Ultrafast tier')}
                        </SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )}
            />
          </fieldset>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Current price for a text request without tool calls or cache writes.'
            )}
          </p>
          {estimate.isError && (
            <p role='alert' className='text-destructive text-sm'>
              {t(
                'This request cannot be estimated with the current pricing rules.'
              )}
            </p>
          )}
          {estimate.data && (
            <output className='bg-muted/65 flex flex-wrap items-center justify-between gap-2 rounded-lg px-3 py-2'>
              <span className='text-muted-foreground text-sm'>
                {t('Estimated request cost')}
              </span>
              <span className='font-semibold tabular-nums'>
                ≈
                {formatBillingCurrencyFromUSD(estimate.data.usd, {
                  locale,
                  abbreviate: false,
                })}
              </span>
            </output>
          )}
        </form>
      </Form>
    </Dialog>
  )
}
