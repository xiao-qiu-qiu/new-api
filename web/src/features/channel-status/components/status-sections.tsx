import { Layers3, Radio } from 'lucide-react'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

import type { PublicChannelStatus } from '../types'
import { StatusCard } from './status-card'

function StatusSection(props: { probes: PublicChannelStatus[] }) {
  const { t } = useTranslation()
  const headingId = useId()
  const first = props.probes[0]
  const isGroup = first.scope === 'group'
  const Icon = isGroup ? Layers3 : Radio

  return (
    <Card
      role='region'
      aria-labelledby={headingId}
      className='bg-card/80 min-w-0 gap-0 rounded-2xl py-0 shadow-xs'
    >
      <CardHeader className='border-border/70 bg-muted/30 flex min-w-0 items-center justify-between gap-3 border-b px-4 py-3 sm:px-6'>
        <CardTitle className='flex min-w-0 items-center gap-2'>
          <Icon
            className='text-muted-foreground size-4 shrink-0'
            aria-hidden='true'
          />
          <h2
            id={headingId}
            className='min-w-0 truncate font-semibold'
            title={first.public_name}
          >
            {first.public_name}
          </h2>
        </CardTitle>
        <span className='text-muted-foreground shrink-0 text-xs'>
          {isGroup ? t('Group probe') : t('Upstream probe')}
        </span>
      </CardHeader>
      <CardContent className='divide-border/60 divide-y px-0'>
        {props.probes.map((probe, modelIndex) => (
          <StatusCard
            key={JSON.stringify([probe.probe_id ?? modelIndex, probe.model])}
            probe={probe}
            sectionName={first.public_name}
          />
        ))}
      </CardContent>
    </Card>
  )
}

export function StatusSections(props: { probes: PublicChannelStatus[] }) {
  const sections = new Map<number | string, PublicChannelStatus[]>()
  for (const probe of props.probes) {
    const sectionKey =
      probe.section_id ??
      probe.probe_id ??
      JSON.stringify([probe.scope, probe.public_name])
    const section = sections.get(sectionKey)
    if (section) section.push(probe)
    else sections.set(sectionKey, [probe])
  }

  return (
    <div className='space-y-4'>
      {[...sections].map(([sectionKey, probes]) => (
        <StatusSection key={sectionKey} probes={probes} />
      ))}
    </div>
  )
}
