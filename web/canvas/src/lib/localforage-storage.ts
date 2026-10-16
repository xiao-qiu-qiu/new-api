import type { StateStorage } from "zustand/middleware";

import { createUserLocalForage, userLocalStorage } from "@/lib/site-storage";

const store = createUserLocalForage("app_state");

export const localForageStorage: StateStorage = {
    getItem: async (name) => {
        if (typeof window === "undefined") return null;
        try {
            return (await store.getItem<string>(name)) || null;
        } catch {
            return userLocalStorage.getItem(name);
        }
    },
    setItem: async (name, value) => {
        if (typeof window === "undefined") return;
        try {
            await store.setItem(name, value);
        } catch {
            await userLocalStorage.setItem(name, value);
        }
    },
    removeItem: async (name) => {
        if (typeof window === "undefined") return;
        try {
            await store.removeItem(name);
        } catch {
            await userLocalStorage.removeItem(name);
        }
    },
};
