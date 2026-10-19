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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleHelp } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  SideDrawerSection,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
} from '@/components/drawer-layout'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { MultiSelect } from '@/components/multi-select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  videoCapabilitiesDraft,
  videoCapabilitiesPayload,
  videoCapabilitiesSchema,
  type VideoCapabilitiesDraft,
  type VideoCapabilitiesSnapshot,
} from '../lib/video-capabilities'

export function ModelVideoCapabilities(props: {
  modelName: string
  onDirtyChange: (dirty: boolean) => void
}) {
  const { t } = useTranslation()
  const canEdit = useAuthStore(
    (state) => state.auth.user?.role === ROLE.SUPER_ADMIN
  )
  const query = useQuery({
    queryKey: ['video-model-capabilities', props.modelName],
    queryFn: async (): Promise<VideoCapabilitiesSnapshot> => {
      const response = await api.get('/api/option/video_model_capabilities', {
        params: { model: props.modelName },
      })
      return requireServerSuccess(response.data).data
    },
    enabled: canEdit && Boolean(props.modelName),
    refetchOnWindowFocus: false,
  })
  if (!canEdit) {
    return (
      <div className='text-muted-foreground p-4 text-sm'>
        {t('Video capabilities are managed by a super administrator.')}
      </div>
    )
  }
  if (query.isError && !query.data) {
    return (
      <ErrorState
        description={getServerErrorMessage(query.error)}
        onRetry={() => void query.refetch()}
      />
    )
  }
  if (!query.data) return <LoadingState />
  return (
    <VideoCapabilitiesEditor
      key={props.modelName}
      snapshot={query.data}
      onDirtyChange={props.onDirtyChange}
      onReload={async () => (await query.refetch({ throwOnError: true })).data}
    />
  )
}

