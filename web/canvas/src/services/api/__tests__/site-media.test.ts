import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import type { SiteContext } from "@/integration/site-context";
import type { AiConfig } from "@/stores/use-config-store";

const channels = [
  {
    id: "media group",
    name: "Media",
    baseUrl: "https://unused.example",
    apiKey: "",
    apiFormat: "openai" as const,
    models: [
      {
        name: "gpt-image-2",
        capability: "image" as const,
        capabilities: ["image", "text"] as Array<"image" | "text">,
      },
      { name: "gpt-image-2", capability: "image" as const },
      { name: "sora-2", capability: "video" as const },
      { name: "tts-1", capability: "audio" as const },
    ],
  },
];
const context: SiteContext = {
  userId: 7,
  group: "media group",
  model: "gpt-image-2",
  agentGroup: "media group",
  agentModel: "gpt-image-2",
  theme: "light",
  language: "en-US",
  channels,
};
const siteFetch =
  vi.fn<(path: string, init?: RequestInit) => Promise<Response>>();
const values = new Map<string, string>();
const storage = {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
};
// Bun's Vitest compatibility supports test/mock, but does not implement stubGlobal.
const windowEvents = new EventTarget();
const documentEvents = new EventTarget();
const globals: Record<string, unknown> = {
  window: {
    __newApiCanvasHost: { context, fetch: siteFetch, selectModel: vi.fn() },
    localStorage: storage,
    addEventListener: vi.fn(windowEvents.addEventListener.bind(windowEvents)),
    removeEventListener: vi.fn(
      windowEvents.removeEventListener.bind(windowEvents),
    ),
    dispatchEvent: windowEvents.dispatchEvent.bind(windowEvents),
  },
  document: {
    visibilityState: "visible",
    addEventListener: vi.fn(
      documentEvents.addEventListener.bind(documentEvents),
    ),
    removeEventListener: vi.fn(
      documentEvents.removeEventListener.bind(documentEvents),
    ),
    dispatchEvent: documentEvents.dispatchEvent.bind(documentEvents),
  },
  localStorage: storage,
  fetch: vi.fn(() => {
    throw new Error("Unexpected direct external fetch");
  }),
};
const originals = Object.keys(globals).map((key) => ({
  key,
  descriptor: Object.getOwnPropertyDescriptor(globalThis, key),
}));
for (const [key, value] of Object.entries(globals)) {
  Object.defineProperty(globalThis, key, { configurable: true, value });
}

const { defaultConfig } = await import("@/stores/use-config-store");
const {
  requestGeneration,
  requestEdit,
  requestImageQuestion,
  fetchChannelModels,
} = await import("../image");
const {
  createVideoGenerationTask,
  pollVideoGenerationTask,
  waitForVideoGenerationTask,
  isVideoTaskFailed,
  isVideoTaskDownloadFailed,
} = await import("../video");
const { requestAudioGeneration } = await import("../audio");
const { requestSiteMediaJson, SiteMediaHttpError } =
  await import("../site-media");
const config: AiConfig = {
  ...defaultConfig,
  channels,
  model: "media group::gpt-image-2",
  apiKey: "",
  baseUrl: "",
  proxyEnabled: true,
  quality: "high",
  size: "1024x1024",
  count: "2",
  systemPrompt: "Be concise.",
};
const reference = {
  id: "reference",
  name: "ref.png",
  type: "image/png",
  dataUrl: "data:image/png;base64,aW1hZ2U=",
};
const json = (value: unknown, status = 200) => Response.json(value, { status });

function videoContractConfig(
  model: string,
  overrides: Partial<AiConfig> = {},
): AiConfig {
  return {
    ...config,
    ...overrides,
    model: `media group::${model}`,
    size: "1280x720",
    videoSeconds: "6",
    channels: [
      {
        ...channels[0],
        models: [...channels[0].models, { name: model, capability: "video" }],
      },
    ],
  };
}

