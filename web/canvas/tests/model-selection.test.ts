import { afterAll, expect, test } from "bun:test";
import type { SiteContext } from "../src/integration/site-context";
import type { AiConfig, ModelCapability } from "../src/stores/use-config-store";
import type { CanvasNodeData } from "../src/types/canvas";

// Config and i18n need an initialized site context before their dynamic imports.
const values = new Map<string, string>();
const originalStorage = Object.getOwnPropertyDescriptor(
  globalThis,
  "localStorage",
);
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
const storage = {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
};
const siteContext: SiteContext = {
  userId: 1,
  theme: "light",
  language: "en-US",
  group: "replacement",
  model: "video-b",
  agentGroup: "replacement",
  agentModel: "text-a",
  channels: [],
};
Object.defineProperty(globalThis, "localStorage", {
  configurable: true,
  value: storage,
});
Object.defineProperty(globalThis, "window", {
  configurable: true,
  value: {
    localStorage: storage,
    __newApiCanvasHost: { context: siteContext },
  },
});
afterAll(() => {
  if (originalStorage) {
    Object.defineProperty(globalThis, "localStorage", originalStorage);
  } else Reflect.deleteProperty(globalThis, "localStorage");
  if (originalWindow) {
    Object.defineProperty(globalThis, "window", originalWindow);
  } else Reflect.deleteProperty(globalThis, "window");
});

const { defaultConfig, resolveModelForCapability, useConfigStore } =
  await import("../src/stores/use-config-store");
const { buildGenerationConfig } =
  await import("../src/lib/canvas/canvas-generation-helpers");
const { CanvasNodeType } = await import("../src/types/canvas");
const { getVideoModelConstraints, normalizeVideoConfig, validateVideoReferenceCounts } = await import("../src/lib/video-model-constraints");
const { getVideoReferenceSelectionDisabledNodes } = await import("../src/components/canvas/canvas-node-generation");

test("video constraints normalize stale model parameters and preserve zero reference limits", () => {
  const restricted: AiConfig = {
    ...defaultConfig,
    model: "restricted::video",
    channels: [{ id: "restricted", name: "Restricted", baseUrl: "", apiKey: "", apiFormat: "openai", models: [{ name: "video", capability: "video", videoCapabilities: { resolutions: ["720p", "1080P"], aspect_ratios: ["2:3"], durations: [5, 10], max_duration_seconds: 8, max_reference_images: 1, max_reference_audio: 0 } }] }],
    vquality: "480",
    size: "1920x1080",
    videoSeconds: "30",
  };
  expect(normalizeVideoConfig(restricted)).toMatchObject({ vquality: "720", size: "720x1080", videoSeconds: "5" });
  expect(getVideoModelConstraints(restricted)).toMatchObject({ maxReferenceAudio: 0, durations: [5], maxDurationSeconds: 8 });
  expect(() => validateVideoReferenceCounts(restricted, { images: 1, audios: 0, videos: 0 })).not.toThrow();
  expect(() => validateVideoReferenceCounts(restricted, { images: 1, audios: 1, videos: 0 })).toThrow();
  expect(normalizeVideoConfig({ ...restricted, vquality: "1080", size: "1080x1620" })).toMatchObject({ vquality: "1080", size: "1080x1620" });
});

test("unconfigured video models retain the existing duration and free resolution defaults", () => {
  const config = { ...defaultConfig, vquality: "864", size: "1536x864", videoSeconds: "60" };
  expect(normalizeVideoConfig(config)).toMatchObject({ vquality: "864", size: "1536x864", videoSeconds: "30" });
  expect(getVideoModelConstraints(config).maxReferenceAudio).toBeUndefined();
});

