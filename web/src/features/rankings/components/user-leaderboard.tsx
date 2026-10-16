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
  Coins,
  Pencil,
  Settings2,
  ShieldCheck,
  Sparkles,
  Trophy,
  Users,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
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
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import { formatCompactNumber, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useSystemConfigStore } from '@/stores/system-config-store'

import {
  useUserRankings,
  type ParticipationChoice,
  type RankingMetric,
} from '../hooks/use-user-rankings'
import type { RankingPeriod } from '../types'
import { ModelUsageBar } from './model-usage-bar'
import { ParticipationDialog } from './participation-dialog'
import { RankingNicknameDialog } from './ranking-nickname-dialog'
import { RankingUserName } from './ranking-user-name'
import { RankingValue } from './ranking-value'
import { UserPodium } from './user-podium'

export function UserLeaderboard(props: {
  period: RankingPeriod
  metric?: RankingMetric
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const currency = useSystemConfigStore((state) => state.config.currency)
  const metric = props.metric ?? 'tokens'
  const spending = metric === 'spending'
  const { rankings, preference, participation, nickname, user } =
    useUserRankings(props.period, metric)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [rulesOpen, setRulesOpen] = useState(false)
  const [nicknameOpen, setNicknameOpen] = useState(false)
  const optedIn =
    (spending
      ? preference.data?.spending_participating
      : preference.data?.participating) ?? false
  const publicUsage = spending
    ? preference.data?.spending_usage_public
    : preference.data?.token_usage_public
  const needsChoice = Boolean(
    user && preference.data && !preference.data.prompted
  )
  const snapshot = rankings.data

  const saveChoice = (choice: ParticipationChoice) => {
    participation.mutate(choice, { onSuccess: () => setSettingsOpen(false) })
  }

  return (
    <section className='space-y-5' aria-label={t('User rankings')}>
      <div className='bg-card/80 flex flex-col justify-between gap-4 rounded-2xl border p-4 sm:flex-row sm:items-center sm:px-5'>
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
            <p className='text-muted-foreground mt-1 text-xs sm:text-sm'>
              {optedIn && publicUsage
                ? t('Your usage details are public on this board.')
                : t(
                    'Your model usage stays private until you choose to share it.'
                  )}
            </p>
          </div>
        </div>
        <div className='flex flex-wrap items-center gap-1.5'>
          {user && preference.data && (
            <Button
              variant='ghost'
              size='sm'
              onClick={() => {
                nickname.reset()
                setNicknameOpen(true)
              }}
            >
              <Pencil className='size-3.5' aria-hidden />
              {preference.data.nickname
                ? t('Edit nickname')
                : t('Set nickname')}
            </Button>
          )}
          <Button variant='ghost' size='sm' onClick={() => setRulesOpen(true)}>
            {t('Ranking rules')}
          </Button>
          {!user ? (
            <Button
              size='sm'
              render={<Link to='/sign-in' search={{ redirect: '/rankings' }} />}
            >
              {t('Sign in to join')}
              <ArrowUpRight className='size-4' aria-hidden />
            </Button>
          ) : (
            <Button
              variant='outline'
              size='sm'
              disabled={preference.isLoading || participation.isPending}
              onClick={() => {
                if (preference.isError) void preference.refetch()
                else {
                  participation.reset()
                  setSettingsOpen(true)
                }
              }}
            >
              <Settings2 className='size-3.5' aria-hidden />
              {preference.isError ? t('Retry') : t('Ranking preferences')}
            </Button>
          )}
        </div>
      </div>

      {rankings.isPending && (
        <div className='space-y-5'>
          <Skeleton className='h-24 rounded-2xl' />
          <Skeleton className='h-[600px] rounded-2xl' />
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
          <div className='grid grid-cols-3 gap-2 sm:gap-4'>
            <RankingMetricCard
              icon={<Users className='size-4' />}
              label={t('Participants')}
              value={formatNumber(snapshot.participants, locale)}
            />
            <RankingMetricCard
              icon={
                spending ? (
                  <Coins className='size-4' />
                ) : (
                  <Sparkles className='size-4' />
                )
              }
              label={spending ? t('Public spending') : t('Public tokens')}
              value={
                spending
                  ? formatBillingCurrencyFromUSD(
                      (snapshot.total_quota ?? 0) / currency.quotaPerUnit,
                      { locale, compact: true }
                    )
                  : formatCompactNumber(snapshot.total_tokens, locale)
              }
              help={t(
                'Includes only users who share usage details on this board. Hidden usage still counts toward ranking.'
              )}
            />
            <RankingMetricCard
              icon={<Trophy className='size-4' />}
              label={t('Your ranking')}
              value={snapshot.self ? `#${snapshot.self.rank}` : '—'}
            />
          </div>
          <div className='bg-card overflow-hidden rounded-2xl border'>
            <div className='flex flex-wrap items-center justify-between gap-3 border-b px-4 py-4 sm:px-6'>
              <div className='flex items-center gap-2'>
                <Trophy className='text-primary size-5' aria-hidden />
                <h2 className='font-semibold'>
                  {spending
                    ? t('Spending leaderboard')
                    : t('Token leaderboard')}
                </h2>
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
                  'No participating users have recorded usage in this period yet.'
                )}
              />
            ) : (
              <>
                <UserPodium users={snapshot.users} metric={metric} />
                <Table className='table-fixed'>
                  <TableHeader>
                    <TableRow className='bg-muted/30 hover:bg-muted/30'>
                      <TableHead className='w-12 pl-3 sm:w-20 sm:pl-6 md:w-[8%] md:pl-3 lg:pl-6'>
                        {t('Rank')}
                      </TableHead>
                      <TableHead className='w-auto md:w-[17%]'>
                        {t('User')}
                      </TableHead>
                      <TableHead className='hidden text-center md:table-cell'>
                        {spending
                          ? t('Model spending share')
                          : t('Model token share')}
                      </TableHead>
                      <TableHead className='w-28 pr-3 text-right sm:w-44 sm:pr-6 md:w-1/4'>
                        {spending ? t('Spending') : t('Total tokens')}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {snapshot.users.map((entry) => (
                      <TableRow
                        key={entry.rank}
                        className={cn(
                          'h-[76px]',
                          entry.is_self && 'bg-primary/5 hover:bg-primary/10'
                        )}
                      >
                        <TableCell className='pl-3 sm:pl-6 md:pl-3 lg:pl-6'>
                          <span
                            className={cn(
                              'flex size-7 items-center justify-center rounded-lg font-semibold tabular-nums sm:size-8',
                              entry.rank <= 3
                                ? 'bg-primary/10 text-primary'
                                : 'text-muted-foreground'
                            )}
                          >
                            {entry.rank}
                          </span>
                        </TableCell>
                        <TableCell className='whitespace-normal'>
                          <div className='flex min-w-0 items-center gap-1.5'>
                            <RankingUserName
                              entry={entry}
                              className='min-w-0 truncate font-medium'
                            />
                            {entry.is_self && (
                              <Badge
                                variant='secondary'
                                className='shrink-0 px-1.5'
                              >
                                {t('You')}
                              </Badge>
                            )}
                          </div>
                          <div className='mt-1 md:hidden'>
                            <ModelUsageBar entry={entry} metric={metric} />
                          </div>
                        </TableCell>
                        <TableCell className='hidden px-5 md:table-cell'>
                          <ModelUsageBar entry={entry} metric={metric} />
                        </TableCell>
                        <TableCell className='pr-3 text-right text-xs font-semibold tabular-nums sm:pr-6 sm:text-sm'>
                          <RankingValue entry={entry} metric={metric} />
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
                  <RankingValue entry={snapshot.self} metric={metric} />
                </span>
              </div>
            )}
          </div>
        </>
      )}
      {(settingsOpen || needsChoice) && preference.data && (
        <ParticipationDialog
          preference={preference.data}
          pending={participation.isPending}
          error={participation.error}
          onClose={() => {
            if (needsChoice) {
              saveChoice({
                participating: false,
                spending_participating: false,
                token_usage_public: false,
                spending_usage_public: false,
              })
            } else setSettingsOpen(false)
          }}
          onSave={saveChoice}
        />
      )}
      <Dialog
        open={rulesOpen}
        onOpenChange={setRulesOpen}
        title={t('Ranking rules')}
        contentClassName='sm:max-w-lg'
        footer={
          <Button onClick={() => setRulesOpen(false)}>{t('Got it')}</Button>
        }
      >
        <ul className='text-muted-foreground list-disc space-y-3 pl-5 text-sm leading-relaxed'>
          <li>
            {t(
              'Rankings use total tokens in the selected period, including input and output. Cache hits count as input.'
            )}
          </li>
          <li>
            {t(
              'Today spending starts at midnight in the server timezone. Other periods follow the selected range. Spending uses actual recorded charges.'
            )}
          </li>
          <li>
            {t(
              'Each model segment follows its token or spending share. Hover or focus a segment to view shared details.'
            )}
          </li>
          <li>
            {t(
              'With details hidden, only your name and rank are public. Spending is shared only when you join the spending board and enable its details.'
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
      </Dialog>
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

function RankingMetricCard(props: {
  icon: React.ReactNode
  label: string
  value: string
  help?: string
}) {
  const label = (
    <span className='flex min-w-0 items-center gap-1.5'>
      {props.icon}
      <span className='truncate'>{props.label}</span>
    </span>
  )
  return (
    <div className='bg-card rounded-2xl border px-3 py-3 sm:px-5'>
      <div className='text-muted-foreground text-[11px] sm:text-xs'>
        {props.help ? (
          <Tooltip>
            <TooltipTrigger
              render={<span tabIndex={0} className='cursor-help' />}
            >
              {label}
            </TooltipTrigger>
            <TooltipContent>{props.help}</TooltipContent>
          </Tooltip>
        ) : (
          label
        )}
      </div>
      <div className='mt-2 truncate text-xl font-semibold tracking-tight tabular-nums sm:text-2xl'>
        {props.value}
      </div>
    </div>
  )
}