beforeEach(() => {
  siteFetch.mockReset();
  values.clear();
});
afterEach(() => {
  vi.useRealTimers();
  Object.assign(document, { visibilityState: "visible" });
});
afterAll(() => {
  for (const original of originals) {
    if (original.descriptor) {
      Object.defineProperty(globalThis, original.key, original.descriptor);
    } else Reflect.deleteProperty(globalThis, original.key);
  }
});

test("image generation accepts empty credentials and duplicate capabilities while posting the real model and group to the site", async () => {
  siteFetch.mockResolvedValue(json({ data: { b64_json: "aW1hZ2U=" } }));
  const result = await requestGeneration(config, "A tree");
  expect(result[0].dataUrl).toBe(reference.dataUrl);
  const [path, init] = siteFetch.mock.calls[0];
  expect(path).toBe("/pg/images/generations");
  expect(JSON.parse(String(init?.body))).toEqual({
    model: "gpt-image-2",
    group: "media group",
    prompt: "Be concise.\n\nA tree",
    n: 2,
    quality: "high",
    size: "1024x1024",
    output_format: "png",
  });
  expect(new Headers(init?.headers).has("Authorization")).toBe(false);
  expect(await fetchChannelModels(channels[0])).toEqual([
    "gpt-image-2",
    "sora-2",
    "tts-1",
  ]);
});

test("image edits preserve uploaded references and allow the browser to set the multipart boundary", async () => {
  siteFetch.mockImplementation(async () =>
    json({ data: [{ url: "https://cdn.example/image.png" }] }),
  );
  for (const references of [
    [reference],
    [reference, { ...reference, id: "second" }],
  ]) {
    await requestEdit(config, "Edit", references);
    const [path, init] = siteFetch.mock.calls.at(-1) || [];
    expect(path).toBe("/pg/images/edits");
    const form = init?.body as FormData;
    expect(form.get("model")).toBe("gpt-image-2");
    expect(form.get("group")).toBe("media group");
    expect(
      form.getAll(references.length === 1 ? "image" : "image[]"),
    ).toHaveLength(references.length);
    expect(new Headers(init?.headers).has("Content-Type")).toBe(false);
  }
});

test("image questions retain multimodal messages and accumulate UTF-8 SSE deltas split across chunks", async () => {
  const bytes = new TextEncoder().encode(
    'data: {"choices":[{"delta":{"content":"你"}}]}\r\n\r\ndata: {"choices":[{"delta":{"content":"好"}}]}\n\ndata: [DONE]',
  );
  siteFetch.mockResolvedValue(
    new Response(
      new ReadableStream({
        start(controller) {
          controller.enqueue(bytes.slice(0, 40));
          controller.enqueue(bytes.slice(40, 42));
          controller.enqueue(bytes.slice(42));
          controller.close();
        },
      }),
      { headers: { "Content-Type": "text/event-stream" } },
    ),
  );
  const messages = [
    {
      role: "user" as const,
      content: [
        { type: "image_url" as const, image_url: { url: reference.dataUrl } },
        { type: "text" as const, text: "Describe" },
      ],
    },
  ];
  const onDelta = vi.fn();
  expect(
    await requestImageQuestion(
      { ...config, reasoningEffort: "high" },
      messages,
      onDelta,
    ),
  ).toBe("你好");
  expect(onDelta.mock.calls.map(([text]) => text)).toEqual(["你", "你好"]);
  const [path, init] = siteFetch.mock.calls[0];
  expect(path).toBe("/pg/chat/completions");
  expect(JSON.parse(String(init?.body))).toMatchObject({
    group: "media group",
    model: "gpt-image-2",
    messages: [{ role: "system", content: "Be concise." }, ...messages],
    stream: true,
    reasoning_effort: "high",
  });
});

