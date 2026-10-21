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
import { create } from 'zustand'
import { createJSONStorage, persist } from 'zustand/middleware'

type ChannelTestState = {
  prompts: Record<string, Record<string, string>>
  setPrompt: (channelId: number, model: string, prompt: string) => void
}

export const useChannelTestStore = create<ChannelTestState>()(
  persist(
    (set) => ({
      prompts: Object.create(null) as ChannelTestState['prompts'],
      setPrompt: (channelId, model, prompt) => {
        set((state) => {
          const channelKey = String(channelId)
          const channelPrompts = Object.assign(
            Object.create(null) as Record<string, string>,
            state.prompts[channelKey]
          )
          const prompts = { ...state.prompts }

          if (prompt.trim()) {
            channelPrompts[model] = prompt
            prompts[channelKey] = channelPrompts
          } else {
            delete channelPrompts[model]
            if (Object.keys(channelPrompts).length > 0) {
              prompts[channelKey] = channelPrompts
            } else {
              delete prompts[channelKey]
            }
          }

          return { prompts }
        })
      },
    }),
    {
      name: 'channel-test-prompts-v1',
      partialize: (state) => ({ prompts: state.prompts }),
      storage: createJSONStorage(() => ({
        getItem: (name) => {
          try {
            return localStorage.getItem(name)
          } catch {
            return null
          }
        },
        setItem: (name, value) => {
          try {
            localStorage.setItem(name, value)
          } catch {
            // Keep session edits usable when browser storage is unavailable.
          }
        },
        removeItem: (name) => {
          try {
            localStorage.removeItem(name)
          } catch {
            // Browser storage can be disabled independently of the session.
          }
        },
      })),
      merge: (persistedState, currentState) => {
        const storedPrompts = (persistedState as Partial<ChannelTestState>)
          ?.prompts
        const prompts = Object.create(null) as ChannelTestState['prompts']
        if (!storedPrompts || typeof storedPrompts !== 'object') {
          return { ...currentState, prompts }
        }

        for (const [channelId, models] of Object.entries(storedPrompts)) {
          if (!models || typeof models !== 'object') continue
          const channelPrompts = Object.create(null) as Record<string, string>
          for (const [model, prompt] of Object.entries(models)) {
            if (typeof prompt === 'string' && prompt.trim()) {
              channelPrompts[model] = prompt
            }
          }
          if (Object.keys(channelPrompts).length > 0) {
            prompts[channelId] = channelPrompts
          }
        }

        return { ...currentState, prompts }
      },
    }
  )
)
