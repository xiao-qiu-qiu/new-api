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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  buildPricingChanges,
  useModelPricing,
  useSaveModelPricing,
  type ModelPricingConfig,
} from '@/features/model-pricing/api'
import { pricingOptions } from '@/features/model-pricing/pricing'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { updateGroupSettings } from '../api'
import { groupSettingsJsonSchemas } from '../billing/group-settings-schema'
import { SettingsPageTitleStatusPortal } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { positiveIntegerSchema } from '../utils/numeric-field'
import { GroupRatioForm } from './group-ratio-form'
import {
  formatGroupSettings,
  normalizeGroupSettings,
  type GroupSettingsDefaults,
} from './group-settings-codec'
import { ModelRatioForm } from './model-ratio-form'
import { ToolPriceSettings } from './tool-price-settings'
import { UpstreamRatioSync } from './upstream-ratio-sync'
import {
  formatJsonForTextarea,
  type JsonValidationError,
  normalizeJsonString,
  validateJsonString,
} from './utils'

type Translate = (key: string, options?: Record<string, unknown>) => string

function formatJsonValidationError(
  t: Translate,
  error?: JsonValidationError,
  fallback = 'Invalid JSON'
) {
  if (!error) return t(fallback)

  if (error.type === 'required') return t('Value is required')
  if (error.type === 'structure') {
    return t(
      fallback === 'Invalid JSON' ? 'JSON structure is invalid' : fallback
    )
  }

  let locationMessage: string
  if (error.line && error.column) {
    locationMessage = t(
      'JSON is invalid at line {{line}}, column {{column}}.',
      {
        line: error.line,
        column: error.column,
      }
    )
  } else if (error.position !== undefined) {
    locationMessage = t('JSON is invalid at position {{position}}.', {
      position: error.position,
    })
  } else {
    locationMessage = t('JSON is invalid. Please check the syntax.')
  }

  const parts = [locationMessage]

  if (error.missingCommaLine) {
    parts.push(
      t('Check line {{line}} for a missing comma.', {
        line: error.missingCommaLine,
      })
    )
  }

  return parts.join(' ')
}

function createJsonStringField(
  t: Translate,
  options?: Parameters<typeof validateJsonString>[1]
) {
  return z.string().superRefine((value, ctx) => {
    const result = validateJsonString(value, options)
    if (!result.valid) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        message: formatJsonValidationError(t, result.error, result.message),
      })
    }
  })
}

const createModelSchema = (t: Translate) =>
  z.object({
    ModelPrice: createJsonStringField(t),
    ModelRatio: createJsonStringField(t),
    CacheRatio: createJsonStringField(t),
    CreateCacheRatio: createJsonStringField(t),
    CompletionRatio: createJsonStringField(t),
    ImageRatio: createJsonStringField(t),
    AudioRatio: createJsonStringField(t),
    AudioCompletionRatio: createJsonStringField(t),
    ExposeRatioEnabled: z.boolean(),
    BillingMode: createJsonStringField(t),
    BillingExpr: createJsonStringField(t),
    IgnoreGroupRatio: createJsonStringField(t, {
      predicate: (parsed) =>
        parsed !== null &&
        typeof parsed === 'object' &&
        !Array.isArray(parsed) &&
        Object.values(parsed).every((value) => typeof value === 'boolean'),
    }),
    PluginBillingExpr: createJsonStringField(t),
  })

function createGroupJsonField(
  t: Translate,
  field: keyof typeof groupSettingsJsonSchemas
) {
  return createJsonStringField(t, {
    allowEmpty: false,
    predicate: (value) =>
      groupSettingsJsonSchemas[field].safeParse(value).success,
  })
}

const createGroupSchema = (t: Translate) =>
  z.object({
    UserGroups: createGroupJsonField(t, 'UserGroups'),
    GroupRatio: createGroupJsonField(t, 'GroupRatio'),
    TopupGroupRatio: createGroupJsonField(t, 'TopupGroupRatio'),
    UserUsableGroups: createGroupJsonField(t, 'UserUsableGroups'),
    GroupGroupRatio: createGroupJsonField(t, 'GroupGroupRatio'),
    SpecialPricingRules: createGroupJsonField(t, 'SpecialPricingRules'),
    GroupSpecialUsableGroup: createGroupJsonField(t, 'GroupSpecialUsableGroup'),
    AutoGroups: createGroupJsonField(t, 'AutoGroups'),
    MaxTokenAutoGroups: positiveIntegerSchema(t('Enter a positive integer')),
    DefaultUseAutoGroup: z.boolean(),
  })