test("a saved site video resumes using its original group and downloads content through the session endpoint", async () => {
  siteFetch.mockResolvedValueOnce(json({ id: "video/id", status: "queued" }));
  const task = await createVideoGenerationTask(
    {
      ...config,
      model: "media group::sora-2",
      size: "1280x720",
      videoSeconds: "8",
    },
    "A tree",
    [reference],
  );
  const form = siteFetch.mock.calls[0][1]?.body as FormData;
  expect(form.get("input_reference")).toBeInstanceOf(File);
  expect(form.get("seconds")).toBe("8");
  expect(form.get("generate_audio")).toBeNull();
  expect(task).toEqual({
    id: "video/id",
    provider: "openai",
    model: "media group::sora-2",
    group: "media group",
  });
  siteFetch.mockResolvedValueOnce(json({ id: task.id, status: "in_progress" }));
  expect(await pollVideoGenerationTask(config, task)).toEqual({
    status: "pending",
  });
  siteFetch.mockResolvedValueOnce(
    json({
      id: task.id,
      status: "completed",
      video_url: "https://unused.example/video.mp4",
    }),
  );
  siteFetch.mockResolvedValueOnce(
    new Response("video", { headers: { "Content-Type": "video/mp4" } }),
  );
  const savedTask = JSON.stringify(task);
  const result = await waitForVideoGenerationTask(
    config,
    JSON.parse(savedTask),
  );
  expect(await result.blob?.text()).toBe("video");
  expect(siteFetch.mock.calls.slice(-2).map(([path]) => path)).toEqual([
    "/pg/videos/video%2Fid?group=media%20group",
    "/pg/videos/video%2Fid/content?group=media%20group",
  ]);
});

test("a terminal video error remains a failed task instead of a transient polling error", async () => {
  siteFetch.mockImplementation(async () =>
    json({
      id: "failed",
      status: "failed",
      error: { message: "Provider rejected the video" },
    }),
  );
  const task = {
    id: "failed",
    provider: "openai" as const,
    model: "media group::sora-2",
  };
  expect(await pollVideoGenerationTask(config, task)).toEqual({
    status: "failed",
    error: "Provider rejected the video",
  });
  const failure = await waitForVideoGenerationTask(config, task).catch(
    (error: unknown) => error,
  );
  expect(isVideoTaskFailed(failure)).toBe(true);
});

test("a video still pending after 120 queries waits for backend completion and only reports state changes", async () => {
  vi.useFakeTimers();
  let queries = 0;
  const onTaskStateChange = vi.fn();
  siteFetch.mockImplementation(async (path) => {
    if (path.includes("/content")) {
      return new Response("long-running video", {
        headers: { "Content-Type": "video/mp4" },
      });
    }
    queries += 1;
    return json({
      id: "slow",
      status: queries > 130 ? "completed" : "in_progress",
    });
  });
  const outcome = waitForVideoGenerationTask(
    config,
    { id: "slow", provider: "openai", model: "media group::sora-2" },
    { onTaskStateChange },
  ).then(
    (result) => ({ result }),
    (error: unknown) => ({ error }),
  );

  await vi.runAllTimersAsync();
  const completed = await outcome;
  expect(completed).not.toHaveProperty("error");
  expect("result" in completed && (await completed.result.blob?.text())).toBe(
    "long-running video",
  );
  expect(queries).toBe(131);
  expect(onTaskStateChange.mock.calls).toEqual([["pending"], ["completed"]]);
});

test("a transient network interruption retries the same task and resumes immediately when the network returns", async () => {
  vi.useFakeTimers();
  const interruption = new TypeError("Failed to fetch");
  const onQueryError = vi.fn();
  siteFetch.mockRejectedValueOnce(interruption);
  siteFetch.mockResolvedValueOnce(json({ id: "recover", status: "completed" }));
  siteFetch.mockResolvedValueOnce(
    new Response("recovered video", {
      headers: { "Content-Type": "video/mp4" },
    }),
  );
  const outcome = waitForVideoGenerationTask(
    config,
    { id: "recover", provider: "openai", model: "media group::sora-2" },
    { onQueryError },
  ).then(
    (result) => ({ result }),
    (error: unknown) => ({ error }),
  );
  await vi.advanceTimersByTimeAsync(0);
  expect(onQueryError).toHaveBeenCalledWith(interruption);
  window.dispatchEvent(new Event("online"));
  await vi.advanceTimersByTimeAsync(0);

  const completed = await outcome;
  expect(completed).not.toHaveProperty("error");
  expect("result" in completed && (await completed.result.blob?.text())).toBe(
    "recovered video",
  );
  expect(siteFetch.mock.calls.map(([path]) => path)).toEqual([
    "/pg/videos/recover?group=media%20group",
    "/pg/videos/recover?group=media%20group",
    "/pg/videos/recover/content?group=media%20group",
  ]);
});

