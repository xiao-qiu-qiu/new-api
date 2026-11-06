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

import { Code2, Eye, HelpCircle } from 'lucide-react'
import { memo, useState } from 'react'
import { useWatch, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { JsonCodeEditor } from '@/components/json-code-editor'
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
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent } from '@/components/ui/tabs'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageActionsPortal } from '../components/settings-page-context'
import { safeNumberFieldProps } from '../utils/numeric-field'
import {
  GroupRatioVisualEditor,
  GroupSettingsNavigation,
  type GroupSettingsSection,
} from './group-ratio-visual-editor'

type GroupFormValues = {
  UserGroups: string
  GroupRatio: string
  TopupGroupRatio: string
  UserUsableGroups: string
  GroupGroupRatio: string
  SpecialPricingRules: string
  AutoGroups: string
  MaxTokenAutoGroups: number
  DefaultUseAutoGroup: boolean
  GroupSpecialUsableGroup: string
}

type GroupRatioFormProps = {
  form: UseFormReturn<GroupFormValues>
  onSave: (values: GroupFormValues) => Promise<void>
  isSaving: boolean
}

type JsonGroupField = Exclude<
  keyof GroupFormValues,
  'MaxTokenAutoGroups' | 'DefaultUseAutoGroup'
>
const fieldSections: Record<keyof GroupFormValues, GroupSettingsSection> = {
  UserGroups: 'users',
  TopupGroupRatio: 'users',
  GroupRatio: 'pricing',
  UserUsableGroups: 'pricing',
  GroupGroupRatio: 'overrides',
  SpecialPricingRules: 'special-pricing',
  GroupSpecialUsableGroup: 'visibility',
  AutoGroups: 'auto',
  MaxTokenAutoGroups: 'auto',
  DefaultUseAutoGroup: 'auto',
}

