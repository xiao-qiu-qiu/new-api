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
import { Coins, Zap } from 'lucide-react'
import { useLayoutEffect, useRef, useState, type Ref } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'

import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { cn } from '@/lib/utils'

import type { RankingMetric } from '../hooks/use-user-rankings'

interface RankingMetricSwitchProps {
  metric: RankingMetric
  onChange: (metric: RankingMetric) => void
}

export function RankingMetricSwitch(props: RankingMetricSwitchProps) {
  const anchor = useRef<HTMLDivElement>(null)
  const inlineControl = useRef<HTMLDivElement>(null)
  const [floating, setFloating] = useState(false)
  const [placement, setPlacement] = useState<{
    left: number
    width: number
  } | null>(null)

  useLayoutEffect(() => {
    const element = anchor.current
    const control = inlineControl.current
    if (!element || !control) return

    const measure = () => {
      const { left, width } = control.getBoundingClientRect()
      setPlacement((previous) => {
        if (previous?.left === left && previous.width === width) return previous
        return { left, width }
      })
    }
    measure()

    // Keep measuring the in-flow control, including font/language changes.
    const resizeObserver = new ResizeObserver(measure)
    resizeObserver.observe(control, { box: 'border-box' })
    resizeObserver.observe(element, { box: 'border-box' })
    if (element.parentElement) resizeObserver.observe(element.parentElement)
    const observer = new IntersectionObserver(
      ([entry]) => {
        measure()
        setFloating(!entry.isIntersecting && entry.boundingClientRect.top < 80)
      },
      { rootMargin: '-80px 0px 0px 0px' }
    )
    observer.observe(element)
    window.addEventListener('resize', measure)
    window.addEventListener('scroll', measure, { passive: true, capture: true })
    return () => {
      observer.disconnect()
      resizeObserver.disconnect()
      window.removeEventListener('resize', measure)
      window.removeEventListener('scroll', measure, true)
    }
  }, [])

  return (
    <>
      {/* An inert copy preserves intrinsic sizing and the original flex layout. */}
      <div
        ref={anchor}
        className='min-h-11 min-w-60'
        style={{ visibility: floating ? 'hidden' : undefined }}
        aria-hidden={floating || undefined}
        inert={floating}
      >
        <RankingMetricControl {...props} ref={inlineControl} />
      </div>
      {floating &&
        placement &&
        createPortal(
          <div className='fixed top-20 z-30' style={placement}>
            <RankingMetricControl {...props} floating />
          </div>,
          document.body
        )}
    </>
  )
}

function RankingMetricControl(
  props: RankingMetricSwitchProps & {
    floating?: boolean
    ref?: Ref<HTMLDivElement>
  }
) {
  const { t } = useTranslation()
  return (
    <ToggleGroup
      ref={props.ref}
      value={[props.metric]}
      onValueChange={(values) => {
        if (values[0]) props.onChange(values[0] as RankingMetric)
      }}
      aria-label={t('User ranking metric')}
      className={cn(
        'bg-muted/80 gap-1 rounded-full p-1',
        props.floating &&
          'bg-background/95 w-full border p-[3px] shadow-lg backdrop-blur-xl'
      )}
      spacing={1}
    >
      <ToggleGroupItem
        value='tokens'
        className='data-pressed:bg-background data-pressed:text-primary h-9 rounded-full px-3 text-xs data-pressed:shadow-sm sm:px-4'
      >
        <Zap className='size-3.5' aria-hidden />
        {t('Token leaderboard')}
      </ToggleGroupItem>
      <ToggleGroupItem
        value='spending'
        className='data-pressed:bg-background data-pressed:text-primary h-9 rounded-full px-3 text-xs data-pressed:shadow-sm sm:px-4'
      >
        <Coins className='size-3.5' aria-hidden />
        {t('Spending leaderboard')}
      </ToggleGroupItem>
    </ToggleGroup>
  )
}
