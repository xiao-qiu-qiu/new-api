import { saveAs } from "file-saver";

import i18n from "@/i18n";
import { useConfigStore, type AiConfig, type WebdavSyncConfig } from "@/stores/use-config-store";
import { usePromptSourceStore, type PromptSourceSchedule } from "@/stores/use-prompt-source-store";
import type { PromptSource } from "@/services/api/prompt-source-presets";

// Site channels, model selections, credentials and proxy settings belong to
// the host bridge. Configuration files carry only portable canvas preferences.
const PORTABLE_CONFIG_KEYS = [
    "audioVoice", "audioFormat", "audioSpeed", "audioInstructions",
    "videoSeconds", "vquality", "videoGenerateAudio", "videoWatermark", "videoMode",
    "systemPrompt", "reasoningEffort", "quality", "size", "background", "count",
    "canvasImageCount", "nodeRegenerateBehavior", "autoFocusOnSelect",
] as const satisfies readonly (keyof AiConfig)[];

type PortableConfig = Pick<AiConfig, (typeof PORTABLE_CONFIG_KEYS)[number]>;

type AppConfigFile = {
    app: "infinite-canvas";
    version: 1;
    exportedAt: string;
    config: Partial<PortableConfig>;
    webdav: Omit<WebdavSyncConfig, "password">;
    promptSources: {
        sources: PromptSource[];
        schedule: PromptSourceSchedule;
    };
};

export function exportAppConfig() {
    const { config, webdav } = useConfigStore.getState();
    const { sources, schedule } = usePromptSourceStore.getState();
    const portableConfig = Object.fromEntries(PORTABLE_CONFIG_KEYS.map((key) => [key, config[key]])) as PortableConfig;
    const data: AppConfigFile = {
        app: "infinite-canvas",
        version: 1,
        exportedAt: new Date().toISOString(),
        config: portableConfig,
        webdav: { url: webdav.url, username: webdav.username, directory: webdav.directory, lastSyncedAt: "" },
        promptSources: { sources, schedule },
    };
    saveAs(new Blob([JSON.stringify(data, null, 2)], { type: "application/json;charset=utf-8" }), "infinite-canvas-config.json");
}

export async function importAppConfig(file: File) {
    let data: AppConfigFile;
    try {
        data = JSON.parse(await file.text()) as AppConfigFile;
    } catch {
        throw new Error(i18n.t("config.invalidFile"));
    }
    if (!data || data.app !== "infinite-canvas" || data.version !== 1 || !data.config || typeof data.config !== "object" || !data.webdav || !data.promptSources || !Array.isArray(data.promptSources.sources) || !data.promptSources.schedule) throw new Error(i18n.t("config.invalidFile"));
    const current = useConfigStore.getState();
    const preferences = Object.fromEntries(
        PORTABLE_CONFIG_KEYS.filter((key) => typeof data.config[key] === typeof current.config[key]).map((key) => [key, data.config[key]]),
    ) as Partial<PortableConfig>;
    const webdav = { ...current.webdav, password: "", lastSyncedAt: "" };
    for (const key of ["url", "username", "directory"] as const) {
        if (typeof data.webdav[key] === "string") webdav[key] = data.webdav[key];
    }
    useConfigStore.setState({ config: { ...current.config, ...preferences }, webdav });
    usePromptSourceStore.setState(data.promptSources);
}
