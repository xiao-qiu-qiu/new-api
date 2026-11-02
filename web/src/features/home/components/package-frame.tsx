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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useTheme } from '@/context/theme-provider'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { useSystemConfig } from '@/hooks/use-system-config'
import { toIntlLocale } from '@/i18n/languages'

import { buildHomepageDocument, isHomepageMessage } from '../package-document'
import { HOMEPAGE_MESSAGE, type HomepagePackage } from '../packages'

export function HomepagePackageFrame(props: {
  pack: HomepagePackage
  preview?: boolean
}) {
  const { i18n, t } = useTranslation()
  const { resolvedTheme } = useTheme()
  const { systemName, logo } = useSystemConfig()
  const { copyToClipboard } = useCopyToClipboard()
  const frame = useRef<HTMLIFrameElement>(null)
  const [height, setHeight] = useState(800)
  const srcDoc = useMemo(
    () => buildHomepageDocument(props.pack, window.location.origin),
    [props.pack]
  )
  const context = useMemo(
    () => ({
      theme: resolvedTheme,
      language: toIntlLocale(i18n.resolvedLanguage || i18n.language),
      siteName: systemName,
      logo,
      origin: window.location.origin,
      apiBase: `${window.location.origin}/v1`,
      config: props.pack.config ?? {},
      messages:
        props.pack.locales?.[
          toIntlLocale(i18n.resolvedLanguage || i18n.language) ?? 'en'
        ] ??
        props.pack.locales?.en ??
        {},
    }),
    [
      resolvedTheme,
      i18n.resolvedLanguage,
      i18n.language,
      systemName,
      logo,
      props.pack.config,
      props.pack.locales,
    ]
  )

  const syncContext = useCallback(() => {
    const style = getComputedStyle(document.documentElement)
    const tokens = Object.fromEntries(
      [
        '--background',
        '--foreground',
        '--primary',
        '--primary-foreground',
        '--muted',
        '--muted-foreground',
        '--border',
        '--card',
        '--card-foreground',
        '--popover',
        '--popover-foreground',
      ].map((name) => [name, style.getPropertyValue(name)])
    )
    frame.current?.contentWindow?.postMessage(
      {
        channel: HOMEPAGE_MESSAGE,
        type: 'context',
        context: { ...context, tokens },
      },
      '*'
    )
  }, [context])

  useEffect(() => {
    syncContext()
  }, [syncContext])
  useEffect(() => {
    const receive = (event: MessageEvent) => {
      const target = frame.current?.contentWindow ?? null
      if (!isHomepageMessage(event, target)) return
      const data = event.data
      if (data.type === 'ready') syncContext()
      if (data.type === 'resize' && Number.isFinite(data.height)) {
        setHeight(Math.min(30000, Math.max(240, data.height)))
      }
      if (
        data.type === 'scroll' &&
        Number.isFinite(data.top) &&
        !props.preview
      ) {
        window.scrollTo({
          top:
            (frame.current?.getBoundingClientRect().top ?? 0) +
            window.scrollY +
            Math.max(0, data.top) -
            80,
          behavior: matchMedia('(prefers-reduced-motion: reduce)').matches
            ? 'instant'
            : 'smooth',
        })
      }
      if (
        data.type === 'copy' &&
        typeof data.text === 'string' &&
        data.text.length <= 100000 &&
        typeof data.requestId === 'string'
      ) {
        void copyToClipboard(data.text).then((success) =>
          target?.postMessage(
            {
              channel: HOMEPAGE_MESSAGE,
              type: 'copied',
              requestId: data.requestId,
              success,
            },
            '*'
          )
        )
      }
    }
    window.addEventListener('message', receive)
    return () => window.removeEventListener('message', receive)
  }, [copyToClipboard, syncContext, props.preview])

  return (
    <iframe
      ref={frame}
      title={props.preview ? t('Homepage package preview') : props.pack.name}
      className='block w-full border-0'
      style={{ height: props.preview ? 620 : height }}
      src={props.pack.kind === 'url' ? props.pack.url : undefined}
      srcDoc={props.pack.kind === 'html' ? srcDoc : undefined}
      sandbox='allow-forms allow-popups allow-popups-to-escape-sandbox allow-scripts allow-top-navigation-by-user-activation'
      referrerPolicy='no-referrer'
      onLoad={syncContext}
    />
  )
}