test("a completed video persists completion before download and classifies download failure without losing its original group", async () => {
  let completionSaved = false;
  siteFetch.mockImplementation(async (path) => {
    if (path.includes("/content")) {
      expect(completionSaved).toBe(true);
      return json(
        { message: "Video download is temporarily unavailable" },
        503,
      );
    }
    return json({ id: "ready", status: "completed" });
  });
  const error = await waitForVideoGenerationTask(
    { ...config, model: "media group::sora-2" },
    {
      id: "ready",
      provider: "openai",
      model: "sora-2",
      group: "original group",
    },
    {
      onTaskStateChange: async (status) => {
        if (status === "completed") {
          await Promise.resolve();
          completionSaved = true;
        }
      },
    },
  ).catch((cause: unknown) => cause);

  expect(completionSaved).toBe(true);
  expect(isVideoTaskDownloadFailed(error)).toBe(true);
  expect(
    isVideoTaskDownloadFailed(new Error("Storage wrapper", { cause: error })),
  ).toBe(true);
  expect(isVideoTaskFailed(error)).toBe(false);
  expect(siteFetch.mock.calls.map(([path]) => path)).toEqual([
    "/pg/videos/ready?group=original%20group",
    "/pg/videos/ready/content?group=original%20group",
  ]);
});

test.each([429, 503])(
  "HTTP %i query interruptions resume the same pending video and report recovery before completion",
  async (status) => {
    vi.useFakeTimers();
    const onTaskStateChange = vi.fn();
    const onQueryError = vi.fn();
    siteFetch.mockResolvedValueOnce(
      json({ id: "retry", status: "in_progress" }),
    );
    siteFetch.mockResolvedValueOnce(
      json({ message: "Try again shortly" }, status),
    );
    siteFetch.mockResolvedValueOnce(
      json({ id: "retry", status: "in_progress" }),
    );
    siteFetch.mockResolvedValueOnce(json({ id: "retry", status: "completed" }));
    siteFetch.mockResolvedValueOnce(
      new Response("video", {
        headers: { "Content-Type": "video/mp4" },
      }),
    );
    const outcome = waitForVideoGenerationTask(
      config,
      { id: "retry", provider: "openai", model: "media group::sora-2" },
      { onTaskStateChange, onQueryError },
    );
    await vi.runAllTimersAsync();
    expect(await (await outcome).blob?.text()).toBe("video");
    expect(onQueryError).toHaveBeenCalledWith(
      expect.objectContaining({ status }),
    );
    expect(onTaskStateChange.mock.calls).toEqual([
      ["pending"],
      ["pending"],
      ["completed"],
    ]);
  },
);

test.each([401, 403, 404])(
  "HTTP %i stops automatic video queries without claiming backend generation failure",
  async (status) => {
    siteFetch.mockResolvedValueOnce(
      json({ message: "Query access interrupted" }, status),
    );
    const error = await waitForVideoGenerationTask(config, {
      id: "saved",
      provider: "openai",
      model: "media group::sora-2",
    }).catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(SiteMediaHttpError);
    expect(error).toMatchObject({
      status,
      message: "Query access interrupted",
    });
    expect(isVideoTaskFailed(error)).toBe(false);
    expect(isVideoTaskDownloadFailed(error)).toBe(false);
    expect(siteFetch).toHaveBeenCalledTimes(1);
  },
);

