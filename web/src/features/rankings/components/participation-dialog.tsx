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
import { Coins, Eye, EyeOff, ShieldCheck, Zap } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import type {
  Participation,
  ParticipationChoice,
} from '../hooks/use-user-rankings'

export function ParticipationDialog(props: {
  preference: Participation
  pending: boolean
  error: Error | null
  onClose: () => void
  onSave: (choice: ParticipationChoice) => void
}) {
  const { t } = useTranslation()
  const [choice, setChoice] = useState<ParticipationChoice>({
    participating: props.preference.participating,
    spending_participating: props.preference.spending_participating ?? false,
    token_usage_public: props.preference.token_usage_public ?? false,
    spending_usage_public: props.preference.spending_usage_public ?? false,
  })
  const boards = [
    {
      id: 'tokens',
      label: t('Token leaderboard'),
      icon: Zap,
      joined: 'participating',
      visible: 'token_usage_public',
      help: t('Show token totals and model breakdown'),
    },
    {
      id: 'spending',
      label: t('Spending leaderboard'),
      icon: Coins,
      joined: 'spending_participating',
      visible: 'spending_usage_public',
      help: t('Show spending totals and model breakdown'),
    },
  ] as const

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !props.pending) props.onClose()
      }}
      title={t('Your ranking preferences')}
      description={t(
        'Choose your boards and what you share. You can change these settings at any time.'
      )}
      contentClassName='sm:max-w-lg'
      footer={
        <>
          <Button
            variant='outline'
            disabled={props.pending}
            onClick={props.onClose}
          >
            {props.preference.prompted ? t('Cancel') : t('Not now')}
          </Button>
          <Button disabled={props.pending} onClick={() => props.onSave(choice)}>
            {props.pending ? t('Saving...') : t('Save preferences')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        {boards.map((board) => {
          const joined = choice[board.joined]
          const visible = choice[board.visible]
          return (
            <div
              key={board.id}
              className={cn(
                'overflow-hidden rounded-xl border transition-colors',
                joined && 'border-primary/30 bg-primary/[0.025]'
              )}
            >
              <div className='flex items-center justify-between gap-4 p-4'>
                <Label
                  htmlFor={`join-${board.id}`}
                  className='flex cursor-pointer items-center gap-3'
                >
                  <span className='bg-muted text-foreground flex size-9 items-center justify-center rounded-lg'>
                    <board.icon className='size-4' aria-hidden />
                  </span>
                  <span className='font-semibold'>{board.label}</span>
                </Label>
                <Switch
                  id={`join-${board.id}`}
                  checked={joined}
                  disabled={props.pending}
                  onCheckedChange={(checked) =>
                    setChoice((previous) => ({
                      ...previous,
                      [board.joined]: checked,
                      [board.visible]: checked && previous[board.visible],
                    }))
                  }
                />
              </div>
              <div
                className={cn(
                  'flex items-center justify-between gap-4 border-t px-4 py-3',
                  !joined && 'opacity-50'
                )}
              >
                <Label
                  htmlFor={`usage-${board.id}`}
                  className='flex cursor-pointer items-center gap-2 text-sm font-normal'
                >
                  {visible && joined ? (
                    <Eye className='text-primary size-4' aria-hidden />
                  ) : (
                    <EyeOff
                      className='text-muted-foreground size-4'
                      aria-hidden
                    />
                  )}
                  {board.help}
                </Label>
                <Switch
                  id={`usage-${board.id}`}
                  checked={visible && joined}
                  disabled={!joined || props.pending}
                  onCheckedChange={(checked) =>
                    setChoice((previous) => ({
                      ...previous,
                      [board.visible]: checked,
                    }))
                  }
                />
              </div>
            </div>
          )
        })}
        <p className='text-muted-foreground flex items-start gap-2 px-1 pt-1 text-xs leading-relaxed'>
          <ShieldCheck className='mt-0.5 size-4 shrink-0' aria-hidden />
          {t(
            'With details hidden, only your name and rank are public. Spending is shared only when you join the spending board and enable its details.'
          )}
        </p>
        {props.error && (
          <p role='alert' className='text-destructive text-sm'>
            {getServerErrorMessage(
              props.error,
              t('Could not save ranking preference')
            )}
          </p>
        )}
      </div>
    </Dialog>
  )
}
