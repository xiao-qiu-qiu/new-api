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
import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

export type UserGroupsEditorProps = {
  userGroups: string
  topupGroupRatio: string
  onChange: (field: string, value: string) => void
}

export function UserGroupsEditor(props: UserGroupsEditorProps) {
  const { t } = useTranslation()
  const formId = useId()
  const [ratioDrafts, setRatioDrafts] = useState<
    Record<string, { text: string; source: number | string | undefined }>
  >({})
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<string | null>(null)
  let userGroups: Record<string, string> = {}
  let topupRatios: Record<string, number | string> = {}
  let invalid = false
  try {
    userGroups = z
      .record(z.string(), z.string())
      .parse(JSON.parse(props.userGroups || '{}'))
    topupRatios = z
      .record(z.string(), z.union([z.number(), z.string()]))
      .parse(JSON.parse(props.topupGroupRatio || '{}'))
  } catch {
    invalid = true
  }
  const schema = z.object({
    name: z
      .string()
      .trim()
      .min(1, t('Value is required'))
      .refine(
        (name) => [...name].length <= 64,
        t('User group name cannot exceed 64 characters')
      )
      .refine(
        (name) =>
          name !== 'auto' && name !== '*' && !Object.hasOwn(userGroups, name),
        t('User group name already exists or is reserved')
      ),
  })
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { name: '' },
  })
  if (invalid) {
    return (
      <Alert variant='destructive'>
        <AlertDescription>
          {t(
            'Invalid user group configuration. Correct the JSON before editing.'
          )}
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex justify-end'>
        <Button
          type='button'
          variant='outline'
          onClick={() => {
            form.reset()
            setAdding(true)
          }}
        >
          {t('Add user group')}
        </Button>
      </div>
      <StaticDataTable
        tableProps={{ 'aria-label': t('User groups') }}
        data={Object.keys(userGroups).map((name) => ({ name }))}
        getRowKey={(row) => row.name}
        emptyContent={
          <EmptyState title={t('No user groups')} className='min-h-28' />
        }
        columns={[
          {
            id: 'name',
            header: t('User group name'),
            cell: (row) => <span className='break-all'>{row.name}</span>,
          },
          {
            id: 'topup',
            header: t('Top-up ratio'),
            cell: (row) => {
              const storedRatio = Object.hasOwn(topupRatios, row.name)
                ? topupRatios[row.name]
                : undefined
              const ratio = storedRatio ?? 1
              const valid =
                typeof ratio === 'number' &&
                Number.isFinite(ratio) &&
                ratio >= 0
              const draft = ratioDrafts[row.name]
              const value =
                draft && Object.is(draft.source, storedRatio)
                  ? draft.text
                  : ratio
              return (
                <Input
                  type='number'
                  min={0}
                  step={0.0001}
                  className='min-w-24'
                  value={value}
                  aria-label={t('Top-up ratio for {{group}}', {
                    group: row.name,
                  })}
                  aria-invalid={!valid}
                  onChange={(event) => {
                    const text = event.target.value
                    const next = { ...topupRatios }
                    const parsed = Number(text)
                    if (text === '' && !event.target.validity.badInput) {
                      delete next[row.name]
                    } else {
                      next[row.name] =
                        Number.isFinite(parsed) && parsed >= 0 && text !== ''
                          ? parsed
                          : text || 'invalid'
                    }
                    setRatioDrafts((previous) => ({
                      ...previous,
                      [row.name]: { text, source: next[row.name] },
                    }))
                    props.onChange(
                      'TopupGroupRatio',
                      JSON.stringify(next, null, 2)
                    )
                  }}
                  onBlur={() =>
                    setRatioDrafts((previous) => {
                      if (previous[row.name]?.text !== '') return previous
                      const next = { ...previous }
                      delete next[row.name]
                      return next
                    })
                  }
                />
              )
            },
          },
          {
            id: 'actions',
            header: t('Actions'),
            cell: (row) => (
              <Button
                type='button'
                variant='ghost'
                disabled={row.name === 'default'}
                aria-label={t('Delete user group {{group}}', {
                  group: row.name,
                })}
                onClick={() => setDeleting(row.name)}
              >
                {t('Delete')}
              </Button>
            ),
          },
        ]}
      />
      <Dialog
        open={adding}
        onOpenChange={setAdding}
        title={t('Add user group')}
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => setAdding(false)}
            >
              {t('Cancel')}
            </Button>
            <Button type='submit' form={formId}>
              {t('Add')}
            </Button>
          </>
        }
      >
        <form
          id={formId}
          onSubmit={form.handleSubmit((values) => {
            props.onChange(
              'UserGroups',
              JSON.stringify({ ...userGroups, [values.name]: '' }, null, 2)
            )
            setAdding(false)
          })}
        >
          <FieldGroup>
            <Field data-invalid={!!form.formState.errors.name}>
              <FieldLabel htmlFor={`${formId}-name`}>
                {t('User group name')}
              </FieldLabel>
              <Input
                id={`${formId}-name`}
                {...form.register('name')}
                aria-invalid={!!form.formState.errors.name}
                aria-describedby={
                  form.formState.errors.name ? `${formId}-error` : undefined
                }
              />
              {form.formState.errors.name && (
                <FieldError id={`${formId}-error`}>
                  {form.formState.errors.name.message}
                </FieldError>
              )}
            </Field>
          </FieldGroup>
        </form>
      </Dialog>
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={t('Delete user group')}
        desc={t(
          'Delete user group {{group}}? Groups still in use cannot be deleted.',
          { group: deleting }
        )}
        destructive
        confirmText={t('Delete')}
        handleConfirm={() => {
          if (deleting === null) return
          const next = { ...userGroups }
          delete next[deleting]
          const nextTopup = { ...topupRatios }
          delete nextTopup[deleting]
          props.onChange('TopupGroupRatio', JSON.stringify(nextTopup, null, 2))
          props.onChange('UserGroups', JSON.stringify(next, null, 2))
          setDeleting(null)
        }}
      />
    </div>
  )
}
