import { afterEach, beforeEach, expect, test, vi } from "vitest";

const fixtures = vi.hoisted(() => ({
    stores: new Map<string, Map<string, unknown>>(),
    failStorage: false,
    saveAs: vi.fn<(blob: Blob, name: string) => void>(),
}));

// Browser storage and file downloads are the only replaced boundaries.
vi.mock("localforage", () => ({
    default: {
        createInstance: (options: { name: string; storeName: string }) => {
            const id = `${options.name}/${options.storeName}`;
            let values = fixtures.stores.get(id);
            if (!values) {
                values = new Map();
                fixtures.stores.set(id, values);
            }
            const records = values;
            return {
                getItem: async (key: string) => {
                    if (fixtures.failStorage) throw new Error("Storage unavailable");
                    return records.get(key) ?? null;
                },
                setItem: async (key: string, value: unknown) => {
                    if (fixtures.failStorage) throw new Error("Storage unavailable");
                    records.set(key, value);
                    return value;
                },
                removeItem: async (key: string) => {
                    if (fixtures.failStorage) throw new Error("Storage unavailable");
                    records.delete(key);
                },
                iterate: async (callback: (value: unknown, key: string) => void) => {
                    records.forEach(callback);
                },
            };
        },
    },
}));
vi.mock("file-saver", () => ({ saveAs: fixtures.saveAs }));

const values = new Map<string, string>();
const storage = {
    get length() { return values.size; },
    key: (index: number) => [...values.keys()][index] ?? null,
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value); },
    removeItem: (key: string) => { values.delete(key); },
};

function setSiteUser(userId: number, viewportWidth = 1280) {
    vi.stubGlobal("window", {
        __newApiCanvasHost: {
            context: { userId, theme: "light", language: "en-US", group: "site", model: "site-model", agentGroup: "site", agentModel: "site-model", channels: [] },
        },
        localStorage: storage,
        sessionStorage: storage,
        location: { origin: "https://station.example" },
        innerWidth: viewportWidth,
        setTimeout,
        clearTimeout,
    });
    vi.stubGlobal("localStorage", storage);
}

beforeEach(() => {
    vi.resetModules();
    fixtures.stores.clear();
    fixtures.failStorage = false;
    fixtures.saveAs.mockClear();
    values.clear();
    setSiteUser(101);
});
afterEach(() => vi.unstubAllGlobals());

test("reloading with a different account isolates media and Blob URL caches, and returning restores the original", async () => {
    const mediaA = await import("../file-storage");
    const blobA = new Blob(["user A media"], { type: "video/mp4" });
    const urlA = await mediaA.setMediaBlob("video:shared-key", blobA);
    expect(await mediaA.resolveMediaUrl("video:shared-key")).toBe(urlA);

    vi.resetModules();
    setSiteUser(202);
    const mediaB = await import("../file-storage");
    expect(await mediaB.getMediaBlob("video:shared-key")).toBeNull();
    expect(await mediaB.resolveMediaUrl("video:shared-key", "missing")).toBe("missing");
    await mediaB.setMediaBlob("video:shared-key", new Blob(["user B media"], { type: "video/mp4" }));

    vi.resetModules();
    setSiteUser(101);
    const reloadedA = await import("../file-storage");
    expect(await (await reloadedA.getMediaBlob("video:shared-key"))?.text()).toBe("user A media");
});

test("IndexedDB failure scopes fallback reads, writes and deletion without reading the shared legacy key", async () => {
    fixtures.failStorage = true;
    values.set("infinite-canvas:canvas_store", "legacy shared data");
    const { localForageStorage: storageA } = await import("@/lib/localforage-storage");
    expect(await storageA.getItem("infinite-canvas:canvas_store")).toBeNull();
    await storageA.setItem("infinite-canvas:canvas_store", "A");

    vi.resetModules();
    setSiteUser(202);
    const { localForageStorage: storageB } = await import("@/lib/localforage-storage");
    expect(await storageB.getItem("infinite-canvas:canvas_store")).toBeNull();
    await storageB.setItem("infinite-canvas:canvas_store", "B");
    await storageB.removeItem("infinite-canvas:canvas_store");
    expect(await storageA.getItem("infinite-canvas:canvas_store")).toBe("A");
    expect(values.get("infinite-canvas:canvas_store")).toBe("legacy shared data");
});

test("prompt sources and sidebar preferences hydrate only from the signed-in account", async () => {
    const { userLocalStorage } = await import("@/lib/site-storage");
    userLocalStorage.setItem("canvas-side-panel-width", "410");
    userLocalStorage.setItem("canvas-side-panel-node-view", "grid");
    const { usePromptSourceStore } = await import("@/stores/use-prompt-source-store");
    usePromptSourceStore.getState().updateSchedule("intervalMinutes", 0);

    vi.resetModules();
    setSiteUser(202, 700);
    const { useCanvasSidePanelStore: sidebarB } = await import("@/stores/use-canvas-side-panel-store");
    const { usePromptSourceStore: promptsB } = await import("@/stores/use-prompt-source-store");
    expect(sidebarB.getState().width).toBe(280);
    expect(sidebarB.getState().nodeViewMode).toBe("list");
    expect(sidebarB.getState().panelOpen).toBe(false);
    expect(promptsB.getState().schedule.intervalMinutes).toBe(30);
    sidebarB.getState().openPanel();
    vi.resetModules();
    setSiteUser(202, 700);
    const { useCanvasSidePanelStore: reloadedSidebarB } = await import("@/stores/use-canvas-side-panel-store");
    expect(reloadedSidebarB.getState().panelOpen).toBe(true);

    vi.resetModules();
    setSiteUser(101);
    const { useCanvasSidePanelStore: sidebarA } = await import("@/stores/use-canvas-side-panel-store");
    const { usePromptSourceStore: promptsA } = await import("@/stores/use-prompt-source-store");
    expect(sidebarA.getState().width).toBe(410);
    expect(sidebarA.getState().nodeViewMode).toBe("grid");
    expect(sidebarA.getState().panelOpen).toBe(true);
    expect(promptsA.getState().schedule.intervalMinutes).toBe(0);
});

