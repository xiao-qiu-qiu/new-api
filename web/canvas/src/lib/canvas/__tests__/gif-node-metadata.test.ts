import { afterAll, beforeEach, expect, test, vi } from "vitest";
import type { CanvasNodeData } from "@/types/canvas";
import type { AiConfig } from "@/stores/use-config-store";

const blobs = vi.hoisted(() => new Map<string, Blob>());
vi.mock("file-saver", () => ({ saveAs: vi.fn() }));
vi.mock("@/services/image-storage", () => ({
    getImageBlob: vi.fn(async (key: string) => blobs.get(key)),
    resolveImageUrl: vi.fn(async (key: string, fallback = "") => blobs.has(key) ? `blob:restored-${key}` : fallback),
    ensureImagePreview: vi.fn(async () => undefined),
    uploadImage: vi.fn(),
}));
vi.mock("@/lib/localforage-storage", () => ({ localForageStorage: { getItem: async () => null, setItem: async () => undefined, removeItem: async () => undefined } }));

const originalWindow = Object.getOwnPropertyDescriptor(globalThis, "window");
Object.defineProperty(globalThis, "window", { configurable: true, value: {
    __newApiCanvasHost: { context: { userId: 7, channels: [], theme: "light", language: "en-US" } },
} });
afterAll(() => {
    if (originalWindow) Object.defineProperty(globalThis, "window", originalWindow);
    else Reflect.deleteProperty(globalThis, "window");
});

const { defaultConfig } = await import("@/stores/use-config-store");
const { createCanvasNode, applyNodeConfigPatch, buildImageGenerationMetadata, imageMetadata } = await import("../canvas-node-factory");
const { buildGenerationConfig, hydrateCanvasImages, resetInterruptedGeneration, resolveMetadataReferences, imageExtension, downloadImage } = await import("../canvas-generation-helpers");
const { exportCanvasProjects } = await import("../canvas-export");
const { useCanvasStore } = await import("@/stores/canvas/use-canvas-store");
const { CanvasNodeType } = await import("@/types/canvas");
const { readZip } = await import("@/lib/zip");
const { saveAs } = await import("file-saver");
const gifConfig: AiConfig = { ...defaultConfig, imageOutputFormat: "gif", imageFrameCount: "64", imageFrameDurationMs: "205" };
const gifOptions = { imageOutputFormat: "gif", imageFrameCount: "64", imageFrameDurationMs: "205" };

beforeEach(() => {
    blobs.clear();
    vi.mocked(saveAs).mockClear();
    useCanvasStore.setState({ projects: [], deletedProjects: [] });
});

test("GIF node options survive creation, parameter changes, generation and retry after restoring history", () => {
    const configNode = createCanvasNode(CanvasNodeType.Config, { x: 0, y: 0 }, gifOptions);
    const patched = applyNodeConfigPatch(configNode, { imageFrameCount: "16" });
    const generation = buildGenerationConfig(defaultConfig, patched, "image");
    expect(generation).toMatchObject({ ...gifOptions, imageFrameCount: "16" });
    const metadata = buildImageGenerationMetadata("generation", generation, 1, []);
    const result = createCanvasNode(CanvasNodeType.Image, { x: 0, y: 0 }, { ...metadata, status: "loading" });
    const [restored] = resetInterruptedGeneration(JSON.parse(JSON.stringify([result])) as CanvasNodeData[]);
    expect(buildGenerationConfig(defaultConfig, restored, "image")).toMatchObject({ ...gifOptions, imageFrameCount: "16" });
});

test("new PNG nodes keep the existing format and GIF frame defaults", () => {
    const node = createCanvasNode(CanvasNodeType.Image, { x: 0, y: 0 });
    expect(node.metadata).toMatchObject({ imageOutputFormat: "png", imageFrameCount: "9", imageFrameDurationMs: "200" });
    expect(buildGenerationConfig(defaultConfig, node, "image")).toMatchObject({ imageOutputFormat: "png", imageFrameCount: "9", imageFrameDurationMs: "200" });
});

test("GIF project export and import preserve metadata, MIME, extension and original animation bytes", async () => {
    const bytes = Uint8Array.from(atob("R0lGODlhAQABAIAAAAAAAP///yH5BAAUAAAAIf8LTkVUU0NBUEUyLjADAQAAACwAAAAAAQABAAACAUwAIfkEABQAAAAsAAAAAAEAAQAAAgFEADs="), (char) => char.charCodeAt(0));
    const blob = new Blob([bytes], { type: "image/gif" });
    blobs.set("image:gif", blob);
    const node = createCanvasNode(CanvasNodeType.Image, { x: 0, y: 0 }, {
        ...buildImageGenerationMetadata("generation", gifConfig, 1, []),
        ...imageMetadata({ url: "blob:original", storageKey: "image:gif", width: 1, height: 1, bytes: blob.size, mimeType: "image/gif" }),
    });
    const projectId = useCanvasStore.getState().importProject({ nodes: [node] });
    const project = useCanvasStore.getState().openProject(projectId);
    if (!project) throw new Error("Missing imported project");
    await exportCanvasProjects([project]);
    const files = await readZip(vi.mocked(saveAs).mock.calls[0][0] as Blob);
    const manifest = files.get("projects.json");
    if (!manifest) throw new Error("Missing project manifest");
    const exported = JSON.parse(await manifest.text());
    const asset = exported.projects[0].files[0];
    expect(asset).toMatchObject({ mimeType: "image/gif", bytes: blob.size });
    expect(asset.path).toMatch(/\.gif$/);
    const exportedImage = files.get(asset.path);
    if (!exportedImage) throw new Error("Missing exported GIF");
    expect(new Uint8Array(await exportedImage.arrayBuffer())).toEqual(bytes);
    const restoredId = useCanvasStore.getState().importProject(exported.projects[0].project);
    const restoredProject = useCanvasStore.getState().openProject(restoredId);
    if (!restoredProject) throw new Error("Missing restored project");
    const [restored] = await hydrateCanvasImages(restoredProject.nodes);
    expect(restored.metadata).toMatchObject({ ...gifOptions, content: "blob:restored-image:gif", mimeType: "image/gif" });
    expect(buildGenerationConfig(defaultConfig, restored, "image")).toMatchObject(gifOptions);
    const references = await resolveMetadataReferences({ generationType: "edit", referenceImages: [{ storageKey: "image:gif" }] });
    expect(references).toMatchObject([{ name: "reference-0.gif", type: "image/gif", dataUrl: "blob:restored-image:gif" }]);
});

test("a GIF blob URL downloads with its MIME extension instead of the PNG fallback", () => {
    expect(imageExtension("blob:https://example.invalid/random", "image/gif")).toBe("gif");
    expect(imageExtension("https://example.invalid/image.png", "image/gif; charset=binary")).toBe("gif");
    expect(imageExtension("blob:png", "image/png")).toBe("png");
    expect(imageExtension("data:image/gif;base64,R0lGODlh")).toBe("gif");
    expect(imageExtension("blob:legacy")).toBe("png");
    downloadImage("blob:original-animation", "canvas-image-result", "image/gif");
    expect(saveAs).toHaveBeenLastCalledWith("blob:original-animation", "canvas-image-result.gif");
    downloadImage("blob:original-png", "image-1", "image/png");
    expect(saveAs).toHaveBeenLastCalledWith("blob:original-png", "image-1.png");
});
