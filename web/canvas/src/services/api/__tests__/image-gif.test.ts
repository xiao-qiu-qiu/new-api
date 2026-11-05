import { afterAll, beforeEach, expect, test, vi } from "vitest";
import type { SiteContext } from "@/integration/site-context";
import type { AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";

const gifBase64 = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";
const pngBase64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1sAAAAASUVORK5CYII=";
const models = [
  "gpt-image-2.6-flare",
  "gpt-image-2.6-sunburst",
  "gpt-image-2",
  "dall-e-3",
  "gpt-image-2.6-flare-extra",
  "gpt-image-2.6-sunburst-extra",
];
const channels = [
  {
    id: "animation group",
    name: "Animation",
    baseUrl: "https://unused.example",
    apiKey: "",
    apiFormat: "openai" as const,
    models: models.map((name) => ({ name, capability: "image" as const })),
  },
];
const context: SiteContext = {
  userId: 7,
  group: channels[0].id,
  model: models[0],
  agentGroup: channels[0].id,
  agentModel: models[0],
  theme: "light",
  language: "en-US",
  channels,
};
const siteFetch =
  vi.fn<(path: string, init?: RequestInit) => Promise<Response>>();
const directFetch = vi.fn(() => {
  throw new Error("Unexpected direct external fetch");
});
const values = new Map<string, string>();
const storage = {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
};
const windowEvents = new EventTarget();
const globals: Record<string, unknown> = {
  window: {
    __newApiCanvasHost: {
      context,
      fetch: siteFetch,
      selectAgentModel: vi.fn(),
    },
    localStorage: storage,
    addEventListener: windowEvents.addEventListener.bind(windowEvents),
    removeEventListener: windowEvents.removeEventListener.bind(windowEvents),
  },
  localStorage: storage,
  fetch: directFetch,
};
const originals = Object.keys(globals).map((key) => ({
  key,
  descriptor: Object.getOwnPropertyDescriptor(globalThis, key),
}));
for (const [key, value] of Object.entries(globals)) {
  Object.defineProperty(globalThis, key, { configurable: true, value });
}

const { defaultConfig } = await import("@/stores/use-config-store");
const { requestGeneration, requestEdit } = await import("../image");

function gifConfig(overrides: Partial<AiConfig> = {}): AiConfig {
  return {
    ...defaultConfig,
    channels,
    model: `${channels[0].id}::gpt-image-2.6-flare`,
    apiKey: "",
    baseUrl: "",
    systemPrompt: "Be concise.",
    quality: "high",
    size: "1024x1024",
    count: "2",
    background: "transparent",
    imageOutputFormat: "gif",
    imageFrameCount: "9",
    imageFrameDurationMs: "200",
    ...overrides,
  };
}

const reference: ReferenceImage = {
  id: "reference",
  name: "reference.png",
  type: "image/png",
  dataUrl: `data:image/png;base64,${pngBase64}`,
};

beforeEach(() => {
  siteFetch.mockReset();
  siteFetch.mockImplementation(async () =>
    Response.json({ data: [{ b64_json: gifBase64 }] }),
  );
  directFetch.mockClear();
  values.clear();
});

afterAll(() => {
  for (const original of originals) {
    if (original.descriptor) {
      Object.defineProperty(globalThis, original.key, original.descriptor);
    } else Reflect.deleteProperty(globalThis, original.key);
  }
});

test.each(["gpt-image-2.6-flare", "gpt-image-2.6-sunburst"])(
  "%s GIF generation sends animation parameters without response_format, streaming or background",
  async (model) => {
    const result = await requestGeneration(
      gifConfig({ model: `${channels[0].id}::${model}` }),
      "A blinking cat",
    );

    expect(siteFetch).toHaveBeenCalledTimes(1);
    const [path, init] = siteFetch.mock.calls[0];
    expect(path).toBe("/pg/images/generations");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      model,
      group: "animation group",
      prompt: "Be concise.\n\nA blinking cat",
      n: 2,
      quality: "high",
      size: "1024x1024",
      output_format: "gif",
      frame_count: 9,
      frame_duration_ms: 200,
    });
    expect(new Headers(init?.headers).has("Authorization")).toBe(false);
    expect(result[0].dataUrl).toBe(`data:image/gif;base64,${gifBase64}`);
    expect(directFetch).not.toHaveBeenCalled();
  },
);

test("GIF generation without saved frame settings uses 9 frames at 200 ms", async () => {
  await requestGeneration(
    gifConfig({ imageFrameCount: undefined, imageFrameDurationMs: undefined }),
    "A blinking cat",
  );
  expect(JSON.parse(String(siteFetch.mock.calls[0][1]?.body))).toMatchObject({
    frame_count: 9,
    frame_duration_ms: 200,
  });
});

