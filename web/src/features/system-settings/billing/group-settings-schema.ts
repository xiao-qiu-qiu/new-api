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
import * as z from 'zod'

export const specialPricingRuleSchema = z.strictObject({
  user_group: z
    .string()
    .min(1)
    .refine((value) => value === value.trim() && value !== 'auto'),
  model: z
    .string()
    .min(1)
    .refine((value) => value === value.trim()),
  price: z.number().finite().nonnegative(),
  billing_unit: z.enum(['image', 'request']),
})

export type SpecialPricingRule = z.infer<typeof specialPricingRuleSchema>

export const groupSettingsJsonSchemas = {
  UserGroups: z.record(z.string(), z.string()),
  GroupRatio: z.record(z.string(), z.number().finite().nonnegative()),
  TopupGroupRatio: z.record(z.string(), z.number().finite().nonnegative()),
  UserUsableGroups: z.record(z.string(), z.string()),
  GroupGroupRatio: z.record(
    z.string(),
    z.record(z.string(), z.number().finite().nonnegative())
  ),
  SpecialPricingRules: z.array(specialPricingRuleSchema).refine((rules) => {
    const keys = rules.map((rule) =>
      JSON.stringify([rule.user_group, rule.model])
    )
    return new Set(keys).size === keys.length
  }),
  GroupSpecialUsableGroup: z.record(
    z.string(),
    z.record(z.string(), z.string())
  ),
  AutoGroups: z.array(z.string()),
}
