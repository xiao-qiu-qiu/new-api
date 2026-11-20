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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'
import { AutoGroupOrderEditor } from '@/features/keys/components/auto-group-order-editor'
import { handleServerError } from '@/lib/handle-server-error'

import { getAdminApiKeyGroups, updateAdminApiKeyGroups } from '../api'
import type { AdminApiKey } from '../types'

interface Props {
  keys: AdminApiKey[]
  onClose: () => void
  onSaved: () => void
}

type GroupForm = { groups: string[]; crossGroupRetry: boolean }
const EMPTY_GROUPS: string[] = []

function getSelectedGroups(key?: AdminApiKey) {
  if (key?.group === 'auto') return key.auto_groups ?? []
  return key?.group ? [key.group] : []
}

export function AdminApiKeyGroupDialog(props: Props) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const ids = props.keys.map((key) => key.id)
  const current = props.keys.length === 1 ? props.keys[0] : undefined
  const [pending, setPending] = useState<GroupForm | null>(null)
  const groupsQuery = useQuery({
    queryKey: ['admin-key-groups', ids],
    queryFn: () => getAdminApiKeyGroups(ids),
    staleTime: 0,
  })
  const groups = groupsQuery.data?.groups ?? EMPTY_GROUPS
  const maxCount = groupsQuery.data?.max_count ?? 20
  const schema = useMemo(
    () =>
      z.object({
        groups: z
          .array(z.string())
          .min(1, t('Select at least one group for this token'))
          .max(maxCount, t('Select at most {{max}} groups', { max: maxCount }))
          .refine(
            (selected) => selected.every((group) => groups.includes(group)),
            t('Remove unavailable groups before saving.')
          ),
        crossGroupRetry: z.boolean(),
      }),
    [t, groups, maxCount]
  )
  const form = useForm<GroupForm>({
    resolver: zodResolver(schema),
    defaultValues: {
      groups: getSelectedGroups(current),
      crossGroupRetry: current?.group === 'auto' && current.cross_group_retry,
    },
  })
  const mutation = useMutation({
    mutationFn: (values: GroupForm) => {
      const isMultiGroup = values.groups.length > 1
      return updateAdminApiKeyGroups({
        ids,
        group: isMultiGroup ? 'auto' : values.groups[0],
        auto_groups: isMultiGroup ? values.groups : [],
        cross_group_retry: isMultiGroup && values.crossGroupRetry,
      })
    },
    onSuccess: (data) => {
      toast.success(
        t('Updated groups for {{count}} API keys', {
          count: data?.count ?? ids.length,
        })
      )
      void client.invalidateQueries({ queryKey: ['admin-keys'] })
      void client.invalidateQueries({ queryKey: ['keys'] })
      void client.invalidateQueries({ queryKey: ['api-key'] })
      void client.invalidateQueries({ queryKey: ['admin-key-groups'] })
      props.onSaved()
      props.onClose()
    },
    onError: (error) => {
      setPending(null)
      handleServerError(error)
    },
  })
  const isMultiGroup = form.watch('groups').length > 1
  const canSave =
    groupsQuery.isSuccess &&
    !groupsQuery.isFetching &&
    groups.length > 0 &&
    !mutation.isPending
  const options = groups.map((group) => ({ value: group, label: group }))

  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => {
          if (!open && !mutation.isPending) props.onClose()
        }}
        title={t('Change API key groups')}
        description={t('Update the selected groups for {{count}} API keys.', {
          count: ids.length,
        })}
        footer={
          <>
            <Button
              variant='outline'
              disabled={mutation.isPending}
              onClick={props.onClose}
            >
              {t('Cancel')}
            </Button>
            <Button
              type='submit'
              form='admin-key-groups-form'
              disabled={!canSave}
            >
              {t('Save changes')}
            </Button>
          </>
        }
      >
        <div className='mb-5 max-h-32 space-y-1 overflow-y-auto rounded-lg border p-3 text-sm'>
          {props.keys.map((key) => (
            <p key={key.id} className='break-all'>
              <span className='font-medium'>{key.name || `#${key.id}`}</span>
              <span className='text-muted-foreground'>
                {' '}
                · {key.username} #{key.user_id} ·{' '}
                {getSelectedGroups(key).join(' → ')}
              </span>
            </p>
          ))}
        </div>
        {groupsQuery.isLoading && <LoadingState />}
        {groupsQuery.isError && (
          <ErrorState
            title={t('Failed to load groups')}
            onRetry={() => void groupsQuery.refetch()}
          />
        )}
        {groupsQuery.isSuccess && groups.length === 0 && (
          <p role='status' className='text-muted-foreground text-sm'>
            {t('These users have no groups in common.')}
          </p>
        )}
        {groupsQuery.isSuccess && groups.length > 0 && (
          <Form {...form}>
            <form
              id='admin-key-groups-form'
              onSubmit={form.handleSubmit(setPending)}
              className='space-y-5'
              inert={mutation.isPending || undefined}
            >
              <FormField
                control={form.control}
                name='groups'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Selected groups')}</FormLabel>
                    <FormControl>
                      <AutoGroupOrderEditor
                        value={field.value}
                        options={options}
                        maxCount={maxCount}
                        onChange={field.onChange}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              {isMultiGroup && (
                <FormField
                  control={form.control}
                  name='crossGroupRetry'
                  render={({ field }) => (
                    <FormItem className='flex items-center justify-between'>
                      <FormLabel>{t('Cross-group retry')}</FormLabel>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </FormItem>
                  )}
                />
              )}
            </form>
          </Form>
        )}
      </Dialog>
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => {
          if (!open && !mutation.isPending) setPending(null)
        }}
        title={t('Change API key groups')}
        desc={t('Apply these groups to {{count}} API keys: {{groups}}?', {
          count: ids.length,
          groups: pending?.groups.join(' → '),
        })}
        confirmText={t('Save changes')}
        isLoading={mutation.isPending}
        handleConfirm={() => {
          if (pending) mutation.mutate(pending)
        }}
      />
    </>
  )
}
