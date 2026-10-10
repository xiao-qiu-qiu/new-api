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
import { Link } from '@tanstack/react-router'
import {
  ArrowUpRight,
  Pencil,
  ShieldCheck,
  Sparkles,
  Trophy,
  Users,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatCompactNumber, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { useUserRankings } from '../hooks/use-user-rankings'
import type { RankingPeriod } from '../types'
import { ParticipationDialog } from './participation-dialog'
import { RankingNicknameDialog } from './ranking-nickname-dialog'
import { RankingUserName } from './ranking-user-name'
import { UserPodium } from './user-podium'

export function UserLeaderboard(props: { period: RankingPeriod }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { rankings, preference, participation, nickname, user } =
    useUserRankings(props.period)
  const [dialog, setDialog] = useState<'join' | 'rules' | 'leave' | null>(null)
  const [nicknameOpen, setNicknameOpen] = useState(false)
  const optedIn = preference.data?.participating ?? false
  const needsChoice = Boolean(
    user && preference.data && !preference.data.prompted
  )
  const snapshot = rankings.data
  let actionText = optedIn ? t('Leave rankings') : t('Join rankings')
  if (preference.isError) actionText = t('Retry')

  const saveChoice = (value: boolean) => {
    participation.mutate(value, { onSuccess: () => setDialog(null) })
  }

  return (
    <section className='space-y-6' aria-label={t('User rankings')}>
      <div className='bg-card/80 flex flex-col justify-between gap-4 rounded-2xl border p-5 sm:flex-row sm:items-center'>
        <div className='flex items-center gap-3'>
          <div className='bg-primary/10 text-primary flex size-10 shrink-0 items-center justify-center rounded-xl'>
            <ShieldCheck className='size-5' aria-hidden />
          </div>
          <div>
            <div className='flex flex-wrap items-center gap-2'>
              <span className='font-medium'>
                {t('Your usage, your choice')}
              </span>
              {user && preference.data && (
                <Badge variant='secondary'>
                  {optedIn ? t('Participating') : t('Viewing only')}
                </Badge>
              )}
            </div>
            <p className='text-muted-foreground mt-1 text-sm'>
              {t('Your nickname, your tokens. Models stay private.')}
            </p>
          </div>
        </div>
        <div className='flex flex-wrap items-center gap-2'>
          {user && preference.data && (
            <Button
              variant='outline'
              onClick={() => {
                nickname.reset()
                setNicknameOpen(true)
              }}
            >
              <Pencil className='size-4' aria-hidden />
              {preference.data.nickname
                ? t('Edit nickname')
                : t('Set nickname')}
            </Button>
          )}
          <Button variant='ghost' onClick={() => setDialog('rules')}>
            {t('Ranking rules')}
          </Button>
          {!user ? (
            <Button
              render={<Link to='/sign-in' search={{ redirect: '/rankings' }} />}
            >
              {t('Sign in to join')}
              <ArrowUpRight className='size-4' />
            </Button>
          ) : (
            <Button
              variant={optedIn ? 'outline' : 'default'}
              disabled={preference.isLoading || participation.isPending}
              onClick={() => {
                if (preference.isError) void preference.refetch()
                else setDialog(optedIn ? 'leave' : 'join')
              }}
            >
              {actionText}
            </Button>
          )}
        </div>
      </div>

      {rankings.isPending && (
        <div className='space-y-5'>
          <Skeleton className='h-28 rounded-2xl' />
          <Skeleton className='h-96 rounded-2xl' />
        </div>
      )}
      {rankings.isError && (
        <ErrorState
          title={t('Unable to load rankings')}
          onRetry={() => {
            void rankings.refetch()
          }}
        />
      )}
      {!rankings.isPending && !rankings.isError && snapshot && (
        <>
          <div className='grid gap-4 sm:grid-cols-3'>
            <RankingMetric
              icon={<Users className='size-4' />}
              label={t('Participants')}
              value={formatNumber(snapshot.participants, locale)}
            />
            <RankingMetric
              icon={<Sparkles className='size-4' />}
              label={t('Community tokens')}
              value={formatCompactNumber(snapshot.total_tokens, locale)}
            />
            <RankingMetric
              icon={<Trophy className='size-4' />}
              label={t('Your ranking')}
              value={snapshot.self ? `#${snapshot.self.rank}` : '—'}
            />
          </div>

          <div className='bg-card overflow-hidden rounded-2xl border'>
            <div className='flex flex-wrap items-center justify-between gap-3 border-b px-5 py-5 sm:px-6'>
              <div className='flex items-center gap-2'>
                <Trophy className='text-primary size-5' aria-hidden />
                <h2 className='font-semibold'>{t('Token leaderboard')}</h2>
              </div>
              <Tooltip>
                <TooltipTrigger
                  render={<Badge variant='outline' className='cursor-help' />}
                >
                  <span className='mr-1.5 size-1.5 rounded-full bg-emerald-500' />
                  {t('Updates every minute')}
                </TooltipTrigger>
                <TooltipContent>
                  {t(
                    'Totals follow retained usage logs. Very short names are fully masked.'
                  )}
                </TooltipContent>
              </Tooltip>
            </div>
            {snapshot.users.length === 0 ? (
              <EmptyState
                icon={Trophy}
                title={t('A new leaderboard starts with you')}
                description={t(
                  'No participating users have recorded tokens in this period yet.'
                )}
              />
            ) : (
              <>
                <UserPodium users={snapshot.users} />
                <Table>
                  <TableHeader>
                    <TableRow className='bg-muted/30 hover:bg-muted/30'>
                      <TableHead className='w-20 pl-5 sm:pl-6'>
                        {t('Rank')}
                      </TableHead>
                      <TableHead>{t('User')}</TableHead>
                      <TableHead className='pr-5 text-right sm:pr-6'>
                        {t('Total tokens')}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {snapshot.users.map((entry) => (
                      <TableRow
                        key={entry.rank}
                        className={cn(
                          'h-[72px]',
                          entry.is_self && 'bg-primary/5 hover:bg-primary/10'
                        )}
                      >
                        <TableCell className='pl-5 sm:pl-6'>
                          <span
                            className={cn(
                              'flex size-8 items-center justify-center rounded-lg font-semibold tabular-nums',
                              entry.rank <= 3
                                ? 'bg-primary/10 text-primary'
                                : 'text-muted-foreground'
                            )}
                          >
                            {entry.rank}
                          </span>
                        </TableCell>
                        <TableCell>
                          <div className='flex items-center gap-2'>
                            <RankingUserName
                              entry={entry}
                              className='max-w-40 font-medium sm:max-w-72'
                            />
                            {entry.is_self && (
                              <Badge variant='secondary'>{t('You')}</Badge>
                            )}
                          </div>
                        </TableCell>
                        <TableCell className='pr-5 text-right sm:pr-6'>
                          <div className='font-semibold tabular-nums'>
                            {formatNumber(entry.total_tokens, locale)}
                          </div>
                          <div className='bg-muted mt-2 ml-auto h-1 w-16 overflow-hidden rounded-full sm:w-28'>
                            <div
                              className='bg-primary/50 h-full rounded-full'
                              style={{
                                width: `${Math.max(2, (entry.total_tokens / snapshot.users[0].total_tokens) * 100)}%`,
                              }}
                            />
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </>
            )}
            {snapshot.self && snapshot.self.rank > 100 && (
              <div className='bg-primary/5 flex items-center justify-between border-t px-6 py-4 text-sm'>
                <span>
                  {t('Your ranking')} <strong>#{snapshot.self.rank}</strong>
                </span>
                <span className='tabular-nums'>
                  {formatNumber(snapshot.self.total_tokens, locale)} Tokens
                </span>
              </div>
            )}
          </div>
        </>
      )}
      <ParticipationDialog
        open={dialog !== null || needsChoice}
        leaving={dialog === 'leave'}
        participating={optedIn}
        pending={participation.isPending}
        onClose={() => {
          if (needsChoice) saveChoice(false)
          else setDialog(null)
        }}
        onConfirm={() => {
          if (!user || (dialog === 'rules' && optedIn)) setDialog(null)
          else saveChoice(dialog !== 'leave')
        }}
      />
      {nicknameOpen && preference.data && (
        <RankingNicknameDialog
          preference={preference.data}
          pending={nickname.isPending}
          error={nickname.error}
          onClose={() => setNicknameOpen(false)}
          onSave={(value) =>
            nickname.mutate(value, { onSuccess: () => setNicknameOpen(false) })
          }
        />
      )}
    </section>
  )
}

function RankingMetric(props: {
  icon: React.ReactNode
  label: string
  value: string
}) {
  return (
    <div className='bg-card rounded-2xl border p-5 sm:p-6'>
      <div className='text-muted-foreground flex items-center gap-2 text-sm'>
        {props.icon}
        {props.label}
      </div>
      <div className='mt-3 text-3xl font-semibold tracking-tight tabular-nums'>
        {props.value}
      </div>
    </div>
  )
}
