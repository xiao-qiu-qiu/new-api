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
import { Reorder } from 'motion/react'
import { useMemo, type ComponentProps } from 'react'
import { useTranslation } from 'react-i18next'

import { AutoGroupOrderItem } from '@/components/auto-group-order-item'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

import {
  ApiKeyGroupCombobox,
  type ApiKeyGroupOption,
} from './api-key-group-combobox'
import { GroupRatioBadge } from './auto-group-visuals'

type AutoGroupOrderEditorProps = Omit<ComponentProps<'div'>, 'onChange'> & {
  value: string[]
  options: ApiKeyGroupOption[]
  maxCount: number
  onChange: (groups: string[]) => void
  'data-slot'?: string
  'data-form-root'?: string
}

export function AutoGroupOrderEditor(props: AutoGroupOrderEditorProps) {
  const { t } = useTranslation()
  const maxCount =
    Number.isInteger(props.maxCount) && props.maxCount > 0 ? props.maxCount : 20
  const atLimit = props.value.length >= maxCount
  const candidates = useMemo(
    () =>
      props.options.filter(
        (option) =>
          option.value !== 'auto' && !props.value.includes(option.value)
      ),
    [props.options, props.value]
  )

  const handleAdd = (group: string) => {
    if (atLimit || props.value.includes(group)) return
    props.onChange([...props.value, group])
  }

  const handleMove = (index: number, direction: 'up' | 'down') => {
    const targetIndex = direction === 'up' ? index - 1 : index + 1
    if (targetIndex < 0 || targetIndex >= props.value.length) return
    const next = [...props.value]
    ;[next[index], next[targetIndex]] = [next[targetIndex], next[index]]
    props.onChange(next)
  }

  return (
    <div
      id={props.id}
      data-slot={props['data-slot']}
      data-form-root={props['data-form-root']}
      role='group'
      tabIndex={-1}
      aria-label={props['aria-label'] || t('Selected groups')}
      aria-describedby={props['aria-describedby']}
      aria-invalid={props['aria-invalid']}
      className={cn('flex flex-col gap-3', props.className)}
    >
      <ApiKeyGroupCombobox
        options={candidates}
        value={undefined}
        onValueChange={handleAdd}
        placeholder={
          atLimit
            ? t('Maximum {{max}} groups selected', { max: maxCount })
            : t('Add group')
        }
        disabled={atLimit || candidates.length === 0}
      />

      {props.value.length > 0 && (
        <Reorder.Group
          axis='y'
          values={props.value}
          onReorder={props.onChange}
          className='flex flex-col gap-2'
        >
          {props.value.map((group, index) => {
            const option = props.options.find((item) => item.value === group)
            return (
              <AutoGroupOrderItem
                key={group}
                group={group}
                index={index}
                count={props.value.length}
                leading={
                  <span
                    className='text-muted-foreground w-4 shrink-0 text-center text-xs tabular-nums'
                    aria-hidden='true'
                  >
                    {index + 1}
                  </span>
                }
                onMove={handleMove}
                onRemove={(removed) =>
                  props.onChange(props.value.filter((item) => item !== removed))
                }
              >
                {option ? (
                  <GroupRatioBadge ratio={option.ratio} />
                ) : (
                  <Badge variant='destructive'>{t('Unavailable')}</Badge>
                )}
              </AutoGroupOrderItem>
            )
          })}
        </Reorder.Group>
      )}
      <p className='text-muted-foreground text-xs' aria-live='polite'>
        {t('{{count}} / {{max}} groups selected', {
          count: props.value.length,
          max: maxCount,
        })}
      </p>
    </div>
  )
}
