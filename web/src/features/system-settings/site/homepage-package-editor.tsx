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
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { JsonCodeEditor } from '@/components/json-code-editor'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'

const draftSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    description: z.string().optional(),
    kind: z.enum(['builtin', 'html', 'url']),
    layout: z.enum(['content', 'full']),
    html: z.string().optional(),
    css: z.string().optional(),
    javascript: z.string().optional(),
    url: z.string().optional(),
  })
  .passthrough()

export function HomepagePackageEditor(props: {
  value: string
  onChange: (value: string) => void
  disabled: boolean
}) {
  const { t } = useTranslation()
  let value: unknown
  try {
    value = JSON.parse(props.value)
  } catch {
    value = null
  }
  const parsed = draftSchema.safeParse(value)
  const pack = parsed.success ? parsed.data : null
  const update = (key: string, next: unknown) =>
    props.onChange(JSON.stringify({ ...pack, [key]: next }, null, 2))

  return (
    <Tabs defaultValue='fields'>
      <TabsList>
        <TabsTrigger value='fields'>{t('Edit')}</TabsTrigger>
        <TabsTrigger value='json'>JSON</TabsTrigger>
      </TabsList>
      <TabsContent value='fields' className='space-y-4'>
        {pack ? (
          <>
            <div className='grid gap-4 sm:grid-cols-2'>
              <div className='space-y-2'>
                <Label htmlFor='homepage-id'>{t('Package ID')}</Label>
                <Input
                  id='homepage-id'
                  value={pack.id}
                  disabled={props.disabled}
                  onChange={(event) => update('id', event.target.value)}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='homepage-name'>{t('Name')}</Label>
                <Input
                  id='homepage-name'
                  value={pack.name}
                  disabled={props.disabled}
                  onChange={(event) => update('name', event.target.value)}
                />
              </div>
            </div>
            <div className='space-y-2'>
              <Label htmlFor='homepage-description'>{t('Description')}</Label>
              <Input
                id='homepage-description'
                value={pack.description ?? ''}
                disabled={props.disabled}
                onChange={(event) => update('description', event.target.value)}
              />
            </div>
            <div className='space-y-2'>
              <Label htmlFor='homepage-layout'>{t('Page layout')}</Label>
              <Select
                value={pack.layout}
                onValueChange={(next) => {
                  if (next) update('layout', next)
                }}
                disabled={props.disabled}
              >
                <SelectTrigger id='homepage-layout'>
                  <SelectValue>
                    {pack.layout === 'content'
                      ? t('Keep site navigation')
                      : t('Full page')}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value='content'>
                    {t('Keep site navigation')}
                  </SelectItem>
                  <SelectItem value='full'>{t('Full page')}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {pack.kind === 'url' && (
              <div className='space-y-2'>
                <Label htmlFor='homepage-url'>URL</Label>
                <Input
                  id='homepage-url'
                  value={pack.url}
                  disabled={props.disabled}
                  onChange={(event) => update('url', event.target.value)}
                />
              </div>
            )}
            {pack.kind === 'html' && (
              <Tabs defaultValue='html'>
                <TabsList>
                  <TabsTrigger value='html'>HTML</TabsTrigger>
                  <TabsTrigger value='css'>CSS</TabsTrigger>
                  <TabsTrigger value='javascript'>JavaScript</TabsTrigger>
                </TabsList>
                {(['html', 'css', 'javascript'] as const).map((key) => (
                  <TabsContent key={key} value={key}>
                    <Textarea
                      aria-label={key}
                      value={pack[key] ?? ''}
                      onChange={(event) => update(key, event.target.value)}
                      disabled={props.disabled}
                      className='min-h-80 font-mono text-xs'
                      spellCheck={false}
                    />
                  </TabsContent>
                ))}
              </Tabs>
            )}
            <p className='text-muted-foreground text-xs'>
              {t('Edit config and advanced fields in the JSON tab.')}
            </p>
          </>
        ) : (
          <p role='alert'>{t('Invalid homepage package configuration')}</p>
        )}
      </TabsContent>
      <TabsContent value='json'>
        <JsonCodeEditor
          ariaLabel={t('Package manifest')}
          value={props.value}
          onChange={props.onChange}
          disabled={props.disabled}
          heightClassName='h-96 min-h-96 max-h-96'
        />
      </TabsContent>
    </Tabs>
  )
}
