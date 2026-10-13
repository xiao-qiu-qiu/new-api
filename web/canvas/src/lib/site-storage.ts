import localforage from "localforage";
import type { StateStorage } from "zustand/middleware";

import { getSiteContext } from "@/integration/site-context";

// The entry point must finish the site handshake before importing the app.
// Bind this document to one user so delayed saves and Blob URL caches keep
// the same owner. Account changes must load a new canvas document.
const userId = getSiteContext()?.userId;
if (typeof userId !== "number" || !Number.isSafeInteger(userId) || userId <= 0) {
    throw new Error("Canvas storage requires an initialized site user");
}

const namespace = `new-api:canvas:${userId}`;

export function getUserStorageKey(key: string): string {
    return `${namespace}:${key}`;
}

export function getUserDatabaseName(name = "infinite-canvas"): string {
    return getUserStorageKey(name);
}

export function getUserWebdavDirectory(directory: string): string {
    return [directory.trim().replaceAll(/^\/+|\/+$/g, ""), "new-api-canvas", `user-${userId}`].filter(Boolean).join("/");
}

export function createUserLocalForage(storeName: string, databaseName = "infinite-canvas"): LocalForage {
    return localforage.createInstance({ name: getUserDatabaseName(databaseName), storeName });
}

// Only callers opting into this adapter are scoped; browser Storage is untouched.
export const userLocalStorage = {
    getItem: (name) => (typeof window === "undefined" ? null : window.localStorage.getItem(getUserStorageKey(name))),
    setItem: (name, value) => {
        if (typeof window !== "undefined") window.localStorage.setItem(getUserStorageKey(name), value);
    },
    removeItem: (name) => {
        if (typeof window !== "undefined") window.localStorage.removeItem(getUserStorageKey(name));
    },
} satisfies StateStorage;
