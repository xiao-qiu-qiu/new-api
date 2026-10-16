import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import type { AiConfig } from "@/stores/use-config-store";

const storage = vi.hoisted(() => new Map<string, Blob>());
vi.mock("file-saver", () => ({ saveAs: vi.fn() }));
vi.mock("@/services/image-storage", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/image-storage")>()),
  resolveImageUrl: vi.fn(async (key: string) =>
    storage.has(key) ? `blob:restored-${key}` : "",
  ),
  getImageBlob: vi.fn(async (key: string) => storage.get(key)),
}));

const channels = [{
  id: "media", name: "Media", baseUrl: "", apiKey: "", apiFormat: "openai" as const,
  models: ["gpt-image-2", "wanx2.1-imageedit"].map((name) => ({ name, capability: "image" as const })),
}];
const siteFetch = vi.fn<(path: string, init?: RequestInit) => Promise<Response>>();
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
Object.defineProperty(globalThis, "window", { configurable: true, value: {
  __newApiCanvasHost: { context: { userId: 7, channels, theme: "light", language: "en-US" }, fetch: siteFetch },
} });

const { defaultConfig } = await import("@/stores/use-config-store");
const { requestEdit } = await import("@/services/api/image");
const { buildImageGenerationMetadata } = await import("../canvas-node-factory");
const { resolveMetadataReferences, sourceNodeReferenceImages } = await import("../canvas-generation-helpers");
const { collectImageStorageKeys } = await import("@/services/image-storage");
const { exportCanvasProjects } = await import("../canvas-export");
const { readZip } = await import("@/lib/zip");
const { saveAs } = await import("file-saver");
const { CanvasNodeType } = await import("@/types/canvas");
const { selectionToAlphaMask, alphaMaskToWanxMask } = await import("../canvas-image-mask");
const config: AiConfig = { ...defaultConfig, channels, model: "media::gpt-image-2", size: "1024x1024", count: "1", systemPrompt: "" };
const source = { id: "base", name: "base.png", type: "image/png", dataUrl: "data:image/png;base64,YmFzZQ==" };
const mask = { ...source, id: "mask", name: "mask.png", role: "mask" as const, dataUrl: "data:image/png;base64,bWFzaw==" };

beforeEach(() => {
  siteFetch.mockReset().mockResolvedValue(Response.json({ data: [{ b64_json: "aW1hZ2U=" }] }));
  storage.clear();
  vi.mocked(saveAs).mockClear();
});
afterEach(() => vi.unstubAllGlobals());
afterAll(() => {
  if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
  else Reflect.deleteProperty(globalThis, "window");
});

test("an OpenAI masked edit uploads the base and mask in separate multipart fields", async () => {
  await requestEdit(config, "Replace the selected area", [source, mask]);
  const [path, init] = siteFetch.mock.calls[0];
  const form = init?.body as FormData;
  expect(path).toBe("/pg/images/edits");
  expect(form.getAll("image")).toHaveLength(1);
  expect(form.getAll("image[]")).toHaveLength(0);
  expect(form.getAll("mask")).toHaveLength(1);
  expect(await (form.get("mask") as File).text()).toBe("mask");
  expect(String(form.get("prompt"))).not.toContain("Image 2");
  expect(new Headers(init?.headers).has("Content-Type")).toBe(false);
});

test("ordinary multi-image edits retain image[] uploads without a mask", async () => {
  await requestEdit(config, "Combine", [source, { ...source, id: "second" }]);
  const form = siteFetch.mock.calls[0][1]?.body as FormData;
  expect(form.getAll("image[]")).toHaveLength(2);
  expect(form.get("mask")).toBeNull();
});

test("painted and erased selection pixels become transparent edits and opaque preserved pixels", () => {
  const pixels = new Uint8ClampedArray([0, 0, 0, 255, 0, 0, 0, 0, 0, 0, 0, 128]);
  selectionToAlphaMask(pixels);
  expect([...pixels]).toEqual([0, 0, 0, 0, 0, 0, 0, 255, 0, 0, 0, 127]);
  alphaMaskToWanxMask(pixels);
  expect([...pixels]).toEqual([255, 255, 255, 255, 0, 0, 0, 255, 128, 128, 128, 255]);
});

