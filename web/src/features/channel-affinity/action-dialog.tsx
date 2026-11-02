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
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Combobox } from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { getAffinityTargets } from './api'
import type {
  AffinityActionRequest,
  AffinityBinding,
  AffinityBindingRef,
} from './types'

interface Props {
  action: 'reset' | 'migrate'
  bindings: AffinityBinding[]
  pending: boolean
  onClose: () => void
  onConfirm: (request: AffinityActionRequest) => void
}

export function AffinityActionDialog(props: Props) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [targetId, setTargetId] = useState<string | null>(null)
  const refs: AffinityBindingRef[] = props.bindings.map(
    ({ id, version, channel_id }) => ({ id, version, channel_id })
  )
  const targets = useQuery({
    queryKey: ['channel-affinity', 'targets', refs],
    queryFn: ({ signal }) => getAffinityTargets(refs, signal),
    enabled: props.action === 'migrate',
    refetchOnWindowFocus: false,
    staleTime: 0,
    retry: false,
    meta: { errorToast: false },
  })
  const target = targets.data?.find(
    (channel) => String(channel.id) === targetId
  )
  const modes = new Set(props.bindings.map((binding) => binding.session_mode))

  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open && !props.pending) props.onClose()
      }}
      title={
        props.action === 'reset' ? t('Reset affinity') : t('Migrate affinity')
      }
      desc={t('Selected sessions: {{total}}', {
        total: formatNumber(props.bindings.length, locale),
      })}
      confirmText={
        props.action === 'reset' ? t('Confirm reset') : t('Confirm migration')
      }
      destructive={props.action === 'reset'}
      isLoading={props.pending}
      disabled={
        props.bindings.length === 0 ||
        (props.action === 'migrate' &&
          (!target || targets.isFetching || targets.isError))
      }
      handleConfirm={() =>
        props.onConfirm({
          action: props.action,
          bindings: refs,
          ...(target && props.action === 'migrate'
            ? { target_channel_id: target.id }
            : {}),
        })
      }
    >
      <div className='grid min-w-0 gap-4 text-sm'>
        {props.bindings.some(
          (binding) => binding.shared || !binding.ownership_known
        ) && (
          <p>
            {t(
              'Shared or unattributed bindings are included. This changes the whole binding for everyone using it.'
            )}
          </p>
        )}
        <div className='grid gap-1'>
          <span className='text-muted-foreground'>{t('Current channel')}</span>
          <p className='break-words'>
            {[
              ...new Set(
                props.bindings.map(
                  (binding) => `${binding.channel_name} #${binding.channel_id}`
                )
              ),
            ].join(', ')}
          </p>
        </div>
        {props.action === 'reset' ? (
          <p>
            {t(
              'The next request will select a channel again. It may select the same channel.'
            )}
          </p>
        ) : (
          <div className='grid gap-2'>
            <Label htmlFor='affinity-target-channel'>
              {t('Target channel')}
            </Label>
            {targets.isPending && (
              <LoadingState inline message={t('Loading...')} />
            )}
            {targets.isError && (
              <ErrorState
                className='min-h-24'
                title={t('Could not load eligible channels')}
                onRetry={() => void targets.refetch()}
              />
            )}
            {targets.isSuccess && (
              <>
                <Combobox
                  id='affinity-target-channel'
                  options={(targets.data ?? []).map((channel) => ({
                    value: String(channel.id),
                    label: `${channel.name} #${channel.id}`,
                  }))}
                  value={targetId}
                  onValueChange={setTargetId}
                  disabled={props.pending || targets.data.length === 0}
                  placeholder={t('Select target channel')}
                />
                {targets.data.length === 0 && (
                  <p className='text-muted-foreground'>
                    {t('No channel is eligible for every selected session.')}
                  </p>
                )}
              </>
            )}
            <p className='text-muted-foreground'>
              {t(
                'Applies to the next request. Existing streams continue, and the target channel sets the new binding lifetime.'
              )}
            </p>
            <p className='text-muted-foreground'>
              {t(
                'Switching upstreams may invalidate encrypted conversation history. Start a new conversation if that happens.'
              )}
            </p>
          </div>
        )}
        <p className='text-muted-foreground'>
          {modes.has('strict')
            ? t('Strict sessions keep their no-fallback behavior.')
            : t('Sessions keep their existing fallback behavior.')}
        </p>
      </div>
    </ConfirmDialog>
  )
}