function VideoCapabilitiesEditor(props: {
  snapshot: VideoCapabilitiesSnapshot
  onDirtyChange: (dirty: boolean) => void
  onReload: () => Promise<VideoCapabilitiesSnapshot | undefined>
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [snapshot, setSnapshot] = useState(props.snapshot)
  const [resetOpen, setResetOpen] = useState(false)
  const [reloadOpen, setReloadOpen] = useState(false)
  const form = useForm<VideoCapabilitiesDraft>({
    resolver: zodResolver(videoCapabilitiesSchema),
    defaultValues: videoCapabilitiesDraft(snapshot.video_capabilities),
  })
  const dirty = form.formState.isDirty
  const onDirtyChange = props.onDirtyChange
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange])
  const reload = useMutation({
    meta: { errorToast: false },
    onMutate: () => form.clearErrors('root.server'),
    mutationFn: props.onReload,
    onSuccess: (loaded) => {
      if (!loaded) return
      setSnapshot(loaded)
      form.reset(videoCapabilitiesDraft(loaded.video_capabilities))
      setReloadOpen(false)
    },
    onError: (error) => {
      setReloadOpen(false)
      form.setError('root.server', {
        message: getServerErrorMessage(error),
      })
    },
  })
  const save = useMutation({
    meta: { errorToast: false },
    onMutate: () => form.clearErrors('root.server'),
    mutationFn: async (
      draft: VideoCapabilitiesDraft | null
    ): Promise<VideoCapabilitiesSnapshot> => {
      const response = await api.patch('/api/option/video_model_capabilities', {
        model_name: snapshot.model_name,
        video_capabilities: draft ? videoCapabilitiesPayload(draft) : null,
        expected_version: snapshot.version,
      })
      return requireServerSuccess(response.data).data
    },
    onSuccess: async (saved) => {
      setSnapshot(saved)
      form.reset(videoCapabilitiesDraft(saved.video_capabilities))
      setResetOpen(false)
      client.setQueryData(['video-model-capabilities', saved.model_name], saved)
      await client.invalidateQueries({ queryKey: ['pricing'] })
      toast.success(t('Video capabilities saved'))
    },
    onError: (error) =>
      form.setError('root.server', {
        message: getServerErrorMessage(
          error,
          t('Failed to save video capabilities')
        ),
      }),
  })
  const listFields = [
    {
      name: 'resolutions',
      label: t('Allowed resolutions'),
      options: ['480p', '720p', '1080p', '1440p', '2160p'],
    },
    {
      name: 'aspect_ratios',
      label: t('Allowed aspect ratios'),
      options: ['16:9', '9:16', '1:1', '4:3', '3:4', '21:9', 'auto'],
    },
    {
      name: 'durations',
      label: t('Allowed durations (seconds)'),
      options: ['4', '5', '6', '8', '10', '12', '15', '20', '30'],
    },
  ] as const
  const numberFields = [
    {
      name: 'max_duration_seconds',
      label: t('Maximum duration (seconds)'),
      min: 1,
      max: 3600,
    },
    {
      name: 'max_reference_images',
      label: t('Maximum reference images'),
      min: 0,
      max: 100,
    },
    {
      name: 'max_reference_audio',
      label: t('Maximum reference audio files'),
      min: 0,
      max: 100,
    },
    {
      name: 'max_reference_videos',
      label: t('Maximum reference videos'),
      min: 0,
      max: 100,
    },
  ] as const
  return (
    <>
      <Form {...form}>
        <form
          id='video-capabilities-form'
          className={sideDrawerFormClassName()}
          onSubmit={form.handleSubmit((values) => save.mutate(values))}
        >
          <SideDrawerSection>
            <div className='flex items-center gap-1'>
              <h3 className='text-sm font-semibold'>
                {t('Video capabilities')}
              </h3>
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('About video capabilities')}
                    />
                  }
                >
                  <CircleHelp className='size-4' />
                </TooltipTrigger>
                <TooltipContent>
                  {t(
                    'Applies to Infinite Canvas and Video Studio. Leave fields empty to use model defaults. Set a reference limit to 0 to disable that input.'
                  )}
                </TooltipContent>
              </Tooltip>
            </div>
            {listFields.map((item) => (
              <FormField
                key={item.name}
                control={form.control}
                name={item.name}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{item.label}</FormLabel>
                    <FormControl>
                      <MultiSelect
                        aria-label={item.label}
                        selected={field.value}
                        onChange={field.onChange}
                        options={item.options.map((value) => ({
                          value,
                          label: value,
                        }))}
                        allowCreate
                        disabled={save.isPending || reload.isPending}
                        placeholder={t('Model defaults')}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            ))}
            <div className='grid gap-4 sm:grid-cols-2'>
              {numberFields.map((item) => (
                <FormField
                  key={item.name}
                  control={form.control}
                  name={item.name}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{item.label}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='number'
                          min={item.min}
                          max={item.max}
                          step={1}
                          disabled={save.isPending || reload.isPending}
                          placeholder={t('Model defaults')}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}
            </div>
          </SideDrawerSection>
        </form>
      </Form>
      <div className={sideDrawerFooterClassName('flex-wrap')}>
        {form.formState.errors.root?.server?.message && (
          <Alert variant='destructive' className='basis-full'>
            <AlertDescription>
              {form.formState.errors.root.server.message}
            </AlertDescription>
          </Alert>
        )}
        <Button
          type='button'
          variant='outline'
          disabled={save.isPending || reload.isPending}
          onClick={() => (dirty ? setReloadOpen(true) : reload.mutate())}
        >
          {t('Reload video capabilities')}
        </Button>
        <Button
          type='button'
          variant='outline'
          disabled={
            save.isPending ||
            reload.isPending ||
            (!snapshot.video_capabilities && !dirty)
          }
          onClick={() => setResetOpen(true)}
        >
          {t('Restore model defaults')}
        </Button>
        <Button
          type='submit'
          form='video-capabilities-form'
          disabled={save.isPending || reload.isPending || !dirty}
        >
          {save.isPending ? t('Saving...') : t('Save video capabilities')}
        </Button>
      </div>
      <ConfirmDialog
        open={reloadOpen}
        onOpenChange={setReloadOpen}
        title={t('Discard unsaved changes?')}
        desc={t('Your changes have not been saved.')}
        confirmText={t('Reload video capabilities')}
        isLoading={reload.isPending}
        handleConfirm={() => reload.mutate()}
      />
      <ConfirmDialog
        open={resetOpen}
        onOpenChange={setResetOpen}
        title={t('Restore model defaults?')}
        desc={t('Remove the video capability overrides for this model.')}
        confirmText={t('Restore model defaults')}
        isLoading={save.isPending}
        handleConfirm={() => save.mutate(null)}
      />
    </>
  )
}