test("a hidden page polls less frequently and resumes querying when it becomes visible", async () => {
  vi.useFakeTimers();
  Object.assign(document, { visibilityState: "hidden" });
  siteFetch.mockResolvedValueOnce(
    json({ id: "hidden", status: "in_progress" }),
  );
  siteFetch.mockResolvedValueOnce(json({ id: "hidden", status: "completed" }));
  siteFetch.mockResolvedValueOnce(
    new Response("video", {
      headers: { "Content-Type": "video/mp4" },
    }),
  );
  const outcome = waitForVideoGenerationTask(config, {
    id: "hidden",
    provider: "openai",
    model: "media group::sora-2",
  });
  await vi.advanceTimersByTimeAsync(20_000);
  expect(siteFetch).toHaveBeenCalledTimes(1);
  Object.assign(document, { visibilityState: "visible" });
  document.dispatchEvent(new Event("visibilitychange"));
  await vi.advanceTimersByTimeAsync(0);
  expect(await (await outcome).blob?.text()).toBe("video");
  expect(vi.getTimerCount()).toBe(0);
});

test("aborting a waiting video removes timers and browser wake listeners before any extra query", async () => {
  vi.useFakeTimers();
  siteFetch.mockImplementation(async () =>
    json({ id: "cancel", status: "in_progress" }),
  );
  const controller = new AbortController();
  const windowAdds = vi.mocked(window.addEventListener).mock.calls.length;
  const documentAdds = vi.mocked(document.addEventListener).mock.calls.length;
  const outcome = waitForVideoGenerationTask(
    config,
    { id: "cancel", provider: "openai", model: "media group::sora-2" },
    { signal: controller.signal },
  ).catch((error: unknown) => error);
  await vi.advanceTimersByTimeAsync(0);
  controller.abort();
  expect(await outcome).toMatchObject({ name: "AbortError" });
  expect(vi.getTimerCount()).toBe(0);
  for (const args of vi
    .mocked(window.addEventListener)
    .mock.calls.slice(windowAdds)) {
    expect(window.removeEventListener).toHaveBeenCalledWith(args[0], args[1]);
  }
  for (const args of vi
    .mocked(document.addEventListener)
    .mock.calls.slice(documentAdds)) {
    expect(document.removeEventListener).toHaveBeenCalledWith(args[0], args[1]);
  }
  window.dispatchEvent(new Event("online"));
  document.dispatchEvent(new Event("visibilitychange"));
  await vi.runAllTimersAsync();
  expect(siteFetch).toHaveBeenCalledTimes(1);
});

test("multiple video reference images retain frame metadata in the gateway task extension", async () => {
  siteFetch.mockResolvedValueOnce(json({ id: "frames", status: "queued" }));
  await createVideoGenerationTask(
    { ...config, model: "media group::sora-2", videoMode: "frames" },
    "Animate",
    [reference, { ...reference, id: "last-frame" }],
  );
  const [path, init] = siteFetch.mock.calls[0];
  expect(path).toBe("/pg/videos");
  expect(JSON.parse(String(init?.body))).toMatchObject({
    model: "sora-2",
    group: "media group",
    input_reference: reference.dataUrl,
    images: [reference.dataUrl, reference.dataUrl],
    metadata: {
      first_frame: reference.dataUrl,
      last_frame: reference.dataUrl,
      generate_audio: true,
      watermark: false,
    },
  });
});

test("video requests preserve model duration, resolution and ratio limits while forwarding reference audio", async () => {
  const videoConfig = { ...videoContractConfig("doubao-seedance-2-0-260128"), videoSeconds: "45", vquality: "1080", size: "2:3" };
  videoConfig.channels[0].models.push({ name: "limited-audio-video", capability: "video", videoCapabilities: {
    durations: [15, 45], max_duration_seconds: 60, resolutions: ["1080p"], aspect_ratios: ["2:3"], max_reference_audio: 1,
  } });
  videoConfig.model = "media group::limited-audio-video";
  siteFetch.mockResolvedValueOnce(json({ id: "audio-reference", status: "queued" }));
  const audios = [{ id: "voice", name: "voice.wav", type: "audio/wav", url: "data:audio/wav;base64,dm9pY2U=" }];
  await createVideoGenerationTask(videoConfig, "Animate", [], { audios });
  expect(JSON.parse(String(siteFetch.mock.calls[0][1]?.body))).toMatchObject({
    seconds: "45", size: "1080x1620", metadata: {
      ratio: "2:3", resolution: "1080p", audios: [audios[0].url],
    },
  });
  siteFetch.mockClear();
  await expect(createVideoGenerationTask(videoConfig, "Animate", [], { audios: [...audios, { ...audios[0], id: "second" }] })).rejects.toThrow("up to 1 reference audio");
  expect(siteFetch).not.toHaveBeenCalled();
});

