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
import { groupSettingsJsonSchemas } from '../billing/group-settings-schema'
import { formatJsonForTextarea, normalizeJsonString } from './utils'

export type GroupSettingsValues = {
  [Field in keyof typeof groupSettingsJsonSchemas]: string
} & {
  MaxTokenAutoGroups: number
  DefaultUseAutoGroup: boolean
}

export type GroupSettingsDefaults = Omit<
  GroupSettingsValues,
  'UserGroups' | 'SpecialPricingRules'
> &
  Partial<Pick<GroupSettingsValues, 'UserGroups' | 'SpecialPricingRules'>>

export function formatGroupSettings(
  values: GroupSettingsDefaults
): GroupSettingsValues {
  return {
    ...values,
    UserGroups: formatJsonForTextarea(values.UserGroups ?? '{}'),
    GroupRatio: formatJsonForTextarea(values.GroupRatio),
    TopupGroupRatio: formatJsonForTextarea(values.TopupGroupRatio),
    UserUsableGroups: formatJsonForTextarea(values.UserUsableGroups),
    GroupGroupRatio: formatJsonForTextarea(values.GroupGroupRatio),
    SpecialPricingRules: formatJsonForTextarea(
      values.SpecialPricingRules ?? '[]'
    ),
    GroupSpecialUsableGroup: formatJsonForTextarea(
      values.GroupSpecialUsableGroup
    ),
    AutoGroups: formatJsonForTextarea(values.AutoGroups),
  }
}

export function normalizeGroupSettings(
  values: GroupSettingsDefaults,
  previousUserGroups?: string
): GroupSettingsValues {
  const normalized = {
    ...values,
    UserGroups: normalizeJsonString(values.UserGroups ?? '{}'),
    GroupRatio: normalizeJsonString(values.GroupRatio),
    TopupGroupRatio: normalizeJsonString(values.TopupGroupRatio),
    UserUsableGroups: normalizeJsonString(values.UserUsableGroups),
    GroupGroupRatio: normalizeJsonString(values.GroupGroupRatio),
    SpecialPricingRules: normalizeJsonString(
      values.SpecialPricingRules ?? '[]'
    ),
    GroupSpecialUsableGroup: normalizeJsonString(
      values.GroupSpecialUsableGroup
    ),
    AutoGroups: normalizeJsonString(values.AutoGroups),
  }
  if (previousUserGroups === undefined) return normalized

  const previousGroups = groupSettingsJsonSchemas.UserGroups.parse(
    JSON.parse(previousUserGroups)
  )
  const nextGroups = groupSettingsJsonSchemas.UserGroups.parse(
    JSON.parse(normalized.UserGroups)
  )
  const removedGroups = new Set(
    Object.keys(previousGroups).filter(
      (name) => !Object.hasOwn(nextGroups, name)
    )
  )
  if (!removedGroups.size) return normalized

  // Keep dependent removals in the same group-settings transaction.
  for (const field of [
    'TopupGroupRatio',
    'GroupGroupRatio',
    'GroupSpecialUsableGroup',
  ] as const) {
    const entries = JSON.parse(normalized[field]) as Record<string, unknown>
    for (const name of removedGroups) delete entries[name]
    normalized[field] = JSON.stringify(entries)
  }
  const rules = groupSettingsJsonSchemas.SpecialPricingRules.parse(
    JSON.parse(normalized.SpecialPricingRules)
  )
  normalized.SpecialPricingRules = JSON.stringify(
    rules.filter(
      (rule) => rule.user_group === '*' || !removedGroups.has(rule.user_group)
    )
  )
  return normalized
}
