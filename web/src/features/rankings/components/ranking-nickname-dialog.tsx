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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { formatTimestamp } from '@/lib/format'
import { getServerErrorMessage } from '@/lib/server-error-message'

import type { Participation } from '../hooks/use-user-rankings'

export function RankingNicknameDialog(props: {
  preference: Participation
  pending: boolean
  error: Error | null
  onClose: () => void
  onSave: (nickname: string) => void
}) {
  const { t } = useTranslation()
  const [value, setValue] = useState(props.preference.nickname ?? '')
  const nickname = value.trim()
  const locked =
    (props.preference.next_nickname_change_at ?? 0) > Date.now() / 1000
  const tooLong = [...nickname].length > 24
  const unchanged = nickname === (props.preference.nickname ?? '')

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !props.pending) props.onClose()
      }}
      title={t('Ranking nickname')}
      description={t(
        'Your nickname is public. You can change it once every 30 days, including clearing it.'
      )}
      contentClassName='sm:max-w-md'
      footer={
        <>
          <Button
            variant='outline'
            disabled={props.pending}
            onClick={props.onClose}
          >
            {t('Cancel')}
          </Button>
          <Button
            type='submit'
            form='ranking-nickname-form'
            disabled={props.pending || locked || tooLong || unchanged}
          >
            {props.pending ? t('Saving...') : t('Save')}
          </Button>
        </>
      }
    >
      <form
        id='ranking-nickname-form'
        className='flex flex-col gap-3'
        onSubmit={(event) => {
          event.preventDefault()
          if (!props.pending && !locked && !tooLong && !unchanged) {
            props.onSave(nickname)
          }
        }}
      >
        <Label htmlFor='ranking-nickname'>{t('Nickname')}</Label>
        <Input
          id='ranking-nickname'
          value={value}
          onChange={(event) => setValue(event.target.value)}
          disabled={props.pending || locked}
          placeholder={t('Leave blank to use your masked username')}
          aria-invalid={tooLong}
          aria-describedby='ranking-nickname-help'
        />
        <p id='ranking-nickname-help' className='text-muted-foreground text-xs'>
          {locked
            ? t('Next nickname change: {{time}}', {
                time: formatTimestamp(
                  props.preference.next_nickname_change_at ?? 0
                ),
              })
            : t(
                'Up to 24 characters. Leave blank to show your masked username.'
              )}
        </p>
        {tooLong && (
          <p role='alert' className='text-destructive text-sm'>
            {t('Please use at most 24 characters.')}
          </p>
        )}
        {props.error && (
          <p role='alert' className='text-destructive text-sm'>
            {getServerErrorMessage(
              props.error,
              t('Could not save ranking preference')
            )}
          </p>
        )}
      </form>
    </Dialog>
  )
}