test.each([
  {
    model: "kling-v2-1",
    plugin: "kling",
    body: {
      image: reference.dataUrl,
      image_tail: "data:image/png;base64,dGFpbA==",
    },
  },
  {
    model: "doubao-seedance-2-0-260128",
    plugin: "doubao",
    body: {
      content: [
        {
          type: "image_url",
          image_url: { url: reference.dataUrl },
          role: "first_frame",
        },
        {
          type: "image_url",
          image_url: { url: "data:image/png;base64,dGFpbA==" },
          role: "last_frame",
        },
        { type: "text", text: "Animate" },
      ],
    },
  },
  {
    model: "MiniMax-Hailuo-2.3",
    plugin: "hailuo",
    body: {
      first_frame_image: reference.dataUrl,
      last_frame_image: "data:image/png;base64,dGFpbA==",
    },
  },
  {
    model: "MiniMax-H3",
    plugin: "hailuo",
    body: {
      resolution: "768P",
      content: [
        { type: "text", text: "Animate" },
        {
          type: "image_url",
          image_url: { url: reference.dataUrl },
          role: "first_frame",
        },
        {
          type: "image_url",
          image_url: { url: "data:image/png;base64,dGFpbA==" },
          role: "last_frame",
        },
      ],
    },
  },
  {
    model: "grok-imagine-video",
    plugin: "xai",
    body: {
      image: { url: reference.dataUrl },
      last_frame: { url: "data:image/png;base64,dGFpbA==" },
    },
  },
  {
    model: "viduq2",
    plugin: "vidu",
    body: { images: [reference.dataUrl, "data:image/png;base64,dGFpbA=="] },
  },
])(
  "$model frame requests survive the real plugin decoder and retain both frames upstream",
  async ({ model, plugin, body }) => {
    siteFetch.mockResolvedValueOnce(json({ id: "frames", status: "queued" }));
    await createVideoGenerationTask(videoContractConfig(model), "Animate", [
      reference,
      { ...reference, id: "tail", dataUrl: "data:image/png;base64,dGFpbA==" },
    ]);
    const init = siteFetch.mock.calls[0][1];
    expect(new Headers(init?.headers).get("Content-Type")).toBe(
      "application/json",
    );
    const payload = JSON.parse(String(init?.body));
    expect(payload.metadata?.mode).toBeUndefined();
    const provider = await import(
      new URL(
        `../../../../../../plugins/tasks/${plugin}/plugin.js`,
        import.meta.url,
      ).href
    );
    const identity = {
      model,
      upstreamModel: model,
      baseUrl: "https://fixture.example",
      apiKey: "sk-fixture",
      upstream: { kind: "new_api" },
    };
    const decoded = provider.protocols.openai_video.decodeRequest({
      ...identity,
      body: { kind: "json", value: payload },
    });
    const outbound = provider.buildSubmitRequest({
      ...identity,
      requestBody: decoded.requestBody,
      action: decoded.action,
    });
    expect(outbound.body).toMatchObject(body);
    if (plugin === "xai") expect(outbound.action).toBe("image_to_video");
    if (plugin === "vidu") expect(outbound.action).toBe("first_tail_to_video");
  },
);

