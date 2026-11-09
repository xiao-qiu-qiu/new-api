import { afterAll, beforeEach, expect, test, vi } from "vitest";
import type { SiteContext } from "@/integration/site-context";
import type { AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";
import {
  isGeminiImageModel,
  resolveGeminiImageSettings,
} from "@/lib/gemini-image";

const pngBase64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1sAAAAASUVORK5CYII=";
const models = [
  "gemini-3-pro-image-preview",
  "gemini-3.1-flash-image-preview",
  "gemini-nano-banana-pro",
  "gpt-image-2",
];
const channels = [
  {
    id: "image group",
    name: "Images",
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

function imageConfig(overrides: Partial<AiConfig> = {}): AiConfig {
  return {
    ...defaultConfig,
    channels,
    model: `${channels[0].id}::${models[0]}`,
    apiKey: "",
    baseUrl: "",
    systemPrompt: "",
    quality: "high",
    size: "2880x2880",
    count: "1",
    background: "transparent",
    imageOutputFormat: "png",
    ...overrides,
  };
}

const reference: ReferenceImage = {
  id: "reference",
  name: "reference.png",
  type: "image/png",
  dataUrl: `data:image/png;base64,${pngBase64}`,
};

const settings = [
  {
    size: "2880x2880",
    quality: "high",
    expectedSize: "1:1",
    expectedQuality: "4K",
  },
  {
    size: "3840x2160",
    quality: "high",
    expectedSize: "16:9",
    expectedQuality: "4K",
  },
  { size: "4:5", quality: "4K", expectedSize: "4:5", expectedQuality: "4K" },
  {
    size: "auto",
    quality: "auto",
    expectedSize: undefined,
    expectedQuality: undefined,
  },
];

beforeEach(() => {
  siteFetch.mockReset();
  siteFetch.mockImplementation(async () =>
    Response.json({ data: [{ b64_json: pngBase64 }] }),
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

test.each(settings)(
  "Gemini generation with $size/$quality sends supported ratio and resolution without background",
  async ({ size, quality, expectedSize, expectedQuality }) => {
    const result = await requestGeneration(
      imageConfig({ size, quality }),
      "A cat",
    );

    expect(siteFetch).toHaveBeenCalledTimes(1);
    const [path, init] = siteFetch.mock.calls[0];
    expect(path).toBe("/pg/images/generations");
    expect(init?.method).toBe("POST");
    const payload = JSON.parse(String(init?.body));
    expect(payload).toEqual({
      model: models[0],
      group: channels[0].id,
      n: 1,
      prompt: "A cat",
      response_format: "b64_json",
      ...(expectedSize ? { size: expectedSize } : {}),
      ...(expectedQuality ? { quality: expectedQuality } : {}),
    });
    expect(result[0].dataUrl).toBe(`data:image/png;base64,${pngBase64}`);
    expect(directFetch).not.toHaveBeenCalled();
  },
);

test.each(settings)(
  "Gemini edit with $size/$quality sends supported multipart fields without background",
  async ({ size, quality, expectedSize, expectedQuality }) => {
    const result = await requestEdit(imageConfig({ size, quality }), "A cat", [
      reference,
    ]);

    expect(siteFetch).toHaveBeenCalledTimes(1);
    const [path, init] = siteFetch.mock.calls[0];
    expect(path).toBe("/pg/images/edits");
    expect(init?.method).toBe("POST");
    expect(init?.body).toBeInstanceOf(FormData);
    const form = init?.body as FormData;
    expect(
      Object.fromEntries(
        [...form.entries()].filter(([key]) => key !== "image"),
      ),
    ).toEqual({
      model: models[0],
      group: channels[0].id,
      n: "1",
      prompt: expect.stringContaining("A cat"),
      response_format: "b64_json",
      ...(expectedSize ? { size: expectedSize } : {}),
      ...(expectedQuality ? { quality: expectedQuality } : {}),
    });
    const file = form.get("image") as File;
    expect(file.name).toBe("reference.png");
    expect(file.type).toBe("image/png");
    expect(file.size).toBeGreaterThan(0);
    expect(result[0].dataUrl).toBe(`data:image/png;base64,${pngBase64}`);
    expect(directFetch).not.toHaveBeenCalled();
  },
);

test.each(["gemini-3.1-flash-image-preview", "gemini-nano-banana-pro"])(
  "%s generation resolves the raw model name before applying Gemini settings",
  async (model) => {
    await requestGeneration(
      imageConfig({ model: `${channels[0].id}::${model}` }),
      "A cat",
    );
    expect(JSON.parse(String(siteFetch.mock.calls[0][1]?.body))).toMatchObject({
      model,
      size: "1:1",
      quality: "4K",
    });
  },
);

test.each(["generation", "edit"] as const)(
  "GPT %s retains pixel dimensions, high quality mapping and transparent background",
  async (mode) => {
    const config = imageConfig({
      model: `${channels[0].id}::gpt-image-2`,
      size: "16:9",
      quality: "xhigh",
    });
    if (mode === "generation") await requestGeneration(config, "A cat");
    else await requestEdit(config, "A cat", [reference]);

    const body = siteFetch.mock.calls[0][1]?.body;
    const parameters =
      body instanceof FormData
        ? Object.fromEntries(body.entries())
        : JSON.parse(String(body));
    expect(parameters).toMatchObject({
      model: "gpt-image-2",
      size: "1536x864",
      quality: "high",
      background: "transparent",
      output_format: "png",
    });
    expect(parameters).not.toHaveProperty("response_format");
  },
);

test.each(["generation", "edit"] as const)(
  "Gemini %s keeps the image count capped at 15",
  async (mode) => {
    const config = imageConfig({ count: "99" });
    if (mode === "generation") await requestGeneration(config, "A cat");
    else await requestEdit(config, "A cat", [reference]);

    const body = siteFetch.mock.calls[0][1]?.body;
    if (body instanceof FormData) expect(body.get("n")).toBe("15");
    else expect(JSON.parse(String(body)).n).toBe(15);
    expect(siteFetch).toHaveBeenCalledTimes(1);
  },
);

test.each(["generation", "edit"] as const)(
  "Gemini %s surfaces upstream failures without retrying",
  async (mode) => {
    siteFetch.mockImplementation(async () =>
      Response.json(
        { error: { message: "Unsupported image parameters" } },
        { status: 400 },
      ),
    );
    const request =
      mode === "generation"
        ? requestGeneration(imageConfig(), "A cat")
        : requestEdit(imageConfig(), "A cat", [reference]);

    await expect(request).rejects.toThrow("Unsupported image parameters");
    expect(siteFetch).toHaveBeenCalledTimes(1);
    expect(directFetch).not.toHaveBeenCalled();
  },
);

test.each([
  ["gemini-3-pro-image-preview", true],
  ["gemini-3.1-flash-image-preview", true],
  ["gemini-3-image", true],
  ["gemini-nano-banana-pro", true],
  ["GEMINI-NANO-BANANA-2", true],
  ["gemini-3-pro", false],
  ["gemini-3.1-flash-preview", false],
  ["gemini-3-pro-imagery", false],
  ["gpt-image-2", false],
  ["image group::gemini-3-pro-image-preview", false],
])("raw model %s has Gemini image classification %s", (model, expected) => {
  expect(isGeminiImageModel(model)).toBe(expected);
});

test.each([
  ["2880x2880", "high", "4k", "1:1"],
  ["3840x2160", "max", "4k", "16:9"],
  ["1024x1536", "low", "1k", "2:3"],
  ["1360x2048", "medium", "2k", "2:3"],
  ["2480x3312", "xhigh", "4k", "3:4"],
  ["2880x2880", "1K", "1k", "1:1"],
  ["16:9", "4K", "4k", "16:9"],
  ["5:4", "2K", "2k", "5:4"],
  ["32:18", "auto", "auto", "16:9"],
  ["9:21", "auto", "auto", "9:16"],
  ["1648x3840", "high", "4k", "9:16"],
  ["1920x1080", "auto", "2k", "16:9"],
  ["1K", "high", "1k", "auto"],
  ["2K", "auto", "2k", "auto"],
  ["4K", "auto", "4k", "auto"],
  ["4K", "2K", "2k", "auto"],
  ["auto", "4K", "4k", "auto"],
  ["auto", "auto", "auto", "auto"],
  ["auto", "high", "auto", "auto"],
  ["1:1", "max", "auto", "1:1"],
  [" 3840X2160 ", " 2k ", "2k", "16:9"],
  ["invalid", "auto", "auto", "auto"],
  ["0:1", "4K", "4k", "auto"],
])(
  "Gemini settings %s/%s resolve to %s and %s",
  (size, quality, scale, ratio) => {
    expect(resolveGeminiImageSettings(size, quality)).toEqual({ scale, ratio });
  },
);
