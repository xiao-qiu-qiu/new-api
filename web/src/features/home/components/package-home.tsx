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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { PublicLayout } from '@/components/layout'
import { Footer } from '@/components/layout/components/footer'

import { resolveHomepagePackage, type HomepagePackage } from '../packages'
import { HomepagePackageFrame } from './package-frame'

export function PackageHome(props: { pack: HomepagePackage }) {
  const { t } = useTranslation()
  const resolved = useQuery({
    queryKey: ['homepage-template', props.pack],
    queryFn: () => resolveHomepagePackage(props.pack),
    staleTime: Infinity,
  })
  const frame = resolved.data ? (
    <HomepagePackageFrame pack={resolved.data} />
  ) : (
    <div className='flex min-h-96 items-center justify-center pt-20'>
      {resolved.isError
        ? t('Failed to load home page content')
        : t('Loading...')}
    </div>
  )
  if (props.pack.layout === 'full') {
    return (
      <>
        {frame}
        <Footer />
      </>
    )
  }
  return (
    <PublicLayout showMainContainer={false}>
      <main className='pt-16'>{frame}</main>
      <Footer />
    </PublicLayout>
  )
}
