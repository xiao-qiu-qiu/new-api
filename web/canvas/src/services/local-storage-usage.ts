import { getUserDatabaseName, getUserStorageKey } from "@/lib/site-storage";

export type IndexedDbStoreUsage = { name: string; records: number; bytes: number };
export type IndexedDbDatabaseUsage = { name: string; version: number; bytes: number; stores: IndexedDbStoreUsage[] };
export type LocalStorageUsage = { usage: number; quota: number | null; contentBytes: number; localStorageBytes: number; databases: IndexedDbDatabaseUsage[] };

export async function readLocalStorageUsage(): Promise<LocalStorageUsage> {
    const [estimate, ...databases] = await Promise.all([
        readStorageEstimate(),
        readDatabaseUsage(getUserDatabaseName()),
        readDatabaseUsage(getUserDatabaseName("infinite-canvas-plugins")),
    ]);
    const contentBytes = databases.reduce((total, database) => total + database.bytes, 0);
    let localStorageBytes = 0;
    if (typeof window !== "undefined") {
        const prefix = getUserStorageKey("");
        const encoder = new TextEncoder();
        for (let index = 0; index < window.localStorage.length; index += 1) {
            const key = window.localStorage.key(index);
            if (key?.startsWith(prefix)) localStorageBytes += encoder.encode(key + (window.localStorage.getItem(key) ?? "")).byteLength;
        }
    }
    return { usage: estimate?.usage ?? contentBytes + localStorageBytes, quota: estimate?.quota ?? null, contentBytes, localStorageBytes, databases };
}

async function readStorageEstimate() {
    if (typeof navigator === "undefined" || !navigator.storage?.estimate) return null;
    try {
        return await navigator.storage.estimate();
    } catch {
        return null;
    }
}

function readDatabaseUsage(name: string) {
    return new Promise<IndexedDbDatabaseUsage>((resolve, reject) => {
        const request = indexedDB.open(name);
        request.onerror = () => reject(request.error);
        request.onsuccess = () => {
            const database = request.result;
            const names = [...database.objectStoreNames];
            if (!names.length) {
                database.close();
                resolve({ name, version: database.version, bytes: 0, stores: [] });
                return;
            }
            const transaction = database.transaction(names, "readonly");
            Promise.all(names.map((storeName) => readStoreUsage(transaction.objectStore(storeName))))
                .then((stores) => {
                    database.close();
                    resolve({ name, version: database.version, bytes: stores.reduce((total, store) => total + store.bytes, 0), stores: stores.sort((a, b) => b.bytes - a.bytes) });
                })
                .catch((error: unknown) => {
                    database.close();
                    reject(error);
                });
        };
    });
}

function readStoreUsage(store: IDBObjectStore) {
    return new Promise<IndexedDbStoreUsage>((resolve, reject) => {
        let records = 0;
        let bytes = 0;
        const request = store.openCursor();
        request.onerror = () => reject(request.error);
        request.onsuccess = () => {
            const cursor = request.result;
            if (!cursor) {
                resolve({ name: store.name, records, bytes });
                return;
            }
            records += 1;
            bytes += valueBytes(cursor.value);
            cursor.continue();
        };
    });
}

function valueBytes(value: unknown) {
    if (value instanceof Blob) return value.size;
    if (value instanceof ArrayBuffer) return value.byteLength;
    if (ArrayBuffer.isView(value)) return value.byteLength;
    return new TextEncoder().encode(typeof value === "string" ? value : JSON.stringify(value)).byteLength;
}