test.each([
  ["2", "20", 2, 20],
  ["64", "2000", 64, 2000],
  ["12", "350", 12, 350],
])(
  "GIF generation forwards valid frame settings %s/%s as numbers",
  async (count, duration, expectedCount, expectedDuration) => {
    await requestGeneration(
      gifConfig({ imageFrameCount: count, imageFrameDurationMs: duration }),
      "A blinking cat",
    );
    expect(JSON.parse(String(siteFetch.mock.calls[0][1]?.body))).toMatchObject({
      frame_count: expectedCount,
      frame_duration_ms: expectedDuration,
    });
  },
);

test("base64 GIF returned for a PNG request retains its actual GIF MIME", async () => {
  const result = await requestGeneration(
    gifConfig({ imageOutputFormat: "png" }),
    "A blinking cat",
  );
  expect(result[0].dataUrl).toBe(`data:image/gif;base64,${gifBase64}`);
});

test.each([
  "gpt-image-2",
  "dall-e-3",
  "gpt-image-2.6-flare-extra",
  "gpt-image-2.6-sunburst-extra",
])(
  "GIF generation with unsupported model %s rejects before fetching",
  async (model) => {
    await expect(
      requestGeneration(
        gifConfig({ model: `${channels[0].id}::${model}` }),
        "A blinking cat",
      ),
    ).rejects.toThrow("GIF output requires GPT Image 2.6 Flare or Sunburst.");
    expect(siteFetch).not.toHaveBeenCalled();
    expect(directFetch).not.toHaveBeenCalled();
  },
);

test.each([
  { name: "a reference image", references: [reference] },
  {
    name: "a reference image and mask",
    references: [
      reference,
      { ...reference, id: "mask", role: "mask" as const },
    ],
  },
  {
    name: "only a mask",
    references: [{ ...reference, id: "mask", role: "mask" as const }],
  },
  { name: "no references", references: [] },
])("GIF edit with $name rejects before fetching", async ({ references }) => {
  await expect(requestEdit(gifConfig(), "Animate", references)).rejects.toThrow(
    "GIF output is only available for text-to-image generation. Remove reference images and masks.",
  );
  expect(siteFetch).not.toHaveBeenCalled();
  expect(directFetch).not.toHaveBeenCalled();
});

test.each(["1", "65", "2.5", "abc", "NaN", "Infinity"])(
  "GIF frame count %s rejects before fetching",
  async (imageFrameCount) => {
    await expect(
      requestGeneration(gifConfig({ imageFrameCount }), "A blinking cat"),
    ).rejects.toThrow("GIF frame count must be an integer from 2 to 64.");
    expect(siteFetch).not.toHaveBeenCalled();
  },
);

test.each(["19", "2001", "20.5", "abc", "NaN", "Infinity"])(
  "GIF frame duration %s rejects before fetching",
  async (imageFrameDurationMs) => {
    await expect(
      requestGeneration(gifConfig({ imageFrameDurationMs }), "A blinking cat"),
    ).rejects.toThrow(
      "GIF frame duration must be an integer from 20 to 2000 ms.",
    );
    expect(siteFetch).not.toHaveBeenCalled();
  },
);

test.each([undefined, "png"])(
  "PNG generation with output format %s preserves existing fields and ignores animation settings",
  async (imageOutputFormat) => {
    siteFetch.mockImplementation(async () =>
      Response.json({ data: [{ b64_json: pngBase64 }] }),
    );
    const result = await requestGeneration(
      gifConfig({
        model: `${channels[0].id}::gpt-image-2`,
        imageOutputFormat,
        imageFrameCount: "invalid",
        imageFrameDurationMs: "invalid",
      }),
      "A tree",
    );

    expect(JSON.parse(String(siteFetch.mock.calls[0][1]?.body))).toEqual({
      model: "gpt-image-2",
      group: "animation group",
      prompt: "Be concise.\n\nA tree",
      n: 2,
      quality: "high",
      size: "1024x1024",
      background: "transparent",
      output_format: "png",
    });
    expect(result[0].dataUrl).toBe(`data:image/png;base64,${pngBase64}`);
  },
);

test("PNG generation with DALL-E keeps response_format and existing quality mapping", async () => {
  siteFetch.mockImplementation(async () =>
    Response.json({ data: [{ b64_json: pngBase64 }] }),
  );
  await requestGeneration(
    gifConfig({
      model: `${channels[0].id}::dall-e-3`,
      imageOutputFormat: "png",
    }),
    "A tree",
  );
  expect(JSON.parse(String(siteFetch.mock.calls[0][1]?.body))).toEqual({
    model: "dall-e-3",
    group: "animation group",
    prompt: "Be concise.\n\nA tree",
    n: 2,
    quality: "hd",
    size: "1024x1024",
    background: "transparent",
    response_format: "b64_json",
  });
});