test("legacy config import preserves the site bridge and exports preferences without connection credentials", async () => {
    const { useConfigStore } = await import("@/stores/use-config-store");
    const { importAppConfig, exportAppConfig } = await import("../config-file");
    const original = useConfigStore.getState().config;
    const channels = [{ id: "site", name: "Site", baseUrl: "https://station.example", apiKey: "site fixture", apiFormat: "openai" as const, models: [{ name: "site-model", capability: "image" as const }] }];
    const config = { ...original, channels, models: ["site::site-model"], model: "site::site-model", imageModel: "site::site-model", baseUrl: "https://station.example", apiKey: "site fixture", proxyEnabled: false, proxyUrl: "site proxy fixture" };
    useConfigStore.setState({ config });
    const imported = {
        app: "infinite-canvas", version: 1,
        config: { ...config, channels: [], models: ["foreign"], model: "foreign", imageModel: "foreign", baseUrl: "https://foreign.example", apiKey: "foreign fixture", proxyEnabled: true, proxyUrl: "http://127.0.0.1:23210", quality: "high" },
        webdav: { url: "https://dav.example", username: "dav-user", password: "foreign password fixture", directory: "backup", lastSyncedAt: "old" },
        promptSources: { sources: [], schedule: { intervalMinutes: 0, lastFetchedAt: "" } },
    };
    await importAppConfig(new File([JSON.stringify(imported)], "legacy.json"));
    expect(useConfigStore.getState().config).toEqual({ ...config, quality: "high" });
    expect(useConfigStore.getState().config.channels).toBe(channels);
    expect(useConfigStore.getState().webdav.password).toBe("");

    exportAppConfig();
    const [blob] = fixtures.saveAs.mock.calls[0];
    const exported = JSON.parse(await blob.text());
    expect(exported.config.quality).toBe("high");
    for (const key of ["channels", "models", "model", "imageModel", "baseUrl", "apiKey", "proxyEnabled", "proxyUrl"]) expect(exported.config).not.toHaveProperty(key);
    expect(exported.webdav).not.toHaveProperty("password");
});

test("WebDAV uses separate user directories and rejects paths that climb out of the user directory", async () => {
    const request = vi.fn<(url: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async () => new Response("", { status: 200 }));
    vi.stubGlobal("fetch", request);
    const config = { url: "https://dav.example", username: "", password: "", directory: "backup", lastSyncedAt: "" };
    const davA = await import("../webdav-sync");
    await davA.uploadWebdavFile(config, "canvas/manifest.json", new Blob(["A"]));
    expect(request.mock.calls.some(([url]) => String(url).includes("/backup/new-api-canvas/user-101/canvas/manifest.json"))).toBe(true);

    vi.resetModules();
    setSiteUser(202);
    const davB = await import("../webdav-sync");
    await davB.uploadWebdavFile(config, "canvas/manifest.json", new Blob(["B"]));
    expect(request.mock.calls.some(([url]) => String(url).includes("/backup/new-api-canvas/user-202/canvas/manifest.json"))).toBe(true);
    await expect(davB.downloadWebdavFile(config, "../user-101/canvas/manifest.json")).rejects.toThrow();
    expect(request.mock.calls.some(([url]) => String(url).includes("/../"))).toBe(false);
});

test("usage reads only the current user's databases and counts config keys under the common prefix", async () => {
    const open = vi.fn((name: string) => {
        const request: { result: { objectStoreNames: string[]; version: number; close: () => void }; onsuccess?: () => void } = {
            result: { objectStoreNames: [], version: 1, close: () => {} },
        };
        queueMicrotask(() => request.onsuccess?.());
        return request;
    });
    vi.stubGlobal("indexedDB", { open });
    vi.stubGlobal("navigator", {});
    const key = "new-api:canvas:101:infinite-canvas:ai_config_store";
    values.set(key, "{}");
    values.set("new-api:canvas:202:infinite-canvas:ai_config_store", "other user");
    values.set("infinite-canvas:ai_config_store", "legacy shared data");
    const { readLocalStorageUsage } = await import("../local-storage-usage");
    const usage = await readLocalStorageUsage();
    expect(open.mock.calls.map(([name]) => name)).toEqual(["new-api:canvas:101:infinite-canvas", "new-api:canvas:101:infinite-canvas-plugins"]);
    expect(usage.localStorageBytes).toBe(new TextEncoder().encode(`${key}{}`).byteLength);
});

test("missing authenticated user identity blocks creation of a shared storage namespace", async () => {
    setSiteUser(0);
    await expect(import("@/lib/site-storage")).rejects.toThrow("initialized site user");
});
