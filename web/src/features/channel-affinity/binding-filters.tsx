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

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

export interface BindingFilterValues {
  user_id: string
  token_id: string
  model: string
}

interface Props {
  disabled: boolean
  onApply: (values: BindingFilterValues) => void
}

export function BindingFilters(props: Props) {
  const { t } = useTranslation()
  const id = z
    .string()
    .trim()
    .refine(
      (value) =>
        value === '' ||
        (/^[1-9]\d*$/.test(value) && Number.isSafeInteger(Number(value))),
      t('Enter a positive ID')
    )
  const form = useForm<BindingFilterValues>({
    resolver: zodResolver(
      z.object({ user_id: id, token_id: id, model: z.string().trim() })
    ),
    defaultValues: { user_id: '', token_id: '', model: '' },
  })

  return (
    <form
      className='grid gap-3 sm:grid-cols-[1fr_1fr_1.5fr_auto]'
      onSubmit={form.handleSubmit(props.onApply)}
    >
      <div className='grid content-start gap-1.5'>
        <Label htmlFor='affinity-user'>{t('User ID')}</Label>
        <Input
          id='affinity-user'
          inputMode='numeric'
          {...form.register('user_id')}
          disabled={props.disabled}
          aria-invalid={!!form.formState.errors.user_id}
          aria-describedby={
            form.formState.errors.user_id ? 'affinity-user-error' : undefined
          }
        />
        {form.formState.errors.user_id && (
          <p id='affinity-user-error' className='text-destructive text-xs'>
            {form.formState.errors.user_id.message}
          </p>
        )}
      </div>
      <div className='grid content-start gap-1.5'>
        <Label htmlFor='affinity-token'>{t('Token ID')}</Label>
        <Input
          id='affinity-token'
          inputMode='numeric'
          {...form.register('token_id')}
          disabled={props.disabled}
          aria-invalid={!!form.formState.errors.token_id}
          aria-describedby={
            form.formState.errors.token_id ? 'affinity-token-error' : undefined
          }
        />
        {form.formState.errors.token_id && (
          <p id='affinity-token-error' className='text-destructive text-xs'>
            {form.formState.errors.token_id.message}
          </p>
        )}
      </div>
      <div className='grid content-start gap-1.5'>
        <Label htmlFor='affinity-model'>{t('Model')}</Label>
        <Input
          id='affinity-model'
          {...form.register('model')}
          disabled={props.disabled}
        />
      </div>
      <Button
        type='submit'
        variant='outline'
        className='mt-auto'
        disabled={props.disabled}
      >
        {t('Filter')}
      </Button>
    </form>
  )
}