test.each(["doubao-seedance-2-0-260128", "MiniMax-H3"])(
  "%s keeps every image, video and audio in reference content accepted by its plugin",
  async (model) => {
    const videoUrls = [
      "data:video/mp4;base64,dmlkZW8x",
      "data:video/mp4;base64,dmlkZW8y",
    ];
    const audioUrls = [
      "data:audio/wav;base64,YXVkaW8x",
      "data:audio/wav;base64,YXVkaW8y",
    ];
    siteFetch.mockResolvedValueOnce(
      json({ id: "references", status: "queued" }),
    );
    await createVideoGenerationTask(
      videoContractConfig(model),
      "Animate",
      [reference, { ...reference, id: "second" }],
      {
        videos: videoUrls.map((url, index) => ({
          id: `v${index}`,
          name: "ref.mp4",
          type: "video/mp4",
          url,
        })),
        audios: audioUrls.map((url, index) => ({
          id: `a${index}`,
          name: "ref.wav",
          type: "audio/wav",
          url,
        })),
      },
    );
    const payload = JSON.parse(String(siteFetch.mock.calls[0][1]?.body));
    expect(payload.images).toBeUndefined(); // content already contains the images; Doubao prepends images otherwise.
    expect(payload.metadata.content).toEqual([
      {
        type: "image_url",
        image_url: { url: reference.dataUrl },
        role: "reference_image",
      },
      {
        type: "image_url",
        image_url: { url: reference.dataUrl },
        role: "reference_image",
      },
      ...videoUrls.map((url) => ({
        type: "video_url",
        video_url: { url },
        role: "reference_video",
      })),
      ...audioUrls.map((url) => ({
        type: "audio_url",
        audio_url: { url },
        role: "reference_audio",
      })),
    ]);
    const plugin = model === "MiniMax-H3" ? "hailuo" : "doubao";
    const provider = await import(
      new URL(
        `../../../../../../plugins/tasks/${plugin}/plugin.js`,
        import.meta.url,
      ).href
    );
    const identity = {
      model,
      upstreamModel: model,
      baseUrl: "https://fixture.example",
    };
    const decoded = provider.protocols.openai_video.decodeRequest({
      ...identity,
      body: { kind: "json", value: payload },
    });
    const outbound = provider.buildSubmitRequest({
      ...identity,
      requestBody: decoded.requestBody,
      action: decoded.action,
    });
    expect(
      outbound.body.content.filter(
        (item: { type: string }) => item.type !== "text",
      ),
    ).toEqual(payload.metadata.content);
  },
);

test("Doubao single-image requests use JSON content instead of its rejected input_reference upload", async () => {
  siteFetch.mockResolvedValueOnce(json({ id: "single", status: "queued" }));
  await createVideoGenerationTask(
    videoContractConfig("doubao-seedance-2-0-260128"),
    "Animate",
    [reference],
  );
  const init = siteFetch.mock.calls[0][1];
  expect(new Headers(init?.headers).get("Content-Type")).toBe(
    "application/json",
  );
  expect(JSON.parse(String(init?.body)).metadata.content).toEqual([
    {
      type: "image_url",
      image_url: { url: reference.dataUrl },
      role: "first_frame",
    },
  ]);
});

test.each([
  {
    model: "viduq2",
    plugin: "vidu",
    body: { images: [reference.dataUrl, reference.dataUrl] },
  },
  {
    model: "grok-imagine-video",
    plugin: "xai",
    body: {
      reference_images: [
        { url: reference.dataUrl },
        { url: reference.dataUrl },
      ],
      reference_audios: [
        { url: "data:audio/wav;base64,YXVkaW8x" },
        { url: "data:audio/wav;base64,YXVkaW8y" },
      ],
    },
  },
])(
  "$model explicit reference mode stays reference mode with two images",
  async ({ model, plugin, body }) => {
    siteFetch.mockResolvedValueOnce(
      json({ id: "references", status: "queued" }),
    );
    await createVideoGenerationTask(
      videoContractConfig(model, { videoMode: "reference" }),
      "Animate",
      [reference, { ...reference, id: "second" }],
      {
        audios:
          plugin === "xai"
            ? [
                "data:audio/wav;base64,YXVkaW8x",
                "data:audio/wav;base64,YXVkaW8y",
              ].map((url, index) => ({
                id: `a${index}`,
                name: "ref.wav",
                type: "audio/wav",
                url,
              }))
            : [],
      },
    );
    const payload = JSON.parse(String(siteFetch.mock.calls[0][1]?.body));
    const provider = await import(
      new URL(
        `../../../../../../plugins/tasks/${plugin}/plugin.js`,
        import.meta.url,
      ).href
    );
    const identity = {
      model,
      upstreamModel: model,
      baseUrl: "https://fixture.example",
    };
    const decoded = provider.protocols.openai_video.decodeRequest({
      ...identity,
      body: { kind: "json", value: payload },
    });
    const outbound = provider.buildSubmitRequest({
      ...identity,
      requestBody: decoded.requestBody,
      action: decoded.action,
    });
    expect(outbound.action).toBe("reference_to_video");
    expect(outbound.body).toMatchObject(body);
  },
);

