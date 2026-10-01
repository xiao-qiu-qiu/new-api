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
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'

export function ParticipationDialog(props: {
  open: boolean
  leaving: boolean
  participating: boolean
  pending: boolean
  onClose: () => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  let confirmText = t('Join rankings')
  if (props.leaving) confirmText = t('Leave rankings')
  else if (props.participating) confirmText = t('Got it')

  return (
    <ConfirmDialog
      open={props.open}
      onOpenChange={(open) => {
        if (!open && !props.pending) props.onClose()
      }}
      title={
        props.leaving
          ? t('Leave the usage rankings?')
          : t('Participate in usage rankings?')
      }
      confirmText={confirmText}
      cancelBtnText={
        props.leaving || props.participating ? t('Cancel') : t('Not now')
      }
      destructive={props.leaving}
      isLoading={props.pending}
      handleConfirm={props.onConfirm}
      className='sm:max-w-lg'
      desc={
        props.leaving ? (
          t(
            'Your entry will be removed immediately. You can join again at any time.'
          )
        ) : (
          <div className='space-y-4 leading-relaxed'>
            <p>
              {t(
                'After joining, your usage will appear on the public leaderboard. Here is how it works:'
              )}
            </p>
            <ul className='list-disc space-y-3 pl-5'>
              <li>
                {t(
                  'Only token usage is shown. The models you use are never displayed.'
                )}
              </li>
              <li>
                {t(
                  'Rankings use total tokens in the selected period, including input and output. Cache hits count as input.'
                )}
              </li>
              <li>
                {t(
                  'Your nickname is shown publicly. Without one, your username is masked. Hover over a nickname to see the masked username.'
                )}
              </li>
              <li>
                {t(
                  'You can leave at any time and your entry will be removed immediately.'
                )}
              </li>
            </ul>
          </div>
        )
      }
    />
  )
}
