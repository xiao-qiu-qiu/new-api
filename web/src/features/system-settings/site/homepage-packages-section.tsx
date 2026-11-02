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
import { useMemo, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { HomepagePackageFrame } from '@/features/home/components/package-frame'
import {
  BUILTIN_HOMEPAGES,
  HOMEPAGE_MAX_BYTES,
  parseHomepagePackage,
  parseHomepageRegistry,
  resolveHomepagePackage,
  type HomepagePackage,
} from '@/features/home/packages'
import { handleServerError } from '@/lib/handle-server-error'

import { FormNavigationGuard } from '../components/form-navigation-guard'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { HomepagePackageEditor } from './homepage-package-editor'

export function HomepagePackagesSection(props: {
  defaultValue: string
  legacy: string
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const importedFile = useRef<HTMLInputElement>(null)
  const [selectedId, setSelectedId] = useState('official')
  const [preview, setPreview] = useState<HomepagePackage | null>(null)
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [pendingSelection, setPendingSelection] =
    useState<HomepagePackage | null>(null)
  const form = useForm({
    defaultValues: { manifest: JSON.stringify(BUILTIN_HOMEPAGES[0], null, 2) },
  })
  const raw = form.watch('manifest')
  const registry = useMemo(() => {
    try {
      return parseHomepageRegistry(props.defaultValue, props.legacy)
    } catch {
      return null
    }
  }, [props.defaultValue, props.legacy])
  const saved = registry?.packages.find((item) => item.id === selectedId)
  const locked = selectedId === 'official' || selectedId === 'codeq'
  const disabled = busy || updateOption.isPending

  const select = (pack: HomepagePackage, draft = false) => {
    form.reset({ manifest: draft ? '' : JSON.stringify(pack, null, 2) })
    if (draft) {
      form.setValue('manifest', JSON.stringify(pack, null, 2), {
        shouldDirty: true,
      })
    }
    setSelectedId(pack.id)
    setPreview(null)
  }
  const validate = () => {
    try {
      const pack = parseHomepagePackage(raw)
      form.clearErrors()
      return pack
    } catch {
      form.setError('manifest', {
        message: t('Invalid homepage package configuration'),
      })
      return null
    }
  }
  const save = async (activate: boolean) => {
    const pack = validate()
    if (!pack || !registry) return
    if (
      pack.id !== selectedId &&
      (registry.packages.some((item) => item.id === pack.id) ||
        ['official', 'codeq', 'legacy'].includes(pack.id))
    ) {
      form.setError('manifest', { message: t('Package ID already exists') })
      return
    }
    const packages = registry.packages.filter((item) => item.id !== selectedId)
    if (pack.kind !== 'builtin') packages.push(pack)
    let active = registry.active
    if (activate || registry.active === selectedId) active = pack.id
    const next = JSON.stringify({ schemaVersion: 1, active, packages })
    try {
      parseHomepageRegistry(next)
      await updateOption.mutateAsync({ key: 'HomePagePackages', value: next })
      select(pack)
    } catch (error) {
      handleServerError(error, t('Failed to update setting'))
    }
  }
  const showPreview = async () => {
    const pack = validate()
    if (!pack) return
    if (pack.id === 'official' && pack.kind === 'builtin') {
      window.open('/', '_blank', 'noopener,noreferrer')
      return
    }
    setBusy(true)
    try {
      setPreview(await resolveHomepagePackage(pack))
    } catch (error) {
      handleServerError(error, t('Failed to load home page content'))
    } finally {
      setBusy(false)
    }
  }
  const duplicate = async () => {
    const pack = validate()
    if (!pack) return
    setBusy(true)
    try {
      const source = await resolveHomepagePackage(pack)
      if (source.kind === 'builtin') return
      let id = `${source.id.replace(/-custom$/, '')}-custom`
      while (registry?.packages.some((item) => item.id === id)) id += '-copy'
      const copy = { ...source, id }
      select(copy, true)
    } catch (error) {
      handleServerError(error, t('Failed to load home page content'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <SettingsSection title={t('Homepage packages')}>
      <FormNavigationGuard when={form.formState.isDirty} />
      {!registry && (
        <p role='alert' className='text-destructive'>
          {t('Invalid homepage package configuration')}
        </p>
      )}
      <div className='grid items-start gap-5 lg:grid-cols-[230px_minmax(0,1fr)]'>
        <div className='space-y-2'>
          {[...BUILTIN_HOMEPAGES, ...(registry?.packages ?? [])].map((pack) => (
            <Button
              key={pack.id}
              variant={selectedId === pack.id ? 'secondary' : 'outline'}
              disabled={disabled}
              className='h-auto min-h-12 w-full justify-between gap-2 px-3 py-3'
              aria-pressed={selectedId === pack.id}
              onClick={() => {
                if (form.formState.isDirty) setPendingSelection(pack)
                else select(pack)
              }}
            >
              <span className='truncate'>
                {pack.kind === 'builtin' && pack.id === 'official'
                  ? t('Official homepage')
                  : pack.name}
              </span>
              {registry?.active === pack.id && (
                <span className='text-primary shrink-0 text-xs'>
                  {t('Active')}
                </span>
              )}
            </Button>
          ))}
          <div className='flex flex-wrap gap-2 pt-2'>
            <Button
              variant='outline'
              disabled={disabled || form.formState.isDirty}
              onClick={() =>
                select(
                  {
                    schemaVersion: 1,
                    id: `custom-${Date.now().toString(36)}`,
                    name: t('Custom Home Page'),
                    kind: 'html',
                    layout: 'content',
                    html: '<main><h1>Hello, New API</h1></main>',
                    css: 'body { margin: 0; padding: 48px; font-family: system-ui; }',
                    javascript: '',
                    config: {},
                  },
                  true
                )
              }
            >
              {t('New HTML package')}
            </Button>
            <Button
              variant='outline'
              disabled={disabled || form.formState.isDirty}
              onClick={() =>
                select(
                  {
                    schemaVersion: 1,
                    id: `external-${Date.now().toString(36)}`,
                    name: t('Custom Home Page'),
                    kind: 'url',
                    layout: 'content',
                    url: 'https://example.com',
                  },
                  true
                )
              }
            >
              {t('New URL package')}
            </Button>
          </div>
          {props.legacy && (
            <Button
              variant='ghost'
              disabled={disabled || form.formState.isDirty}
              onClick={() => {
                if (registry) {
                  void updateOption
                    .mutateAsync({
                      key: 'HomePagePackages',
                      value: JSON.stringify({ ...registry, active: 'legacy' }),
                    })
                    .catch(() => {})
                }
              }}
            >
              {t('Use legacy homepage content')}
            </Button>
          )}
        </div>
        <div className='min-w-0 space-y-4 rounded-xl border p-4 sm:p-5'>
          <div className='flex flex-wrap gap-2'>
            <Button
              disabled={disabled || !registry}
              onClick={() => void save(true)}
            >
              {t('Save and activate')}
            </Button>
            {!locked && (
              <Button
                variant='outline'
                disabled={disabled || !registry}
                onClick={() => void save(false)}
              >
                {t('Save package')}
              </Button>
            )}
            <Button
              variant='outline'
              disabled={disabled || selectedId === 'official'}
              onClick={() => void showPreview()}
            >
              {t('Preview')}
            </Button>
            <Button
              variant='outline'
              disabled={disabled || selectedId === 'official'}
              onClick={() => void duplicate()}
            >
              {t('Duplicate and edit')}
            </Button>
            <Button
              variant='ghost'
              disabled={disabled}
              onClick={() =>
                select(
                  saved ??
                    BUILTIN_HOMEPAGES.find((item) => item.id === selectedId) ??
                    BUILTIN_HOMEPAGES[0]
                )
              }
            >
              {t('Reset')}
            </Button>
            {saved && (
              <Button
                variant='destructive'
                disabled={disabled}
                onClick={() => setDeleting(true)}
              >
                {t('Delete')}
              </Button>
            )}
          </div>
          <HomepagePackageEditor
            value={raw}
            onChange={(value) =>
              form.setValue('manifest', value, { shouldDirty: true })
            }
            disabled={disabled || locked}
          />
          {form.formState.errors.manifest && (
            <p role='alert' className='text-destructive text-sm'>
              {form.formState.errors.manifest.message}
            </p>
          )}
          <div className='flex flex-wrap items-end gap-3 border-t pt-4'>
            <div className='min-w-0 flex-1 space-y-2'>
              <Label htmlFor='homepage-import'>
                {t('Import homepage package')}
              </Label>
              <Input
                ref={importedFile}
                id='homepage-import'
                type='file'
                accept='.json,application/json'
                disabled={disabled || form.formState.isDirty}
                onChange={async (event) => {
                  const file = event.target.files?.[0]
                  if (!file) return
                  try {
                    if (file.size > HOMEPAGE_MAX_BYTES) {
                      throw new Error('Invalid homepage package configuration')
                    }
                    const pack = parseHomepagePackage(await file.text())
                    if (
                      pack.kind !== 'builtin' &&
                      registry?.packages.some((item) => item.id === pack.id)
                    ) {
                      throw new Error('Package ID already exists')
                    }
                    select(pack, pack.kind !== 'builtin')
                  } catch (error) {
                    handleServerError(
                      error,
                      t('Invalid homepage package configuration')
                    )
                  }
                  if (importedFile.current) importedFile.current.value = ''
                }}
              />
            </div>
            <Button
              variant='outline'
              disabled={disabled}
              onClick={async () => {
                const pack = validate()
                if (!pack) return
                try {
                  const resolved = await resolveHomepagePackage(pack)
                  const url = URL.createObjectURL(
                    new Blob([JSON.stringify(resolved, null, 2)], {
                      type: 'application/json',
                    })
                  )
                  const link = document.createElement('a')
                  link.href = url
                  link.download = `${resolved.id}.homepage.json`
                  link.click()
                  setTimeout(() => URL.revokeObjectURL(url), 1000)
                } catch (error) {
                  handleServerError(error)
                }
              }}
            >
              {t('Export package')}
            </Button>
          </div>
        </div>
      </div>
      {preview && (
        <div className='overflow-hidden rounded-xl border'>
          <HomepagePackageFrame
            key={JSON.stringify(preview)}
            pack={preview}
            preview
          />
        </div>
      )}
      <ConfirmDialog
        open={!!pendingSelection}
        onOpenChange={(open) => {
          if (!open) setPendingSelection(null)
        }}
        title={t('Unsaved changes')}
        desc={t('Discard unsaved changes?')}
        handleConfirm={() => {
          if (pendingSelection) select(pendingSelection)
          setPendingSelection(null)
        }}
      />
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={t('Delete homepage package')}
        desc={t('Deleting the active package restores the official homepage.')}
        destructive
        isLoading={updateOption.isPending}
        handleConfirm={() => {
          if (!registry) return
          void updateOption
            .mutateAsync({
              key: 'HomePagePackages',
              value: JSON.stringify({
                ...registry,
                active:
                  registry.active === selectedId ? 'official' : registry.active,
                packages: registry.packages.filter(
                  (item) => item.id !== selectedId
                ),
              }),
            })
            .then(() => {
              select(BUILTIN_HOMEPAGES[0])
              setDeleting(false)
            })
            .catch(() => {})
        }}
      />
    </SettingsSection>
  )
}