test.each(["google", "vertex-ai"])(
  "%s plugin receives one image and its native resolution/audio parameter names",
  async (plugin) => {
    const model = "veo-3.0-generate-001";
    siteFetch.mockResolvedValueOnce(json({ id: "veo", status: "queued" }));
    await createVideoGenerationTask(videoContractConfig(model), "Animate", [
      reference,
    ]);
    const payload = JSON.parse(String(siteFetch.mock.calls[0][1]?.body));
    const provider = await import(
      new URL(
        `../../../../../../plugins/tasks/${plugin}/plugin.js`,
        import.meta.url,
      ).href
    );
    const identity = {
      model,
      upstreamModel: model,
      baseUrl: "https://fixture.example",
      auth: { projectId: "fixture" },
    };
    const decoded = provider.protocols.openai_video.decodeRequest({
      ...identity,
      body: { kind: "json", value: payload },
    });
    const outbound = provider.buildSubmitRequest({
      ...identity,
      requestBody: decoded.requestBody,
      action: decoded.action,
    });
    expect(outbound.action).toBe("image_to_video");
    expect(outbound.body.parameters).toMatchObject({
      generateAudio: true,
      resolution: "720p",
      aspectRatio: "16:9",
      durationSeconds: 6,
    });
    expect(outbound.body.parameters).not.toHaveProperty("resolution_name");
    expect(outbound.body.parameters).not.toHaveProperty("generate_audio");
    expect(outbound.body.instances[0].image).toBeDefined();
  },
);

test("speech requests use standard OpenAI fields and reject JSON errors masquerading as audio", async () => {
  const speech = {
    ...config,
    model: "media group::tts-1",
    audioFormat: "wav",
    audioSpeed: "1.25",
    audioInstructions: "Speak slowly.",
  };
  siteFetch.mockResolvedValueOnce(
    new Response("audio", {
      headers: { "Content-Type": "application/octet-stream" },
    }),
  );
  expect((await requestAudioGeneration(speech, "Hello")).type).toBe(
    "audio/wav",
  );
  expect(siteFetch.mock.calls[0][0]).toBe("/pg/audio/speech");
  expect(JSON.parse(String(siteFetch.mock.calls[0][1]?.body))).toEqual({
    model: "tts-1",
    group: "media group",
    input: "Hello",
    voice: "alloy",
    response_format: "wav",
    speed: 1.25,
    instructions: "Speak slowly.",
  });
  siteFetch.mockResolvedValueOnce(
    json({ error: { message: "Insufficient quota" } }),
  );
  await expect(requestAudioGeneration(speech, "Hello")).rejects.toThrow(
    "Insufficient quota",
  );
});

test("stale model selections and pre-canceled requests never issue a site request, and HTTP errors preserve their reason", async () => {
  await expect(
    requestGeneration({ ...config, model: "removed::gpt-image-2" }, "Tree"),
  ).rejects.toThrow();
  await expect(requestAudioGeneration(config, "Tree")).rejects.toThrow();
  const controller = new AbortController();
  controller.abort();
  await expect(
    requestGeneration(config, "Tree", { signal: controller.signal }),
  ).rejects.toMatchObject({ name: "AbortError" });
  expect(siteFetch).not.toHaveBeenCalled();
  siteFetch.mockResolvedValueOnce(
    json({ success: false, message: "Group access denied" }, 403),
  );
  await expect(requestGeneration(config, "Tree")).rejects.toThrow(
    "Group access denied",
  );
  siteFetch.mockResolvedValueOnce(
    json({ error: { message: "Quota exhausted" } }),
  );
  await expect(
    requestSiteMediaJson("/pg/images/generations", { method: "POST" }),
  ).rejects.toThrow("Quota exhausted");
});