test("a wanx masked edit sends one base image and an opaque white edit mask without size", async () => {
  const pixels = { data: new Uint8ClampedArray([0, 0, 0, 0, 0, 0, 0, 255]) };
  const putImageData = vi.fn();
  vi.stubGlobal("Image", class extends EventTarget {
    naturalWidth = 2;
    naturalHeight = 1;
    set src(_url: string) { queueMicrotask(() => this.dispatchEvent(new Event("load"))); }
  });
  vi.stubGlobal("document", { createElement: () => ({
    getContext: () => ({ drawImage: vi.fn(), getImageData: () => pixels, putImageData }),
    toDataURL: () => "data:image/png;base64,d2hpdGUtbWFzaw==",
  }) });
  await requestEdit({ ...config, model: "media::wanx2.1-imageedit", size: "9:16" }, "Replace", [source, mask]);
  const form = siteFetch.mock.calls[0][1]?.body as FormData;
  expect(form.get("size")).toBeNull();
  expect(form.getAll("image")).toHaveLength(1);
  expect(form.getAll("image[]")).toHaveLength(0);
  expect(await (form.get("mask") as File).text()).toBe("white-mask");
  expect([...putImageData.mock.calls[0][0].data]).toEqual([255, 255, 255, 255, 0, 0, 0, 255]);
});

test("saved mask references survive project export and restore with their mask role", async () => {
  storage.set("image:base", new Blob(["base"], { type: "image/png" }));
  storage.set("image:mask", new Blob(["mask"], { type: "image/png" }));
  const metadata = buildImageGenerationMetadata("edit", config, 1, [
    { ...source, storageKey: "image:base" }, { ...mask, storageKey: "image:mask" },
  ]);
  expect([...collectImageStorageKeys(metadata)].sort()).toEqual(["image:base", "image:mask"]);
  await exportCanvasProjects([{
    id: "project", title: "Mask", createdAt: "2026-10-14", updatedAt: "2026-10-14", viewport: { x: 0, y: 0, k: 1 }, connections: [],
    chatSessions: [], activeChatId: "", backgroundMode: "lines", showImageInfo: false,
    nodes: [{ id: "result", type: CanvasNodeType.Image, title: "Result", position: { x: 0, y: 0 }, width: 1024, height: 1024, metadata }],
  }]);
  const zip = await readZip(vi.mocked(saveAs).mock.calls[0][0] as Blob);
  const projectFile = zip.get("projects.json");
  expect(projectFile).toBeDefined();
  if (!projectFile) throw new Error("Missing projects.json");
  const exported = JSON.parse(await projectFile.text());
  expect(exported.projects[0].files.map((file: { storageKey: string }) => file.storageKey).sort()).toEqual(["image:base", "image:mask"]);
  const restored = await resolveMetadataReferences(exported.projects[0].project.nodes[0].metadata);
  expect(restored?.map((image) => image.role)).toEqual([undefined, "mask"]);
  expect(restored?.map((image) => image.storageKey)).toEqual(["image:base", "image:mask"]);
});

test("missing saved mask assets stop retry rather than reverting to a multi-image edit", async () => {
  storage.set("image:base", new Blob(["base"]));
  const metadata = buildImageGenerationMetadata("edit", config, 1, [
    { ...source, storageKey: "image:base" }, { ...mask, storageKey: "image:missing" },
  ]);
  expect(await resolveMetadataReferences(metadata)).toBeNull();
  expect(siteFetch).not.toHaveBeenCalled();
});

test("a visible overlay node supplies its saved mask asset when used as a generation reference", () => {
  const references = sourceNodeReferenceImages({
    id: "mask-node", type: CanvasNodeType.Image, title: "Mask", position: { x: 0, y: 0 }, width: 1024, height: 1024,
    metadata: { content: "blob:blue-overlay", storageKey: "image:overlay", maskReference: { storageKey: "image:mask", role: "mask" } },
  });
  expect(references).toMatchObject([{ role: "mask", dataUrl: "", storageKey: "image:mask" }]);
});