export const GroupRatioForm = memo(function GroupRatioForm(
  props: GroupRatioFormProps
) {
  const { t } = useTranslation()
  const values = useWatch({ control: props.form.control })
  const [editMode, setEditMode] = useState<'visual' | 'json'>('visual')
  const [guideOpen, setGuideOpen] = useState(false)
  const [visualValid, setVisualValid] = useState(true)
  const [section, setSection] = useState<GroupSettingsSection>('pricing')
  const jsonFields: { name: JsonGroupField; label: string }[] = [
    { name: 'GroupRatio', label: t('Multiplier groups') },
    { name: 'UserUsableGroups', label: t('Selectable groups') },
    { name: 'UserGroups', label: t('User group default multiplier groups') },
    { name: 'TopupGroupRatio', label: t('Top-up group ratios') },
    { name: 'GroupGroupRatio', label: t('Special ratio rules') },
    { name: 'SpecialPricingRules', label: t('Special pricing rules') },
    { name: 'GroupSpecialUsableGroup', label: t('Special usable group rules') },
    { name: 'AutoGroups', label: t('Auto group order') },
  ]

  const maxTokenAutoGroupsField = (
    <FormField
      control={props.form.control}
      name='MaxTokenAutoGroups'
      render={({ field, fieldState }) => (
        <FormItem data-invalid={fieldState.invalid}>
          <FormLabel>{t('Maximum custom groups per token')}</FormLabel>
          <FormControl>
            <Input
              {...safeNumberFieldProps(field)}
              type='number'
              min={1}
              step={1}
              aria-invalid={fieldState.invalid}
            />
          </FormControl>
          <FormDescription>
            {t(
              'Limits only token-specific Auto snapshots. Global Auto inheritance remains unlimited.'
            )}
          </FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )
  const defaultUseAutoGroupField = (
    <FormField
      control={props.form.control}
      name='DefaultUseAutoGroup'
      render={({ field }) => (
        <SettingsSwitchItem>
          <SettingsSwitchContent>
            <FormLabel>{t('Default to auto groups')}</FormLabel>
            <FormDescription>
              {t(
                'If default auto group is enabled, newly created tokens start with auto instead of an empty group.'
              )}
            </FormDescription>
          </SettingsSwitchContent>
          <FormControl>
            <Switch checked={field.value} onCheckedChange={field.onChange} />
          </FormControl>
        </SettingsSwitchItem>
      )}
    />
  )

  const submit = props.form.handleSubmit(props.onSave, (errors) => {
    const name = Object.keys(errors)[0] as keyof GroupFormValues | undefined
    if (!name) return
    setSection(fieldSections[name])
    setEditMode(name === 'MaxTokenAutoGroups' ? 'visual' : 'json')
    requestAnimationFrame(() => props.form.setFocus(name))
  })

  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex flex-wrap justify-end gap-2'>
        <Button variant='outline' size='sm' onClick={() => setGuideOpen(true)}>
          <HelpCircle className='mr-2 size-4' aria-hidden='true' />
          {t('Usage guide')}
        </Button>
        <Button
          variant='outline'
          size='sm'
          disabled={editMode === 'visual' && !visualValid}
          onClick={async () => {
            if (editMode === 'json' && !(await props.form.trigger())) return
            setEditMode(editMode === 'visual' ? 'json' : 'visual')
          }}
        >
          {editMode === 'visual' ? (
            <Code2 className='mr-2 size-4' aria-hidden='true' />
          ) : (
            <Eye className='mr-2 size-4' aria-hidden='true' />
          )}
          {editMode === 'visual' ? t('Switch to JSON') : t('Switch to Visual')}
        </Button>
      </div>
      <Form {...props.form}>
        <SettingsPageActionsPortal>
          <Button
            type='button'
            size='sm'
            onClick={submit}
            disabled={props.isSaving || !visualValid}
          >
            {props.isSaving ? t('Saving...') : t('Save group settings')}
          </Button>
        </SettingsPageActionsPortal>
        {editMode === 'visual' ? (
          <GroupRatioVisualEditor
            section={section}
            onSectionChange={setSection}
            userGroups={values.UserGroups ?? '{}'}
            groupRatio={values.GroupRatio ?? '{}'}
            topupGroupRatio={values.TopupGroupRatio ?? '{}'}
            userUsableGroups={values.UserUsableGroups ?? '{}'}
            groupGroupRatio={values.GroupGroupRatio ?? '{}'}
            specialPricingRules={values.SpecialPricingRules ?? '[]'}
            autoGroups={values.AutoGroups ?? '[]'}
            groupSpecialUsableGroup={values.GroupSpecialUsableGroup ?? '{}'}
            maxTokenAutoGroupsField={maxTokenAutoGroupsField}
            defaultUseAutoGroupField={defaultUseAutoGroupField}
            onValidationChange={setVisualValid}
            onChange={(field, value) =>
              props.form.setValue(field as JsonGroupField, value, {
                shouldValidate: true,
                shouldDirty: true,
              })
            }
          />
        ) : (
          <Tabs
            value={section}
            onValueChange={(value) => setSection(value as GroupSettingsSection)}
            className='min-w-0 gap-5'
          >
            <GroupSettingsNavigation />
            <TabsContent key={section} value={section}>
              <SettingsForm onSubmit={submit}>
                {jsonFields
                  .filter((field) => fieldSections[field.name] === section)
                  .map((entry) => (
                    <FormField
                      key={entry.name}
                      control={props.form.control}
                      name={entry.name}
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>{entry.label}</FormLabel>
                          <FormControl>
                            <JsonCodeEditor
                              value={field.value}
                              onChange={field.onChange}
                              name={field.name}
                              onBlur={field.onBlur}
                              textareaRef={field.ref}
                            />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  ))}
                {section === 'auto' && (
                  <>
                    {maxTokenAutoGroupsField}
                    {defaultUseAutoGroupField}
                  </>
                )}
              </SettingsForm>
            </TabsContent>
          </Tabs>
        )}
      </Form>
      <Sheet open={guideOpen} onOpenChange={setGuideOpen}>
        <SheetContent
          side='right'
          className={sideDrawerContentClassName('sm:max-w-lg')}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('Group pricing usage guide')}</SheetTitle>
            <SheetDescription>
              {t('User groups and multiplier groups have separate roles.')}
            </SheetDescription>
          </SheetHeader>
          <div className={sideDrawerFormClassName('gap-5')}>
            <section className='space-y-2'>
              <h3 className='text-sm font-semibold'>{t('User groups')}</h3>
              <p className='text-muted-foreground text-sm leading-6'>
                {t(
                  'User groups define account membership, top-up ratios and a default multiplier group. Assign them in user management or subscription plans.'
                )}
              </p>
            </section>
            <section className='space-y-2'>
              <h3 className='text-sm font-semibold'>
                {t('Multiplier groups')}
              </h3>
              <p className='text-muted-foreground text-sm leading-6'>
                {t(
                  'Multiplier groups define channel routing and the base ratio for API calls.'
                )}
              </p>
              <p className='text-muted-foreground text-sm leading-6'>
                {t(
                  'A token with no group uses its user group’s default multiplier group. Auto tries the configured multiplier groups in order.'
                )}
              </p>
            </section>
            <section className='space-y-2'>
              <h3 className='text-sm font-semibold'>
                {t('Special ratio rules')}
              </h3>
              <p className='text-muted-foreground text-sm leading-6'>
                {t(
                  'A rule maps a user group to a multiplier group. Its ratio replaces the base ratio; it is not an additional discount.'
                )}
              </p>
            </section>
            <section className='space-y-2'>
              <h3 className='text-sm font-semibold'>{t('Group visibility')}</h3>
              <p className='text-muted-foreground text-sm leading-6'>
                {t(
                  'Select which multiplier groups are available to everyone, then add or hide groups for specific user groups.'
                )}
              </p>
            </section>
          </div>
        </SheetContent>
      </Sheet>
    </div>
  )
})
