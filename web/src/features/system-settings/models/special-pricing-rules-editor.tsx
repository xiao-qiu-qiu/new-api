import { Plus } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { formatPricingAmount } from '@/features/model-pricing/currency'

import type { SpecialPricingRule } from '../billing/group-settings-schema'
import { safeJsonParse } from '../utils/json-parser'
import { SpecialPricingRuleDialog } from './special-pricing-rule-dialog'

type SpecialPricingRulesEditorProps = {
  value: string
  userGroupOptions: string[]
  onChange: (value: string) => void
}

export function SpecialPricingRulesEditor(
  props: SpecialPricingRulesEditorProps
) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState<number | 'new' | null>(null)
  const [deleting, setDeleting] = useState<number | null>(null)
  const rules = useMemo(
    () =>
      safeJsonParse<SpecialPricingRule[]>(props.value, {
        fallback: [],
        silent: true,
      }),
    [props.value]
  )
  const write = (next: SpecialPricingRule[]) =>
    props.onChange(JSON.stringify(next, null, 2))

  return (
    <Card className='min-w-0 shadow-none'>
      <CardHeader className='gap-2 border-b'>
        <CardTitle>{t('Special pricing rules')}</CardTitle>
        <CardDescription>
          {t(
            'Set final model prices by user group, across all multiplier groups.'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-5'>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <Button size='sm' onClick={() => setEditing('new')}>
            <Plus className='size-4' aria-hidden='true' />
            {t('Add pricing rule')}
          </Button>
          <span className='text-muted-foreground text-sm'>
            {t('Final prices ignore group multipliers')}
          </span>
        </div>
        <StaticDataTable
          tableProps={{ 'aria-label': t('Special pricing rules') }}
          data={rules}
          getRowKey={(_, index) => index}
          emptyContent={
            <EmptyState
              title={t('No special pricing rules')}
              description={t(
                'Add a user group and model pattern to set a fixed price.'
              )}
            />
          }
          columns={[
            {
              id: 'user_group',
              header: t('User group'),
              cell: (rule) =>
                rule.user_group === '*' ? (
                  t('All users (fallback)')
                ) : (
                  <span className='font-medium'>{rule.user_group}</span>
                ),
            },
            {
              id: 'model',
              header: t('Model pattern'),
              cell: (rule) => (
                <code className='text-sm break-all'>{rule.model}</code>
              ),
            },
            {
              id: 'price',
              header: t('Unit price (USD)'),
              cell: (rule) => (
                <span className='font-medium whitespace-nowrap tabular-nums'>
                  {formatPricingAmount(rule.price)}
                  <span className='text-muted-foreground ml-1 font-normal'>
                    {rule.billing_unit === 'image'
                      ? t('/image')
                      : t('/request')}
                  </span>
                </span>
              ),
            },
            {
              id: 'actions',
              header: <span className='sr-only'>{t('Actions')}</span>,
              cell: (rule, index) => (
                <StaticRowActions
                  editLabel={t('Edit pricing rule for {{model}}', {
                    model: rule.model,
                  })}
                  deleteLabel={t('Delete')}
                  menuLabel={t('Actions for {{model}}', { model: rule.model })}
                  onEdit={() => setEditing(index)}
                  onDelete={() => setDeleting(index)}
                />
              ),
            },
          ]}
        />
      </CardContent>
      {editing !== null && (
        <SpecialPricingRuleDialog
          rule={editing === 'new' ? undefined : rules[editing]}
          userGroupOptions={props.userGroupOptions}
          onClose={() => setEditing(null)}
          onApply={(rule) => {
            if (
              rules.some(
                (item, index) =>
                  index !== editing &&
                  item.user_group === rule.user_group &&
                  item.model === rule.model
              )
            ) {
              return false
            }
            const next = [...rules]
            if (editing === 'new') next.push(rule)
            else next[editing] = rule
            write(next)
            setEditing(null)
            return true
          }}
        />
      )}
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={t('Delete pricing rule?')}
        desc={t(
          'The model will use the next matching rule or its regular price.'
        )}
        confirmText={t('Delete')}
        destructive
        handleConfirm={() => {
          write(rules.filter((_, index) => index !== deleting))
          setDeleting(null)
        }}
      />
    </Card>
  )
}
