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
import { GaugeIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  PromptInputModelSelect,
  PromptInputModelSelectContent,
  PromptInputModelSelectItem,
  PromptInputModelSelectTrigger,
  PromptInputModelSelectValue,
} from '@/components/ai-elements/prompt-input'
import { supportsOpenAITierPolicy } from '@/lib/openai-service-tier'

import type { PlaygroundServiceTier } from '../../types'

export function PlaygroundSpeedTierSelect(props: {
  model: string
  value: PlaygroundServiceTier
  onChange: (value: PlaygroundServiceTier) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  if (!supportsOpenAITierPolicy(props.model)) return null

  const options: { value: PlaygroundServiceTier; label: string }[] = [
    { value: 'default', label: t('Standard') },
    { value: 'priority', label: t('Fast tier') },
    { value: 'ultrafast', label: t('Ultrafast tier') },
    { value: 'flex', label: t('Flex tier') },
  ]

  return (
    <PromptInputModelSelect
      items={options}
      value={props.value}
      disabled={props.disabled}
      onValueChange={(value) => {
        const selected = options.find((option) => option.value === value)
        if (selected) props.onChange(selected.value)
      }}
    >
      <PromptInputModelSelectTrigger
        aria-label={t('GPT speed tier')}
        className='border-border/60 bg-background text-foreground h-8 max-w-36 shrink-0 border border-solid px-2.5 text-xs'
      >
        <GaugeIcon
          className='text-muted-foreground size-3.5'
          aria-hidden='true'
        />
        <PromptInputModelSelectValue />
      </PromptInputModelSelectTrigger>
      <PromptInputModelSelectContent
        align='end'
        alignItemWithTrigger={false}
        className='min-w-40'
      >
        {options.map((option) => (
          <PromptInputModelSelectItem key={option.value} value={option.value}>
            {option.label}
          </PromptInputModelSelectItem>
        ))}
      </PromptInputModelSelectContent>
    </PromptInputModelSelect>
  )
}
