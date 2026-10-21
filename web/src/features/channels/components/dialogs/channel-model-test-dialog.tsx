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
import { Gauge, Info, Loader2, MessageSquareText } from 'lucide-react'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useChannelTestStore } from '@/stores/channel-test-store'

import { formatResponseTime } from '../../lib/channel-utils'

export type ChannelModelTestResult = {
  status: 'idle' | 'testing' | 'success' | 'error'
  responseTime?: number
  completedAt?: number
  error?: string
  errorCode?: string
  prompt?: string
  responseText?: string
  responseModel?: string
  responseTruncated?: boolean
}

type ChannelModelTestDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelId: number
  channelName: string
  model: string
  result?: ChannelModelTestResult
  disabled: boolean
  onTest: () => void
}

export function ChannelModelTestDialog(props: ChannelModelTestDialogProps) {
  const { t } = useTranslation()
  const promptId = useId()
  const prompt = useChannelTestStore(
    (state) => state.prompts[String(props.channelId)]?.[props.model] ?? ''
  )
  const setPrompt = useChannelTestStore((state) => state.setPrompt)
  const result = props.result
  const isTesting = result?.status === 'testing'
  const responseText = result?.responseText
  const hasResult = result?.status === 'success' || result?.status === 'error'

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Prompt and response')}
      description={`${props.channelName} · ${props.model}`}
      descriptionClassName='break-all'
      contentClassName='sm:max-w-3xl'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Close')}
          </Button>
          <Button onClick={props.onTest} disabled={props.disabled}>
            {isTesting ? <Loader2 className='animate-spin' /> : <Gauge />}
            {isTesting ? t('Testing...') : t('Test Connection')}
          </Button>
        </>
      }
    >
      <FieldGroup>
        <Field data-disabled={props.disabled || undefined}>
          <div className='flex items-center justify-between gap-2'>
            <div className='flex items-center gap-1.5'>
              <FieldLabel htmlFor={promptId}>{t('Test prompt')}</FieldLabel>
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('About test prompts')}
                    />
                  }
                >
                  <Info className='size-3.5' />
                </TooltipTrigger>
                <TooltipContent className='max-w-72'>
                  {t(
                    'Saved in this browser for this channel and model. Leave empty to use the default test prompt.'
                  )}
                </TooltipContent>
              </Tooltip>
            </div>
            <Button
              variant='ghost'
              size='sm'
              disabled={props.disabled || !prompt}
              onClick={() => setPrompt(props.channelId, props.model, '')}
            >
              {t('Use default prompt')}
            </Button>
          </div>
          <Textarea
            id={promptId}
            value={prompt}
            onChange={(event) =>
              setPrompt(props.channelId, props.model, event.target.value)
            }
            maxLength={16000}
            disabled={props.disabled}
            placeholder={t(
              'Enter a test prompt, or leave empty for the default'
            )}
            className='min-h-28 resize-y'
          />
        </Field>
      </FieldGroup>

      {hasResult && (result.prompt ?? '') !== prompt && (
        <section className='space-y-2'>
          <h3 className='text-muted-foreground text-xs font-medium'>
            {t('Prompt used for this response')}
          </h3>
          <pre className='bg-muted/30 max-h-32 overflow-auto rounded-md border p-3 text-xs wrap-anywhere whitespace-pre-wrap'>
            {result.prompt || t('Default test prompt')}
          </pre>
        </section>
      )}

      <section className='space-y-2' aria-label={t('Model response')}>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <div className='flex flex-wrap items-center gap-2'>
            <h3 className='text-sm font-medium'>{t('Model response')}</h3>
            {hasResult && (
              <StatusBadge
                label={result.status === 'success' ? t('Success') : t('Failed')}
                variant={result.status === 'success' ? 'success' : 'danger'}
                copyable={false}
              />
            )}
            {hasResult && typeof result.responseTime === 'number' && (
              <span className='text-muted-foreground text-xs'>
                {formatResponseTime(result.responseTime, t)}
              </span>
            )}
          </div>
          {responseText && (
            <CopyButton value={responseText} tooltip={t('Copy response')} />
          )}
        </div>
        {result?.responseModel && (
          <p className='text-muted-foreground text-xs break-all'>
            {t('Response model identifier')}: {result.responseModel}
          </p>
        )}
        {result?.status === 'error' && (
          <Alert variant='destructive'>
            <AlertTitle>{t('Test failed')}</AlertTitle>
            <AlertDescription className='max-h-32 overflow-auto wrap-anywhere whitespace-pre-wrap'>
              {result.error || t('Test failed')}
              {result.errorCode && ` (${result.errorCode})`}
            </AlertDescription>
          </Alert>
        )}
        {isTesting && (
          <LoadingState
            className='min-h-28'
            message={t('Testing...')}
            size='sm'
          />
        )}
        {!isTesting && responseText && (
          <pre className='bg-muted/30 max-h-[42dvh] min-h-28 overflow-auto rounded-md border p-3 text-sm leading-relaxed wrap-anywhere whitespace-pre-wrap'>
            {responseText}
          </pre>
        )}
        {!isTesting && !responseText && result?.status !== 'error' && (
          <EmptyState
            icon={MessageSquareText}
            className='min-h-28 gap-2 py-5'
            title={hasResult ? t('No text returned') : t('No response yet')}
          />
        )}
        {result?.responseTruncated && (
          <StatusBadge
            label={t('Response truncated')}
            variant='warning'
            copyable={false}
          />
        )}
      </section>
    </Dialog>
  )
}