test("video reference selection disables a full image slot and an explicitly unsupported audio group", () => {
  const config: AiConfig = { ...defaultConfig, model: "media::video", channels: [{ id: "media", name: "Media", baseUrl: "", apiKey: "", apiFormat: "openai", models: [{ name: "video", capability: "video", videoCapabilities: { max_reference_images: 1, max_reference_audio: 0 } }] }] };
  const node = (id: string, type: CanvasNodeData["type"], groupId?: string): CanvasNodeData => ({ id, type, title: id, position: { x: 0, y: 0 }, width: 100, height: 100, metadata: { content: type === "image" ? "data:image/png;base64,AA==" : "https://media.example/file", groupId } });
  const nodes = [node("target", CanvasNodeType.Video), node("first", CanvasNodeType.Image), node("second", CanvasNodeType.Image), node("group", CanvasNodeType.Group), node("audio", CanvasNodeType.Audio, "group"), node("text", CanvasNodeType.Text)];
  const disabled = getVideoReferenceSelectionDisabledNodes(config, "target", nodes, [{ id: "connection", fromNodeId: "first", toNodeId: "target" }]);
  expect(disabled.has("second")).toBe(true);
  expect(disabled.has("audio")).toBe(true);
  expect(disabled.has("group")).toBe(true);
  expect(disabled.has("text")).toBe(false);
});

const config: AiConfig = {
  ...defaultConfig,
  videoModel: "replacement::video-b",
  channels: [
    {
      id: "replacement",
      name: "Replacement",
      baseUrl: "https://example.com",
      apiKey: "",
      apiFormat: "openai",
      models: [
        { name: "video-b", capability: "video" },
        { name: "video-a", capability: "video" },
        { name: "image-a", capability: "image" },
      ],
    },
  ],
};
const ready = useConfigStore.getState().isAiConfigReady;
const videoNode = (model: string): CanvasNodeData => ({
  id: "saved-video",
  type: CanvasNodeType.Video,
  title: "Saved video",
  position: { x: 0, y: 0 },
  width: 320,
  height: 180,
  metadata: {
    model,
    videoTaskId: "original-task",
    videoTaskProvider: "openai",
    seconds: "4",
  },
});

test("a saved video on a removed provider is not replaced by the default video model", () => {
  const selected = buildGenerationConfig(
    config,
    videoNode("removed::video-a"),
    "video",
  );
  expect(selected.model).toBe("removed::video-a");
  expect(ready(selected, selected.model)).toBe(false);
});

test("removing a model from an existing provider prevents retry even with valid credentials", () => {
  const selected = buildGenerationConfig(
    config,
    videoNode("replacement::deleted-video"),
    "video",
  );
  expect(selected.model).toBe("replacement::deleted-video");
  expect(ready(selected, selected.model)).toBe(false);
});

test("task recovery rejects a missing provider instead of querying the first provider", () => {
  expect(ready(config, "removed::video-a")).toBe(false);
  expect(ready(config, "unknown-video")).toBe(false);
});

test("an explicitly selected unavailable default is not replaced by the built-in default", () => {
  const selected = buildGenerationConfig(
    { ...config, videoModel: "removed::video-a" },
    undefined,
    "video",
  );
  expect(selected.model).toBe("removed::video-a");
  expect(ready(selected, selected.model)).toBe(false);
});

test("a new node without a selection still uses its configured default", () => {
  const selected = buildGenerationConfig(config, undefined, "video");
  expect(selected.model).toBe("replacement::video-b");
  expect(ready(selected, selected.model)).toBe(true);
});

test("switching generation capability still selects a model of the new capability", () => {
  expect(
    resolveModelForCapability(config, "replacement::image-a", "video"),
  ).toBe("replacement::video-b");
});

test("changing credentials preserves site model readiness and the saved task", () => {
  const node = videoNode("replacement::video-a");
  const selected = buildGenerationConfig(
    { ...config, channels: [{ ...config.channels[0], apiKey: "rotated-key" }] },
    node,
    "video",
  );
  expect(selected.model).toBe("replacement::video-a");
  expect(ready(selected, selected.model)).toBe(true);
  expect(node.metadata?.videoTaskId).toBe("original-task");
  expect(
    ready(
      { ...selected, channels: [{ ...config.channels[0], apiKey: "" }] },
      selected.model,
    ),
  ).toBe(true);
});

test("unavailable explicit selections are preserved for every generation capability", () => {
  for (const capability of [
    "image",
    "video",
    "text",
    "audio",
  ] as ModelCapability[]) {
    expect(
      resolveModelForCapability(config, `removed::${capability}`, capability),
    ).toBe(`removed::${capability}`);
  }
});
