// @vitest-environment jsdom
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  fireEvent,
  getByRole,
  getByText,
  queryByText,
} from "@testing-library/dom";
import { App } from "antd";

const fixtures = vi.hoisted(() => ({
  logs: new Map<string, Record<string, unknown>>(),
  wait: vi.fn(),
  create: vi.fn(),
  storeVideo: vi.fn(),
  writeLog: vi.fn(),
  config: {
    model: "videos::H3-KS",
    videoModel: "videos::H3-KS",
    size: "1920x1080",
    vquality: "1080",
    videoSeconds: "15",
    videoGenerateAudio: "true",
    videoWatermark: "false",
    videoMode: "reference",
    channels: [],
  },
  t: (key: string) => key,
}));

vi.mock("@/lib/site-storage", () => ({
  getUserStorageKey: (key: string) => key,
  createUserLocalForage: () => ({
    iterate: async (callback: (value: Record<string, unknown>) => void) =>
      fixtures.logs.forEach((value) => callback(structuredClone(value))),
    setItem: async (key: string, value: Record<string, unknown>) => {
      await fixtures.writeLog();
      fixtures.logs.set(key, structuredClone(value));
    },
    removeItem: async (key: string) => {
      fixtures.logs.delete(key);
    },
  }),
}));
vi.mock("@/services/api/video", () => ({
  createVideoGenerationTask: fixtures.create,
  waitForVideoGenerationTask: fixtures.wait,
  pollVideoGenerationTask: vi.fn(),
  storeGeneratedVideo: fixtures.storeVideo,
  isVideoTaskFailed: (error: Error) => error.name === "VideoTaskFailed",
  isVideoTaskDownloadFailed: (error: Error) =>
    error.name === "VideoTaskDownloadError",
}));
vi.mock("@/services/file-storage", () => ({
  deleteStoredMedia: vi.fn(),
  resolveMediaUrl: async (_key: string, fallback: string) =>
    fallback || "blob:recovered",
}));
vi.mock("@/services/image-storage", () => ({
  resolveImageUrl: vi.fn(),
  ensureImagePreview: vi.fn(),
  getImagePreviewRevision: () => 0,
  previewUrlFor: vi.fn(),
  subscribeImagePreviews: () => () => undefined,
  uploadImage: vi.fn(),
}));
vi.mock("@/components/canvas/asset-picker-modal", () => ({
  AssetPickerModal: () => null,
}));
vi.mock("@/components/model-picker", () => ({ ModelPicker: () => null }));
vi.mock("@/components/prompts/prompt-select-dialog", () => ({
  PromptSelectDialog: () => null,
}));
vi.mock("@/components/video-settings-panel", () => ({
  VideoSettingsPanel: () => null,
  normalizeVideoResolutionValue: (value: string) => value.replace(/p$/, ""),
  normalizeVideoSizeValue: (value: string) => value,
  videoModeLabel: (value: string) => value,
  videoSizeLabel: (value: string) => value,
}));
vi.mock("@/stores/use-config-store", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/stores/use-config-store")>()),
  useEffectiveConfig: () => fixtures.config,
  useConfigStore: (select: (value: unknown) => unknown) =>
    select({
      config: fixtures.config,
      updateConfig: vi.fn(),
      isAiConfigReady: () => true,
      openConfigDialog: vi.fn(),
    }),
  boolConfig: (_value: string, fallback: boolean) => fallback,
  modelOptionLabel: (_config: unknown, model: string) => model,
}));
vi.mock("@/stores/use-asset-store", () => ({
  useAssetStore: (select: (value: unknown) => unknown) =>
    select({ addAsset: vi.fn() }),
}));
vi.mock("@/stores/use-workbench-agent-store", () => ({
  useWorkbenchAgentStore: (select: (value: unknown) => unknown) =>
    select({
      videoCommand: null,
      clearVideoCommand: vi.fn(),
      updateTask: vi.fn(),
    }),
}));
vi.mock("@/stores/use-theme-store", () => ({
  useThemeStore: (select: (value: unknown) => unknown) =>
    select({ theme: "light" }),
}));
vi.mock("@/lib/canvas-theme", () => ({ canvasThemes: { light: {} } }));
vi.mock("@/i18n", () => ({
  default: { t: fixtures.t, language: "en-US", resolvedLanguage: "en-US" },
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: fixtures.t }) }));