type ModelFormValues = z.infer<ReturnType<typeof createModelSchema>>
type GroupFormValues = z.infer<ReturnType<typeof createGroupSchema>>
type RatioTabId =
  | 'models'
  | 'unset-models'
  | 'groups'
  | 'tool-prices'
  | 'upstream-sync'

type RatioSettingsCardProps = {
  modelDefaults: Omit<ModelFormValues, 'IgnoreGroupRatio'> & {
    IgnoreGroupRatio?: string
  }
  groupDefaults: GroupSettingsDefaults
  toolPricesDefault: string
  titleKey?: string
  visibleTabs?: RatioTabId[]
}

export function RatioSettingsCard({
  modelDefaults: initialModelDefaults,
  groupDefaults,
  toolPricesDefault,
  titleKey = 'Pricing Ratios',
  visibleTabs = ['models', 'groups', 'tool-prices', 'upstream-sync'],
}: RatioSettingsCardProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const queryClient = useQueryClient()
  const saveGroups = useMutation({
    mutationFn: async (options: Record<string, string>) =>
      requireServerSuccess(await updateGroupSettings(options)),
    meta: { errorToast: false },
  })
  const [confirmOpen, setConfirmOpen] = useState(false)

  const pricingQuery = useModelPricing(
    [],
    visibleTabs.some((tab) => tab === 'models' || tab === 'unset-models')
  )
  const savePricing = useSaveModelPricing()
  const [pricingBaseline, setPricingBaseline] =
    useState<ModelPricingConfig | null>(null)
  useEffect(() => {
    if (!pricingBaseline && pricingQuery.data) {
      setPricingBaseline(pricingQuery.data)
    }
  }, [pricingBaseline, pricingQuery.data])
  const modelDefaults = useMemo(() => {
    const defaults = {
      ...initialModelDefaults,
      IgnoreGroupRatio: initialModelDefaults.IgnoreGroupRatio ?? '{}',
    }
    if (!pricingBaseline) return defaults
    return {
      ...defaults,
      ...pricingBaseline.options,
      BillingMode: pricingBaseline.options['billing_setting.billing_mode'],
      BillingExpr: pricingBaseline.options['billing_setting.billing_expr'],
      IgnoreGroupRatio:
        pricingBaseline.options['billing_setting.ignore_group_ratio'] ??
        defaults.IgnoreGroupRatio,
      PluginBillingExpr:
        pricingBaseline.options['billing_setting.plugin_billing_expr'],
    }
  }, [initialModelDefaults, pricingBaseline])
  const resetMutation = useMutation({
    mutationFn: async () => {
      if (!pricingBaseline) return
      await savePricing.mutateAsync(
        pricingBaseline.entries.map((entry) => ({
          model_name: entry.model_name,
          expected_version: entry.version,
          pricing: {},
          reset: true,
        }))
      )
      const refreshed = await pricingQuery.refetch()
      setPricingBaseline(refreshed.data ?? null)
    },
    onSuccess: () => {
      toast.success(t('Model prices reset successfully'))
      setConfirmOpen(false)
    },
    onError: (error) => handleServerError(error),
  })

  const modelNormalizedDefaults = useRef({
    ModelPrice: normalizeJsonString(modelDefaults.ModelPrice),
    ModelRatio: normalizeJsonString(modelDefaults.ModelRatio),
    CacheRatio: normalizeJsonString(modelDefaults.CacheRatio),
    CreateCacheRatio: normalizeJsonString(modelDefaults.CreateCacheRatio),
    CompletionRatio: normalizeJsonString(modelDefaults.CompletionRatio),
    ImageRatio: normalizeJsonString(modelDefaults.ImageRatio),
    AudioRatio: normalizeJsonString(modelDefaults.AudioRatio),
    AudioCompletionRatio: normalizeJsonString(
      modelDefaults.AudioCompletionRatio
    ),
    ExposeRatioEnabled: modelDefaults.ExposeRatioEnabled,
    BillingMode: normalizeJsonString(modelDefaults.BillingMode),
    BillingExpr: normalizeJsonString(modelDefaults.BillingExpr),
    IgnoreGroupRatio: normalizeJsonString(modelDefaults.IgnoreGroupRatio),
    PluginBillingExpr: normalizeJsonString(modelDefaults.PluginBillingExpr),
  })
  const [savedModelValues, setSavedModelValues] = useState(
    modelNormalizedDefaults.current
  )

  const groupNormalizedDefaults = useRef(normalizeGroupSettings(groupDefaults))
  const modelSchema = useMemo(() => createModelSchema(t), [t])
  const groupSchema = useMemo(() => createGroupSchema(t), [t])

  const modelForm = useForm<ModelFormValues>({
    resolver: zodResolver(modelSchema),
    mode: 'onChange',
    defaultValues: {
      ...modelDefaults,
      ModelPrice: formatJsonForTextarea(modelDefaults.ModelPrice),
      ModelRatio: formatJsonForTextarea(modelDefaults.ModelRatio),
      CacheRatio: formatJsonForTextarea(modelDefaults.CacheRatio),
      CreateCacheRatio: formatJsonForTextarea(modelDefaults.CreateCacheRatio),
      CompletionRatio: formatJsonForTextarea(modelDefaults.CompletionRatio),
      ImageRatio: formatJsonForTextarea(modelDefaults.ImageRatio),
      AudioRatio: formatJsonForTextarea(modelDefaults.AudioRatio),
      AudioCompletionRatio: formatJsonForTextarea(
        modelDefaults.AudioCompletionRatio
      ),
      BillingMode: formatJsonForTextarea(modelDefaults.BillingMode),
      BillingExpr: formatJsonForTextarea(modelDefaults.BillingExpr),
      IgnoreGroupRatio: formatJsonForTextarea(modelDefaults.IgnoreGroupRatio),
      PluginBillingExpr: formatJsonForTextarea(modelDefaults.PluginBillingExpr),
    },
  })

  const groupForm = useForm<GroupFormValues>({
    resolver: zodResolver(groupSchema),
    mode: 'onChange',
    defaultValues: formatGroupSettings(groupDefaults),
  })

  useEffect(() => {
    modelNormalizedDefaults.current = {
      ModelPrice: normalizeJsonString(modelDefaults.ModelPrice),
      ModelRatio: normalizeJsonString(modelDefaults.ModelRatio),
      CacheRatio: normalizeJsonString(modelDefaults.CacheRatio),
      CreateCacheRatio: normalizeJsonString(modelDefaults.CreateCacheRatio),
      CompletionRatio: normalizeJsonString(modelDefaults.CompletionRatio),
      ImageRatio: normalizeJsonString(modelDefaults.ImageRatio),
      AudioRatio: normalizeJsonString(modelDefaults.AudioRatio),
      AudioCompletionRatio: normalizeJsonString(
        modelDefaults.AudioCompletionRatio
      ),
      ExposeRatioEnabled: modelDefaults.ExposeRatioEnabled,
      BillingMode: normalizeJsonString(modelDefaults.BillingMode),
      BillingExpr: normalizeJsonString(modelDefaults.BillingExpr),
      IgnoreGroupRatio: normalizeJsonString(modelDefaults.IgnoreGroupRatio),
      PluginBillingExpr: normalizeJsonString(modelDefaults.PluginBillingExpr),
    }
    setSavedModelValues(modelNormalizedDefaults.current)

    modelForm.reset({
      ...modelDefaults,
      ModelPrice: formatJsonForTextarea(modelDefaults.ModelPrice),
      ModelRatio: formatJsonForTextarea(modelDefaults.ModelRatio),
      CacheRatio: formatJsonForTextarea(modelDefaults.CacheRatio),
      CreateCacheRatio: formatJsonForTextarea(modelDefaults.CreateCacheRatio),
      CompletionRatio: formatJsonForTextarea(modelDefaults.CompletionRatio),
      ImageRatio: formatJsonForTextarea(modelDefaults.ImageRatio),
      AudioRatio: formatJsonForTextarea(modelDefaults.AudioRatio),
      AudioCompletionRatio: formatJsonForTextarea(
        modelDefaults.AudioCompletionRatio
      ),
      BillingMode: formatJsonForTextarea(modelDefaults.BillingMode),
      BillingExpr: formatJsonForTextarea(modelDefaults.BillingExpr),
      IgnoreGroupRatio: formatJsonForTextarea(modelDefaults.IgnoreGroupRatio),
      PluginBillingExpr: formatJsonForTextarea(modelDefaults.PluginBillingExpr),
    })
  }, [modelDefaults, modelForm])

  useEffect(() => {
    if (groupForm.formState.isDirty) return
    groupNormalizedDefaults.current = normalizeGroupSettings(groupDefaults)
    groupForm.reset(formatGroupSettings(groupDefaults))
  }, [groupDefaults, groupForm])

  const saveModelRatios = useCallback(
    async (values: ModelFormValues) => {
      const normalized = {
        ModelPrice: normalizeJsonString(values.ModelPrice),
        ModelRatio: normalizeJsonString(values.ModelRatio),
        CacheRatio: normalizeJsonString(values.CacheRatio),
        CreateCacheRatio: normalizeJsonString(values.CreateCacheRatio),
        CompletionRatio: normalizeJsonString(values.CompletionRatio),
        ImageRatio: normalizeJsonString(values.ImageRatio),
        AudioRatio: normalizeJsonString(values.AudioRatio),
        AudioCompletionRatio: normalizeJsonString(values.AudioCompletionRatio),
        ExposeRatioEnabled: values.ExposeRatioEnabled,
        BillingMode: normalizeJsonString(values.BillingMode),
        BillingExpr: normalizeJsonString(values.BillingExpr),
        IgnoreGroupRatio: normalizeJsonString(values.IgnoreGroupRatio),
        PluginBillingExpr: normalizeJsonString(values.PluginBillingExpr),
      }

      if (!pricingBaseline) return
      try {
        const changes = buildPricingChanges(
          pricingBaseline,
          pricingOptions(modelNormalizedDefaults.current),
          pricingOptions(normalized)
        )
        const visibilityChanged =
          normalized.ExposeRatioEnabled !==
          modelNormalizedDefaults.current.ExposeRatioEnabled
        if (!changes.length && !visibilityChanged) {
          toast.info(t('No model price changes to save'))
          return
        }
        await savePricing.mutateAsync(changes)
        if (visibilityChanged) {
          await updateOption.mutateAsync({
            key: 'ExposeRatioEnabled',
            value: normalized.ExposeRatioEnabled,
          })
        }
        const refreshed = await pricingQuery.refetch()
        setPricingBaseline(refreshed.data ?? null)
        modelNormalizedDefaults.current = normalized
        setSavedModelValues(normalized)
        toast.success(t('Model pricing saved'))
      } catch (error) {
        handleServerError(error)
      }
    },
    [t, updateOption, pricingBaseline, savePricing, pricingQuery]
  )

  const saveGroupRatios = useCallback(
    async (values: GroupFormValues) => {
      const normalized = normalizeGroupSettings(
        values,
        groupNormalizedDefaults.current.UserGroups
      )

      // Map form field names to API keys (most are 1:1, except GroupSpecialUsableGroup)
      const apiKeyMap: Record<string, string> = {
        GroupSpecialUsableGroup:
          'group_ratio_setting.group_special_usable_group',
      }

      const updates = (
        Object.keys(normalized) as Array<keyof typeof normalized>
      ).filter(
        (key) => normalized[key] !== groupNormalizedDefaults.current[key]
      )

      if (!updates.length) return
      const options = Object.fromEntries(
        updates.map((key) => [apiKeyMap[key] || key, String(normalized[key])])
      )
      try {
        await saveGroups.mutateAsync(options)
        groupNormalizedDefaults.current = normalized
        groupForm.reset(normalized)
        toast.success(t('Setting updated successfully'))
        await Promise.all(
          [
            'system-options',
            'account-user-groups',
            'groups',
            'user-groups',
            'playground-groups',
            'token-auto-groups',
            'pricing',
          ].map((key) => queryClient.invalidateQueries({ queryKey: [key] }))
        )
      } catch (error) {
        handleServerError(error)
      }
    },
    [saveGroups, groupForm, queryClient, t]
  )

  const handleResetRatios = useCallback(() => {
    setConfirmOpen(true)
  }, [])

  const { mutate: resetMutate } = resetMutation
  const handleConfirmReset = useCallback(() => {
    resetMutate()
  }, [resetMutate])

  const tabLabels: Record<RatioTabId, string> = {
    models: 'Model prices',
    'unset-models': 'Unset price models',
    groups: 'Group ratios',
    'tool-prices': 'Tool prices',
    'upstream-sync': 'Upstream price sync',
  }
  const tabsGridClass =
    {
      1: 'grid-cols-1',
      2: 'grid-cols-2',
      3: 'grid-cols-3',
      4: 'grid-cols-4',
      5: 'grid-cols-5',
    }[visibleTabs.length] ?? 'grid-cols-4'
  const defaultTab = visibleTabs[0] ?? 'models'

  const renderTabContent = (tab: RatioTabId) => {
    if (tab === 'models' || tab === 'unset-models') {
      if (pricingQuery.isError) {
        return (
          <ErrorState
            description={pricingQuery.error.message}
            onRetry={() => void pricingQuery.refetch()}
          />
        )
      }
      if (!pricingBaseline) return <LoadingState />
      return (
        <>
          {savePricing.isError && (
            <Button
              variant='outline'
              size='sm'
              onClick={async () => {
                const refreshed = await pricingQuery.refetch()
                if (refreshed.data) setPricingBaseline(refreshed.data)
                savePricing.reset()
              }}
            >
              {t('Reload pricing')}
            </Button>
          )}
          <ModelRatioForm
            form={modelForm}
            savedValues={savedModelValues}
            onSave={saveModelRatios}
            onReset={handleResetRatios}
            isSaving={updateOption.isPending || savePricing.isPending}
            isResetting={resetMutation.isPending}
            variant={tab === 'unset-models' ? 'unset' : 'default'}
          />
        </>
      )
    }
    if (tab === 'groups') {
      if (!groupSchema.safeParse(formatGroupSettings(groupDefaults)).success) {
        return <ErrorState description={t('JSON structure is invalid')} />
      }
      return (
        <GroupRatioForm
          form={groupForm}
          onSave={saveGroupRatios}
          isSaving={saveGroups.isPending}
        />
      )
    }
    if (tab === 'tool-prices') {
      return <ToolPriceSettings defaultValue={toolPricesDefault} />
    }
    return <UpstreamRatioSync />
  }

  const renderTabSwitcher = () => (
    <TabsList className={`grid w-fit max-w-full ${tabsGridClass}`}>
      {visibleTabs.map((tab) => (
        <TabsTrigger key={tab} value={tab}>
          {t(tabLabels[tab])}
        </TabsTrigger>
      ))}
    </TabsList>
  )

  return (
    <>
      {visibleTabs.length === 1 ? (
        <SettingsSection
          title={t(titleKey)}
          className={
            defaultTab === 'models' || defaultTab === 'unset-models'
              ? 'min-h-0 flex-1'
              : undefined
          }
        >
          {renderTabContent(defaultTab)}
        </SettingsSection>
      ) : (
        <Tabs defaultValue={defaultTab} className='h-full min-h-0 gap-6'>
          <SettingsPageTitleStatusPortal>
            {renderTabSwitcher()}
          </SettingsPageTitleStatusPortal>

          <SettingsSection title={t(titleKey)} className='min-h-0 flex-1'>
            {visibleTabs.map((tab) => (
              <TabsContent
                key={tab}
                value={tab}
                className={
                  tab === 'models' || tab === 'unset-models'
                    ? 'flex min-h-0 flex-col data-hidden:hidden'
                    : 'min-h-0'
                }
              >
                {renderTabContent(tab)}
              </TabsContent>
            ))}
          </SettingsSection>
        </Tabs>
      )}

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('Reset all model prices?')}
        desc={t(
          'This will clear custom pricing ratios and revert to upstream defaults.'
        )}
        destructive
        isLoading={resetMutation.isPending}
        handleConfirm={handleConfirmReset}
        confirmText={t('Reset')}
      />
    </>
  )
}
