import type { TFunction } from 'i18next'
import { z } from 'zod'

import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

export function createRoutingPolicySchema(t: TFunction) {
  return z.object({
    RetryTimes: z.number().int().min(0).max(99),
    AutomaticRetryStatusCodes: z
      .string()
      .refine(
        (value) => parseHttpStatusCodeRules(value).ok,
        t('Invalid status code rules')
      ),
    channel_failover_setting: z
      .object({
        enabled: z.boolean(),
        primary_channel_id: z.number().int().min(0),
        fallback_channel_id: z.number().int().min(0),
        model_regex: z.string(),
        poll_interval_seconds: z.number().int().min(1).max(60),
        recovery_successes: z.number().int().min(1).max(10),
      })
      .superRefine((value, context) => {
        if (
          value.enabled &&
          (value.primary_channel_id <= 0 ||
            value.fallback_channel_id <= 0 ||
            value.primary_channel_id === value.fallback_channel_id)
        ) {
          context.addIssue({
            code: 'custom',
            path: ['fallback_channel_id'],
            message: t('Choose two different channels'),
          })
        }
        const patterns = value.model_regex
          .split('\n')
          .map((pattern) => pattern.trim())
          .filter(Boolean)
        if (
          patterns.length > 32 ||
          patterns.some(
            (pattern) => new TextEncoder().encode(pattern).length > 256
          )
        ) {
          context.addIssue({
            code: 'custom',
            path: ['model_regex'],
            message: t('Use up to 32 model patterns, each at most 256 bytes'),
          })
        }
      }),
    channel_affinity_setting: z.object({
      enabled: z.boolean(),
      session_mode: z.enum(['', 'off', 'prefer', 'strict']),
      switch_on_success: z.boolean(),
      keep_on_channel_disabled: z.boolean(),
      max_entries: z.number().int().min(0),
      default_ttl_seconds: z.number().int().min(0),
      rules: z.string().superRefine((value, context) => {
        try {
          const rules: unknown = JSON.parse(value)
          if (!Array.isArray(rules)) {
            context.addIssue({
              code: 'custom',
              message: t('Rules JSON must be an array'),
            })
          }
        } catch {
          context.addIssue({
            code: 'custom',
            message: t('Invalid rules JSON format'),
          })
        }
      }),
    }),
  })
}

export type RoutingPolicyFormValues = z.infer<
  ReturnType<typeof createRoutingPolicySchema>
>

export function routingPolicyFormValues(
  options: Record<string, string>
): RoutingPolicyFormValues {
  return {
    RetryTimes: Number(options.RetryTimes),
    AutomaticRetryStatusCodes: options.AutomaticRetryStatusCodes,
    channel_failover_setting: {
      enabled: options['channel_failover_setting.enabled'] === 'true',
      primary_channel_id: Number(
        options['channel_failover_setting.primary_channel_id'] || 0
      ),
      fallback_channel_id: Number(
        options['channel_failover_setting.fallback_channel_id'] || 0
      ),
      model_regex: (
        (JSON.parse(options['channel_failover_setting.model_regex'] || '[]') ??
          []) as string[]
      ).join('\n'),
      poll_interval_seconds: Number(
        options['channel_failover_setting.poll_interval_seconds'] || 3
      ),
      recovery_successes: Number(
        options['channel_failover_setting.recovery_successes'] || 2
      ),
    },
    channel_affinity_setting: {
      enabled: options['channel_affinity_setting.enabled'] === 'true',
      session_mode: (options['channel_affinity_setting.session_mode'] ||
        '') as RoutingPolicyFormValues['channel_affinity_setting']['session_mode'],
      switch_on_success:
        options['channel_affinity_setting.switch_on_success'] === 'true',
      keep_on_channel_disabled:
        options['channel_affinity_setting.keep_on_channel_disabled'] === 'true',
      max_entries: Number(options['channel_affinity_setting.max_entries']),
      default_ttl_seconds: Number(
        options['channel_affinity_setting.default_ttl_seconds']
      ),
      rules: options['channel_affinity_setting.rules'] || '[]',
    },
  }
}

export function routingPolicyOptions(
  values: RoutingPolicyFormValues
): Record<string, string> {
  return {
    RetryTimes: String(values.RetryTimes),
    AutomaticRetryStatusCodes: values.AutomaticRetryStatusCodes,
    ...Object.fromEntries(
      Object.entries(values.channel_failover_setting).map(([key, value]) => [
        `channel_failover_setting.${key}`,
        key === 'model_regex'
          ? JSON.stringify(
              String(value)
                .split('\n')
                .map((pattern) => pattern.trim())
                .filter(Boolean)
            )
          : String(value),
      ])
    ),
    ...Object.fromEntries(
      Object.entries(values.channel_affinity_setting).map(([key, value]) => [
        `channel_affinity_setting.${key}`,
        String(value),
      ])
    ),
  }
}