const { default: VideoPage } = await import("../index");
let container: HTMLDivElement;
let root: Root;

function storedTask(status = "failed") {
  return {
    id: "log-original",
    createdAt: Date.now() - 428_000,
    title: "Original video",
    prompt: "Original prompt",
    time: "22:32",
    model: "videos::H3-KS",
    config: { ...fixtures.config },
    references: [],
    durationMs: 428_000,
    size: "1920x1080",
    resolution: "1080",
    seconds: "15",
    status,
    task: {
      id: "task-original",
      provider: "openai",
      model: "H3-KS",
      group: "videos",
    },
    error: "videoWorkbench.timeout",
  };
}

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  window.matchMedia = vi.fn().mockReturnValue({
    matches: false,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  });
  fixtures.logs.clear();
  fixtures.wait.mockReset();
  fixtures.create.mockReset();
  fixtures.storeVideo.mockReset();
  fixtures.writeLog.mockReset();
  fixtures.writeLog.mockResolvedValue(undefined);
  fixtures.storeVideo.mockResolvedValue({
    url: "blob:recovered",
    storageKey: "video:recovered",
    bytes: 100,
    mimeType: "video/mp4",
    width: 1536,
    height: 864,
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

test("opening the workbench retrieves an old timed-out task without generating another video", async () => {
  fixtures.logs.set("log-original", storedTask());
  fixtures.wait.mockImplementation(async (_config, _task, options) => {
    await options.onTaskStateChange("completed");
    return { blob: new Blob(["video"], { type: "video/mp4" }) };
  });
  await act(async () =>
    root.render(createElement(App, null, createElement(VideoPage))),
  );
  expect(fixtures.wait).toHaveBeenCalledWith(
    expect.anything(),
    expect.objectContaining({ id: "task-original", group: "videos" }),
    expect.anything(),
  );
  expect(fixtures.create).not.toHaveBeenCalled();
  expect(fixtures.logs.get("log-original")).toMatchObject({
    status: "success",
    task: { id: "task-original" },
    video: { storageKey: "video:recovered" },
  });
  expect(container.querySelector("video")?.src).toBe("blob:recovered");
});

test("a query interruption keeps the task recoverable and the status action queries the same task", async () => {
  fixtures.logs.set("log-original", storedTask("pending"));
  fixtures.wait.mockRejectedValueOnce(new Error("HTTP 403"));
  await act(async () =>
    root.render(createElement(App, null, createElement(VideoPage))),
  );
  expect(fixtures.logs.get("log-original")).toMatchObject({
    status: "pending",
    task: { id: "task-original" },
  });
  expect(
    getByText(
      container,
      "Status check interrupted. The task continues in the background.",
    ),
  ).toBeTruthy();
  fixtures.wait.mockImplementationOnce(async (_config, _task, options) => {
    await options.onTaskStateChange("completed");
    return { blob: new Blob(["video"]) };
  });
  await act(async () =>
    fireEvent.click(
      getByRole(container, "button", { name: "Check task status" }),
    ),
  );
  expect(fixtures.wait).toHaveBeenCalledTimes(2);
  expect(fixtures.wait.mock.calls[1][1].id).toBe("task-original");
  expect(fixtures.create).not.toHaveBeenCalled();
  expect(fixtures.logs.get("log-original")?.status).toBe("success");
});

test.each(["download", "local storage"])(
  "a completed video's %s failure only offers retrieval of the original task",
  async (failure) => {
    fixtures.logs.set("log-original", storedTask("pending"));
    fixtures.wait.mockImplementation(async (_config, _task, options) => {
      await options.onTaskStateChange("completed");
      if (failure === "download") {
        throw Object.assign(new Error("download interrupted"), {
          name: "VideoTaskDownloadError",
        });
      }
      return { blob: new Blob(["video"]) };
    });
    if (failure === "local storage") {
      fixtures.storeVideo.mockRejectedValueOnce(
        new DOMException("Full", "QuotaExceededError"),
      );
    }
    await act(async () =>
      root.render(createElement(App, null, createElement(VideoPage))),
    );
    expect(fixtures.logs.get("log-original")).toMatchObject({
      status: "completed",
      task: { id: "task-original" },
    });
    expect(
      getByText(
        container,
        "Video generated, but retrieval failed. Retrieve the video again.",
      ),
    ).toBeTruthy();
    expect(
      getByRole(container, "button", { name: "Retrieve video" }),
    ).toBeTruthy();
    expect(queryByText(container, "workbench.failed")).toBeNull();
    fixtures.wait.mockImplementationOnce(async (_config, _task, options) => {
      await options.onTaskStateChange("completed");
      return { blob: new Blob(["video"]) };
    });
    await act(async () =>
      fireEvent.click(
        getByRole(container, "button", { name: "Retrieve video" }),
      ),
    );
    expect(fixtures.create).not.toHaveBeenCalled();
    expect(fixtures.wait.mock.calls[1][1].id).toBe("task-original");
    expect(fixtures.logs.get("log-original")?.status).toBe("success");
  },
);

test("leaving the page aborts its wait while preserving the durable task", async () => {
  fixtures.logs.set("log-original", storedTask("pending"));
  let signal: AbortSignal | undefined;
  fixtures.wait.mockImplementation((_config, _task, options) => {
    signal = options.signal;
    return new Promise((_resolve, reject) =>
      options.signal.addEventListener(
        "abort",
        () => reject(new DOMException("Aborted", "AbortError")),
        { once: true },
      ),
    );
  });
  await act(async () =>
    root.render(createElement(App, null, createElement(VideoPage))),
  );
  await act(async () => root.render(createElement(App)));
  expect(signal?.aborted).toBe(true);
  expect(fixtures.logs.get("log-original")).toMatchObject({
    status: "pending",
    task: { id: "task-original" },
  });
  expect(fixtures.create).not.toHaveBeenCalled();
});

test("an actual backend failure stays failed and only Generate again creates a new task", async () => {
  const audios = [
    {
      id: "audio-original",
      name: "original.mp3",
      type: "audio/mpeg",
      url: "data:audio/mpeg;base64,YXVkaW8=",
      storageKey: "",
    },
  ];
  fixtures.logs.set("log-original", { ...storedTask("pending"), audios });
  fixtures.wait.mockRejectedValueOnce(
    Object.assign(new Error("Upstream rejected the task"), {
      name: "VideoTaskFailed",
    }),
  );
  await act(async () =>
    root.render(createElement(App, null, createElement(VideoPage))),
  );
  expect(fixtures.logs.get("log-original")).toMatchObject({
    status: "failed",
    taskFailed: true,
    task: { id: "task-original" },
  });
  expect(fixtures.wait).toHaveBeenCalledTimes(1);
  fixtures.create.mockResolvedValueOnce({
    id: "task-new",
    provider: "openai",
    model: "H3-KS",
    group: "videos",
  });
  fixtures.wait.mockImplementationOnce(
    (_config, _task, options) =>
      new Promise((_resolve, reject) =>
        options.signal.addEventListener(
          "abort",
          () => reject(new DOMException("Aborted", "AbortError")),
          { once: true },
        ),
      ),
  );
  await act(async () =>
    fireEvent.click(getByRole(container, "button", { name: "Generate again" })),
  );
  expect(fixtures.create).toHaveBeenCalledOnce();
  expect(fixtures.create.mock.calls[0][1]).toBe("Original prompt");
  expect(fixtures.create.mock.calls[0][3]).toEqual({ audios });
  expect(fixtures.wait.mock.calls[1][1].id).toBe("task-new");
});

test("recovering another history entry does not replace the selected task's result", async () => {
  fixtures.logs.set("log-original", storedTask("pending"));
  fixtures.logs.set("log-older", {
    ...storedTask("pending"),
    id: "log-older",
    createdAt: Date.now() - 500_000,
    task: {
      id: "task-older",
      provider: "openai",
      model: "H3-KS",
      group: "videos",
    },
  });
  fixtures.wait.mockImplementation(async (_config, task, options) => {
    if (task.id === "task-original") {
      return new Promise((_resolve, reject) =>
        options.signal.addEventListener(
          "abort",
          () => reject(new DOMException("Aborted", "AbortError")),
          { once: true },
        ),
      );
    }
    await options.onTaskStateChange("completed");
    return { blob: new Blob(["older video"]) };
  });
  await act(async () =>
    root.render(createElement(App, null, createElement(VideoPage))),
  );
  expect(fixtures.logs.get("log-older")?.status).toBe("success");
  expect(container.querySelector("video")).toBeNull();
  expect(getByRole(container, "status").textContent).toBe(
    "workbench.generating",
  );
  expect(fixtures.create).not.toHaveBeenCalled();
});

test("a task-history write failure does not prevent retrieving an already generated video", async () => {
  fixtures.logs.set("log-original", storedTask("pending"));
  fixtures.writeLog.mockRejectedValue(
    new DOMException("History storage blocked", "QuotaExceededError"),
  );
  fixtures.wait.mockImplementationOnce(async (_config, _task, options) => {
    await options.onTaskStateChange("completed");
    return { blob: new Blob(["video"]) };
  });
  await act(async () =>
    root.render(createElement(App, null, createElement(VideoPage))),
  );
  expect(container.querySelector("video")?.src).toBe("blob:recovered");
  expect(fixtures.storeVideo).toHaveBeenCalledOnce();
  expect(fixtures.create).not.toHaveBeenCalled();
});

test("a submission failure does not replace a history video selected while that submission was pending", async () => {
  fixtures.logs.set("log-saved", {
    ...storedTask("success"),
    id: "log-saved",
    title: "Saved video",
    video: {
      id: "video-saved",
      url: "blob:saved",
      storageKey: "video:saved",
      bytes: 200,
      mimeType: "video/mp4",
      width: 1536,
      height: 864,
      durationMs: 1000,
    },
  });
  const submission = Promise.withResolvers<unknown>();
  fixtures.create.mockReturnValueOnce(submission.promise);
  await act(async () =>
    root.render(createElement(App, null, createElement(VideoPage))),
  );
  await act(async () =>
    fireEvent.change(getByRole(container, "textbox"), {
      target: { value: "New request" },
    }),
  );
  await act(async () =>
    fireEvent.click(
      getByRole(container, "button", { name: "workbench.generate" }),
    ),
  );
  expect(fixtures.create).toHaveBeenCalledOnce();
  await act(async () => fireEvent.click(getByText(container, "Saved video")));
  expect(container.querySelector("video")?.src).toBe("blob:saved");
  await act(async () => submission.reject(new Error("Submission rejected")));
  expect(container.querySelector("video")?.src).toBe("blob:saved");
});

test("deleting old history preserves a new task whose browser storage write failed", async () => {
  fixtures.logs.set("log-old", {
    ...storedTask("success"),
    id: "log-old",
    title: "Old video",
  });
  fixtures.writeLog.mockRejectedValue(
    new DOMException("Full", "QuotaExceededError"),
  );
  fixtures.create.mockResolvedValue({
    id: "task-memory",
    provider: "openai",
    model: "H3-KS",
    group: "videos",
  });
  fixtures.wait.mockImplementation(async (_config, _task, options) => {
    await options.onTaskStateChange("completed");
    return { blob: new Blob(["video"]) };
  });
  fixtures.storeVideo.mockRejectedValueOnce(
    new DOMException("Full", "QuotaExceededError"),
  );
  await act(async () =>
    root.render(createElement(App, null, createElement(VideoPage))),
  );
  await act(async () =>
    fireEvent.change(getByRole(container, "textbox"), {
      target: { value: "New video" },
    }),
  );
  await act(async () =>
    fireEvent.click(
      getByRole(container, "button", { name: "workbench.generate" }),
    ),
  );
  expect(
    getByRole(container, "button", { name: "Retrieve video" }),
  ).toBeTruthy();
  const oldLog = getByRole(container, "button", { name: /Old video/ });
  await act(async () => fireEvent.click(getByRole(oldLog, "checkbox")));
  await act(async () =>
    fireEvent.click(getByRole(container, "button", { name: "common.delete" })),
  );
  await act(async () =>
    fireEvent.click(
      getByRole(getByRole(document.body, "dialog"), "button", {
        name: "common.delete",
      }),
    ),
  );
  expect(queryByText(container, "Old video")).toBeNull();
  await act(async () =>
    fireEvent.click(getByRole(container, "button", { name: "Retrieve video" })),
  );
  expect(fixtures.wait).toHaveBeenCalledTimes(2);
  expect(fixtures.wait.mock.calls[1][1].id).toBe("task-memory");
  expect(fixtures.create).toHaveBeenCalledOnce();
  expect(container.querySelector("video")?.src).toBe("blob:recovered");
});
