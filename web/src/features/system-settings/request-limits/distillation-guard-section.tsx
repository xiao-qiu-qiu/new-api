import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

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
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchField,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const limits = {
  min_output_tokens: [1, 10_000_000],
  max_cached_tokens: [0, 10_000_000],
  consecutive_requests: [1, 1000],
  window_minutes: [1, 1440],
  block_minutes: [1, 1440],
} as const

function integerRange([min, max]: readonly [number, number]) {
  return z.number().int().min(min).max(max)
}

const policySchema = z.object({
  enabled: z.boolean(),
  min_output_tokens: integerRange(limits.min_output_tokens),
  max_cached_tokens: integerRange(limits.max_cached_tokens),
  consecutive_requests: integerRange(limits.consecutive_requests),
  window_minutes: integerRange(limits.window_minutes),
  block_minutes: integerRange(limits.block_minutes),
})

export function DistillationGuardSection({ policy }: { policy: string }) {
  const { t } = useTranslation()
  let parsed: unknown
  try {
    parsed = JSON.parse(policy)
  } catch {
    parsed = null
  }
  const result = policySchema.safeParse(parsed)
  if (!result.success) {
    return <p role='alert'>{t('Invalid distillation protection settings')}</p>
  }
  return <DistillationGuardForm initial={result.data} />
}

function DistillationGuardForm({
  initial,
}: {
  initial: z.infer<typeof policySchema>
}) {
  const { t } = useTranslation()
  const update = useUpdateOption()
  const form = useForm({
    resolver: zodResolver(policySchema, {
      error: (issue) => {
        const name = issue.path?.[0]
        if (typeof name !== 'string' || !Object.hasOwn(limits, name)) {
          return undefined
        }
        const [min, max] = limits[name as keyof typeof limits]
        return t('Enter a whole number from {{min}} to {{max}}', { min, max })
      },
    }),
    defaultValues: initial,
    mode: 'onChange',
  })
  const save = form.handleSubmit((values) => {
    update.mutate(
      { key: 'DistillationGuardPolicy', value: JSON.stringify(values) },
      { onSuccess: () => form.reset(values) }
    )
  })
  const fields = [
    ['min_output_tokens', t('Minimum output tokens')],
    ['max_cached_tokens', t('Maximum cached input tokens')],
    ['consecutive_requests', t('Consecutive matching requests')],
    ['window_minutes', t('Observation window (minutes)')],
    ['block_minutes', t('Non-streaming cooldown (minutes)')],
  ] as const
  return (
    <SettingsSection title={t('Distillation protection')}>
      <Form {...form}>
        <SettingsForm onSubmit={save}>
          <SettingsPageFormActions
            onSave={save}
            isSaving={update.isPending}
            isSaveDisabled={!form.formState.isDirty || !form.formState.isValid}
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchField
                controlId='distillation-enabled'
                checked={field.value}
                onCheckedChange={field.onChange}
                label={t('Limit non-streaming text generation')}
                description={t(
                  'Count consecutive matching completions per user. A non-matching completion resets the count. Streaming remains available during cooldown.'
                )}
              />
            )}
          />
          <SettingsFormGrid>
            {fields.map(([name, label]) => (
              <FormField
                key={name}
                control={form.control}
                name={name}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{label}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        step={1}
                        min={limits[name][0]}
                        max={limits[name][1]}
                        {...field}
                        value={Number.isFinite(field.value) ? field.value : ''}
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
          </SettingsFormGrid>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
