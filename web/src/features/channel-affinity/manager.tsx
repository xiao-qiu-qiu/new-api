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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowRightLeft, RefreshCw, RotateCcw } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { CacheStatsDialog } from '@/features/system-settings/general/channel-affinity/cache-stats-dialog'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { AffinityActionDialog } from './action-dialog'
import { applyAffinityAction, getAffinityBindings } from './api'
import { BindingFilters, type BindingFilterValues } from './binding-filters'
import { BindingsTable } from './bindings-table'
import type {
  AffinityActionResult,
  AffinityBinding,
  AffinityBindingFilters,
  AffinityLogTarget,
} from './types'

interface Props {
  page?: boolean
  onOpenChange?: (open: boolean) => void
  channelId?: number
  channelName?: string
  target?: AffinityLogTarget
}

type Scope = 'session' | 'token' | 'user'
type PendingAction = {
  action: 'reset' | 'migrate'
  bindings: AffinityBinding[]
}

export function AffinityManager(props: Props) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const queryClient = useQueryClient()
  const [scope, setScope] = useState<Scope>('session')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [filters, setFilters] = useState<BindingFilterValues>({
    channel_id: '',
    user_id: '',
    token_id: '',
    model: '',
  })
  const [selection, setSelection] = useState<AffinityBinding[] | null>(null)
  const [pendingAction, setPendingAction] = useState<PendingAction | null>(null)
  const [result, setResult] = useState<AffinityActionResult | null>(null)
  const [statsOpen, setStatsOpen] = useState(false)
  const target = props.target
  const params: AffinityBindingFilters = { page, page_size: pageSize }
  if (props.channelId) params.channel_id = props.channelId
  if (target) {
    if (scope === 'session') {
      if (target.binding_id) params.binding_id = target.binding_id
      else {
        params.rule_name = target.rule_name
        if (target.using_group) params.using_group = target.using_group
        params.key_fp = target.key_fp
        if (target.user_id) params.user_id = target.user_id
        if (target.token_id) params.token_id = target.token_id
      }
    } else {
      params.user_id = target.user_id
      if (scope === 'token') params.token_id = target.token_id
    }
  } else {
    if (props.page && filters.channel_id) {
      params.channel_id = Number(filters.channel_id)
    }
    if (filters.user_id) params.user_id = Number(filters.user_id)
    if (filters.token_id) params.token_id = Number(filters.token_id)
    if (filters.model) params.model = filters.model
  }
  const canLookup =
    !target ||
    scope !== 'session' ||
    !!target.binding_id ||
    !!(target.rule_name && target.key_fp)
  const query = useQuery({
    queryKey: ['channel-affinity', 'bindings', params],
    queryFn: ({ signal }) => getAffinityBindings(params, signal),
    enabled: canLookup,
    refetchInterval: pendingAction || !params.binding_id ? false : 15000,
    refetchOnWindowFocus: !pendingAction,
    meta: { errorToast: false },
  })
  const bindings = query.data?.items ?? []
  let selected = selection ?? []
  if (
    selection === null &&
    target &&
    scope === 'session' &&
    bindings.length === 1 &&
    !bindings[0].shared &&
    bindings[0].user_id > 0 &&
    bindings[0].token_id > 0
  ) {
    selected = bindings
  }
  const mutation = useMutation({
    mutationFn: applyAffinityAction,
    retry: false,
    onSuccess: (data) => {
      setResult(data)
      setPendingAction(null)
      setSelection([])
      setPage(1)
      void queryClient.invalidateQueries({
        queryKey: ['channel-affinity', 'bindings'],
      })
    },
    onError: (error) => handleServerError(error),
  })
  const total = query.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / params.page_size))
  const failedMessages = [
    ...new Set(
      result?.items.flatMap((item) =>
        item.status !== 'succeeded' && item.status !== 'success' && item.message
          ? [item.message]
          : []
      ) ?? []
    ),
  ]
  let content: ReactNode
  if (!canLookup || (query.isSuccess && bindings.length === 0)) {
    content = (
      <EmptyState
        title={t('No active bindings found')}
        description={t(
          'Older bindings appear after their next request. Expired bindings cannot be changed.'
        )}
        className='min-h-40'
      />
    )
  } else if (query.isPending) {
    content = <LoadingState />
  } else if (query.isError) {
    content = (
      <ErrorState
        title={t('Could not load session bindings')}
        description={getServerErrorMessage(query.error)}
        onRetry={() => void query.refetch()}
      />
    )
  } else {
    content = (
      <BindingsTable
        bindings={bindings}
        selected={selected}
        disabled={!canOperate || mutation.isPending || query.isFetching}
        onSelect={setSelection}
      />
    )
  }

  const refreshButton = (
    <Button
      variant='outline'
      size='sm'
      aria-label={t('Refresh bindings')}
      disabled={query.isFetching || mutation.isPending || !canLookup}
      onClick={() => void query.refetch()}
    >
      <RefreshCw
        aria-hidden='true'
        className={
          query.isFetching ? 'animate-spin motion-reduce:animate-none' : ''
        }
      />
      {t('Refresh')}
    </Button>
  )
  const actions = (
    <div className='flex w-full flex-wrap items-center justify-between gap-3'>
      <span className='text-muted-foreground text-sm'>
        {t('Selected sessions: {{total}}', {
          total: formatNumber(selected.length, locale),
        })}
      </span>
      <div className='flex flex-wrap gap-2'>
        {props.page && refreshButton}
        <Button
          variant='outline'
          disabled={
            !canOperate ||
            selected.length === 0 ||
            query.isFetching ||
            mutation.isPending
          }
          onClick={() =>
            setPendingAction({ action: 'reset', bindings: selected })
          }
        >
          <RotateCcw aria-hidden='true' />
          {t('Reset affinity')}
        </Button>
        <Button
          disabled={
            !canOperate ||
            selected.length === 0 ||
            query.isFetching ||
            mutation.isPending
          }
          onClick={() =>
            setPendingAction({ action: 'migrate', bindings: selected })
          }
        >
          <ArrowRightLeft aria-hidden='true' />
          {t('Migrate affinity')}
        </Button>
      </div>
    </div>
  )
  const body = (
    <div className='min-w-0 space-y-4'>
      {!props.page && (
        <div className='flex flex-wrap items-end justify-between gap-3'>
          {target ? (
            <div className='grid min-w-48 gap-1.5'>
              <Label htmlFor='affinity-scope'>{t('Operation scope')}</Label>
              <NativeSelect
                id='affinity-scope'
                value={scope}
                disabled={mutation.isPending}
                onChange={(event) => {
                  setScope(event.target.value as Scope)
                  setSelection(null)
                  setPage(1)
                  setResult(null)
                }}
              >
                <NativeSelectOption value='session'>
                  {t('Current session')}
                </NativeSelectOption>
                {!!target.user_id && !!target.token_id && (
                  <NativeSelectOption value='token'>
                    {t("This token's active sessions")}
                  </NativeSelectOption>
                )}
                {!!target.user_id && (
                  <NativeSelectOption value='user'>
                    {t("This user's active sessions")}
                  </NativeSelectOption>
                )}
              </NativeSelect>
            </div>
          ) : (
            <span className='font-medium'>{t('Active session bindings')}</span>
          )}
          <div className='flex gap-2'>
            {!!target?.rule_name && !!target.key_fp && (
              <Button variant='ghost' onClick={() => setStatsOpen(true)}>
                {t('Cache Statistics')}
              </Button>
            )}
            {refreshButton}
          </div>
        </div>
      )}
      {!target && (
        <BindingFilters
          showChannel={props.page}
          disabled={mutation.isPending}
          onApply={(values) => {
            setFilters(values)
            setPage(1)
            setSelection([])
            setResult(null)
          }}
        />
      )}
      {result && (
        <Alert role='status'>
          <AlertDescription>
            <p>
              {t(
                'Updated: {{succeeded}} · Expired: {{expired}} · Conflicts: {{conflicted}} · Failed: {{failed}}',
                {
                  succeeded: formatNumber(result.succeeded, locale),
                  expired: formatNumber(result.expired, locale),
                  conflicted: formatNumber(result.conflicted, locale),
                  failed: formatNumber(result.failed, locale),
                }
              )}
            </p>
            {failedMessages.length > 0 && (
              <ul className='mt-2 list-inside list-disc'>
                {failedMessages.map((message) => (
                  <li key={message}>{t(message)}</li>
                ))}
              </ul>
            )}
          </AlertDescription>
        </Alert>
      )}
      {content}
      {bindings.some(
        (binding) => binding.shared || !binding.ownership_known
      ) && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'Shared and unattributed bindings are excluded from owner filters and select-all. Select them individually to change the whole binding.'
          )}
        </p>
      )}
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <span className='text-muted-foreground text-sm'>
          {t('Active bindings: {{total}}', {
            total: formatNumber(total, locale),
          })}
        </span>
        <div className='flex flex-wrap items-center gap-2'>
          {props.page && (
            <>
              <Label htmlFor='affinity-page-size'>{t('Rows per page')}</Label>
              <NativeSelect
                id='affinity-page-size'
                value={pageSize}
                disabled={query.isFetching || mutation.isPending}
                onChange={(event) => {
                  setPageSize(Number(event.target.value))
                  setPage(1)
                  setSelection([])
                }}
              >
                {[20, 50, 100, 200].map((size) => (
                  <NativeSelectOption key={size} value={size}>
                    {formatNumber(size, locale)}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </>
          )}
          <Button
            variant='outline'
            size='sm'
            disabled={page <= 1 || query.isFetching || mutation.isPending}
            onClick={() => {
              setPage(page - 1)
              setSelection([])
            }}
          >
            {t('Previous')}
          </Button>
          <span className='text-muted-foreground text-sm tabular-nums'>
            {formatNumber(page, locale)} / {formatNumber(pages, locale)}
          </span>
          <Button
            variant='outline'
            size='sm'
            disabled={page >= pages || query.isFetching || mutation.isPending}
            onClick={() => {
              setPage(page + 1)
              setSelection([])
            }}
          >
            {t('Next')}
          </Button>
        </div>
      </div>
    </div>
  )

  return (
    <>
      {props.page ? (
        <SectionPageLayout stackActionsOnMobile>
          <SectionPageLayout.Title>
            {t('Affinity sessions')}
          </SectionPageLayout.Title>
          <SectionPageLayout.Actions>{actions}</SectionPageLayout.Actions>
          <SectionPageLayout.Content>{body}</SectionPageLayout.Content>
        </SectionPageLayout>
      ) : (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!mutation.isPending) props.onOpenChange?.(open)
          }}
          title={
            <span className='flex flex-wrap items-center gap-2'>
              {t('Manage affinity')}
              {props.channelId && (
                <Badge
                  variant='secondary'
                  className='max-w-full break-words whitespace-normal'
                >
                  {props.channelName} #{props.channelId}
                </Badge>
              )}
            </span>
          }
          contentClassName='sm:max-w-5xl'
          footer={actions}
        >
          {body}
        </Dialog>
      )}
      {pendingAction && (
        <AffinityActionDialog
          action={pendingAction.action}
          bindings={pendingAction.bindings}
          pending={mutation.isPending}
          onClose={() => setPendingAction(null)}
          onConfirm={(request) => mutation.mutate(request)}
        />
      )}
      {statsOpen && target && (
        <CacheStatsDialog
          open
          onOpenChange={setStatsOpen}
          target={{
            rule_name: target.rule_name || '',
            using_group: target.using_group || '',
            key_fp: target.key_fp || '',
            key_hint: '',
          }}
        />
      )}
    </>
  )
}
