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
import { Coins, Eye, EyeOff, Zap } from 'lucide-react'
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
  initialSetup?: boolean
  onClose: () => void
  onSave: (choice: ParticipationChoice) => void
}) {
  const { t } = useTranslation()
  const [choice, setChoice] = useState<ParticipationChoice>({
    participating: props.initialSetup || props.preference.participating,
    spending_participating:
      props.initialSetup || props.preference.spending_participating || false,
    model_details_public:
      props.initialSetup || props.preference.model_details_public || false,
  })
  const boards = [
    {
      id: 'tokens',
      label: t('Token leaderboard'),
      icon: Zap,
      joined: 'participating',
      help: t('Joining shares your token total'),
    },
    {
      id: 'spending',
      label: t('Spending leaderboard'),
      icon: Coins,
      joined: 'spending_participating',
      help: t('Joining shares your spending total'),
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
                  <span className='space-y-1.5'>
                    <span className='block font-semibold'>{board.label}</span>
                    <span
                      id={`join-${board.id}-help`}
                      className='text-muted-foreground block text-xs leading-relaxed font-normal'
                    >
                      {board.help}
                    </span>
                  </span>
                </Label>
                <Switch
                  id={`join-${board.id}`}
                  aria-label={board.label}
                  aria-describedby={`join-${board.id}-help`}
                  checked={joined}
                  disabled={props.pending}
                  onCheckedChange={(checked) =>
                    setChoice((previous) => ({
                      ...previous,
                      [board.joined]: checked,
                    }))
                  }
                />
              </div>
            </div>
          )
        })}
        <div className='flex items-center justify-between gap-4 rounded-xl border p-4'>
          <Label
            htmlFor='model-details'
            className='flex cursor-pointer items-center gap-3'
          >
            <span className='bg-muted text-muted-foreground flex size-9 shrink-0 items-center justify-center rounded-lg'>
              {choice.model_details_public ? (
                <Eye className='size-4' aria-hidden />
              ) : (
                <EyeOff className='size-4' aria-hidden />
              )}
            </span>
            <span className='space-y-1.5'>
              <span className='block font-semibold'>
                {t('Share model breakdown')}
              </span>
              <span
                id='model-details-help'
                className='text-muted-foreground block text-xs leading-relaxed font-normal'
              >
                {t('Applies to both leaderboards')}
              </span>
            </span>
          </Label>
          <Switch
            id='model-details'
            aria-label={t('Share model breakdown')}
            aria-describedby='model-details-help'
            checked={choice.model_details_public}
            disabled={props.pending}
            onCheckedChange={(checked) =>
              setChoice((previous) => ({
                ...previous,
                model_details_public: checked,
              }))
            }
          />
        </div>
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
