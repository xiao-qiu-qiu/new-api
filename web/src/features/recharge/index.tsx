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
import { ExternalLink, ShoppingBag } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout/components/section-page-layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'

export function Recharge() {
  const { t } = useTranslation()
  const { status, loading, error } = useStatus()
  let storeUrl: string | undefined
  let sandbox =
    'allow-forms allow-scripts allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-downloads'

  try {
    const url = new URL(status?.recharge_store_url ?? '')
    if (
      (url.protocol === 'https:' || url.protocol === 'http:') &&
      !url.username &&
      !url.password
    ) {
      storeUrl = url.href
      // External stores need their own origin for cookies and localStorage.
      // Same-origin frames retain an opaque origin to isolate console data.
      if (url.origin !== window.location.origin) {
        sandbox += ' allow-same-origin'
      }
    }
  } catch {
    // Empty or invalid configuration is handled by the empty state below.
  }

  let content
  if (loading && !status) {
    content = <LoadingState className='h-full' />
  } else if (error && !status) {
    content = <ErrorState description={t('Failed to load recharge store.')} />
  } else if (!storeUrl) {
    content = (
      <EmptyState
        icon={ShoppingBag}
        title={t('Recharge store is not configured')}
        description={t(
          'Please contact the administrator to set up the recharge store.'
        )}
      />
    )
  } else {
    content = (
      <iframe
        key={storeUrl}
        src={storeUrl}
        title={t('Recharge Store')}
        className='h-full w-full rounded-xl border bg-white'
        allow='payment'
        referrerPolicy='strict-origin-when-cross-origin'
        sandbox={sandbox}
      />
    )
  }

  return (
    <SectionPageLayout fixedContent>
      <SectionPageLayout.Title>{t('Recharge')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        {storeUrl && (
          <Button
            variant='outline'
            role='link'
            render={
              <a href={storeUrl} target='_blank' rel='noopener noreferrer' />
            }
          >
            <ExternalLink className='size-4' aria-hidden='true' />
            {t('Open in new tab')}
          </Button>
        )}
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>{content}</SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
