import { nanoid } from "nanoid";

import i18n from "@/i18n";
import { getSiteContext } from "@/integration/site-context";
import { dataUrlToFile } from "@/lib/image-utils";
import { buildImageReferencePromptText } from "@/lib/image-reference-prompt";
import { imageToDataUrl } from "@/services/image-storage";
import { imageSizePresets } from "@/lib/media-size";
import { encodeWanxImageEditMask } from "@/lib/canvas/canvas-image-mask";
import { requestSiteResponsesText } from "./site-agent";
import type { AiConfig, ModelChannel } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";
import {
  assertMediaPayload,
  requestSiteMedia,
  requestSiteMediaJson,
  resolveSiteMediaModel,
  siteMediaModels,
  type MediaRequestOptions,
} from "./site-media";

const apiText = (key: string, options?: Record<string, unknown>) =>
  i18n.t(`apiErrors.${key}`, options);

export type AiTextMessage = {
  role: "system" | "user" | "assistant";
  content:
    | string
    | Array<
        | { type: "text"; text: string }
        | { type: "image_url"; image_url: { url: string } }
      >;
};

type ImageApiResponse = {
  data?: Record<string, unknown> | Array<Record<string, unknown>>;
  images?: Array<Record<string, unknown>>;
  results?: Array<Record<string, unknown>>;
};
type ChatPayload = {
  choices?: Array<{
    delta?: { content?: string | null };
    message?: { content?: string | null };
  }>;
};

const IMAGE_QUALITY_VALUES = new Set(["low", "medium", "high", "xhigh", "max"]);
const DEFAULT_IMAGE_SHORT_SIDE = 1024;
const IMAGE_SIZE_STEP = 16;
const IMAGE_MIN_PIXELS = 655360;
const IMAGE_MAX_PIXELS = 8294400;
const IMAGE_MAX_EDGE = 3840;
const IMAGE_MAX_RATIO = 3;
const IMAGE_OUTPUT_FORMAT = "png";

function normalizeQuality(quality: string) {
  const value = quality.trim().toLowerCase();
  return IMAGE_QUALITY_VALUES.has(value) ? value : undefined;
}

/** Only "transparent" is forwarded; any other value (incl. empty) means keep the default opaque background. */
function normalizeBackground(background: string | undefined) {
  return background?.trim().toLowerCase() === "transparent"
    ? "transparent"
    : undefined;
}

/** Map a bare ratio such as "1:1" to an explicit pixel dimension like "1024x1024". */
function resolveSize(ratio: string): string {
  const parsedRatio = parseImageRatio(ratio);
  const preset = imageSizePresets["1k"][ratio];
  if (preset) return preset;
  const isLandscape = parsedRatio.width >= parsedRatio.height;
  const longRatio = isLandscape
    ? parsedRatio.width / parsedRatio.height
    : parsedRatio.height / parsedRatio.width;
  const shortSide = DEFAULT_IMAGE_SHORT_SIDE;
  const longSide =
    Math.round((shortSide * longRatio) / IMAGE_SIZE_STEP) * IMAGE_SIZE_STEP;
  const width = isLandscape ? longSide : shortSide;
  const height = isLandscape ? shortSide : longSide;
  validateImageSize(width, height);
  return `${width}x${height}`;
}

function parseRatioValue(value: string) {
  const parts = value.split(":");
  if (parts.length !== 2) throw new Error(apiText("invalidImageSizeFormat"));
  const w = Number(parts[0]);
  const h = Number(parts[1]);
  if (!Number.isFinite(w) || !Number.isFinite(h) || w <= 0 || h <= 0) {
    throw new Error(apiText("positiveImageRatio"));
  }
  return { width: w, height: h };
}

function parseImageRatio(value: string) {
  const ratio = parseRatioValue(value);
  if (
    Math.max(ratio.width, ratio.height) / Math.min(ratio.width, ratio.height) >
    IMAGE_MAX_RATIO
  ) {
    throw new Error(apiText("imageRatioLimit"));
  }
  return ratio;
}

function parseImageDimensions(value: string) {
  const match = value.match(/^(\d+)x(\d+)$/i);
  if (!match) return null;
  return { width: Number(match[1]), height: Number(match[2]) };
}

function validateImageSize(width: number, height: number) {
  if (
    !Number.isInteger(width) ||
    !Number.isInteger(height) ||
    width <= 0 ||
    height <= 0
  ) {
    throw new Error(apiText("positiveImageDimensions"));
  }
  if (width % IMAGE_SIZE_STEP !== 0 || height % IMAGE_SIZE_STEP !== 0) {
    throw new Error(apiText("imageDimensionStep"));
  }
  if (Math.max(width, height) > IMAGE_MAX_EDGE) {
    throw new Error(apiText("imageEdgeLimit"));
  }
  if (Math.max(width, height) / Math.min(width, height) > IMAGE_MAX_RATIO) {
    throw new Error(apiText("imageRatioLimit"));
  }
  const pixels = width * height;
  if (
    (pixels < IMAGE_MIN_PIXELS &&
      !(width === height && (width === 256 || width === 512))) ||
    pixels > IMAGE_MAX_PIXELS
  ) {
    throw new Error(apiText("imagePixelLimit"));
  }
}

function resolveRequestSize(size: string) {
  const value = size.trim();
  if (!value || value.toLowerCase() === "auto") return undefined;
  const dimensions = parseImageDimensions(value);
  if (dimensions) {
    validateImageSize(dimensions.width, dimensions.height);
    return `${dimensions.width}x${dimensions.height}`;
  }
  if (value.includes(":")) return resolveSize(value);
  throw new Error(apiText("invalidImageSizeFormat"));
}

function parseImagePayload(
  payload: ImageApiResponse,
): Array<{ id: string; dataUrl: string }> {
  const data = payload.data || payload.images || payload.results || [];
  const images = (Array.isArray(data) ? data : [data]).flatMap((item) => {
    if (!item || typeof item !== "object") return [];
    if (typeof item.b64_json === "string" && item.b64_json) {
      return [
        { id: nanoid(), dataUrl: `data:image/png;base64,${item.b64_json}` },
      ];
    }
    if (typeof item.url === "string" && item.url) {
      return [{ id: nanoid(), dataUrl: item.url }];
    }
    return [];
  });
  if (!images.length) throw new Error(apiText("noImageReturned"));
  return images;
}

function withSystemPrompt(config: AiConfig, prompt: string): string {
  const systemPrompt = config.systemPrompt.trim();
  return systemPrompt ? `${systemPrompt}\n\n${prompt}` : prompt;
}

function imageRequestParameters(
  config: AiConfig,
): Record<string, string | number> {
  const selection = resolveSiteMediaModel(
    config,
    config.model || config.imageModel,
    "image",
  );
  const size = selection.model.toLowerCase() === "wanx2.1-imageedit"
    ? undefined
    : resolveRequestSize(config.size);
  const background = normalizeBackground(config.background);
  let quality = normalizeQuality(config.quality);
  if (quality === "xhigh" || quality === "max") quality = "high";
  if (selection.model === "dall-e-3" && quality) {
    quality = quality === "high" ? "hd" : "standard";
  }
  if (selection.model === "dall-e-2") quality = undefined;
  return {
    ...selection,
    n: Math.max(
      1,
      Math.min(15, Math.floor(Math.abs(Number(config.count)) || 1)),
    ),
    ...(quality ? { quality } : {}),
    ...(size ? { size } : {}),
    ...(background ? { background } : {}),
    ...(/gpt-image/i.test(selection.model)
      ? { output_format: IMAGE_OUTPUT_FORMAT }
      : { response_format: "b64_json" }),
  };
}

export async function requestGeneration(
  config: AiConfig,
  prompt: string,
  options?: MediaRequestOptions,
): Promise<Array<{ id: string; dataUrl: string }>> {
  const payload = await requestSiteMediaJson<ImageApiResponse>(
    "/pg/images/generations",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...imageRequestParameters(config),
        prompt: withSystemPrompt(config, prompt),
      }),
    },
    options,
  );
  return parseImagePayload(payload);
}

export async function requestEdit(
  config: AiConfig,
  prompt: string,
  references: ReferenceImage[],
  options?: MediaRequestOptions,
): Promise<Array<{ id: string; dataUrl: string }>> {
  const images = references.filter((image) => image.role !== "mask");
  const masks = references.filter((image) => image.role === "mask");
  if (!images.length || masks.length > 1) {
    throw new Error(apiText("referenceImageReadFailed"));
  }
  const parameters = imageRequestParameters(config);
  const form = new FormData();
  for (const [key, value] of Object.entries(parameters)) {
    form.set(key, String(value));
  }
  form.set(
    "prompt",
    withSystemPrompt(config, buildImageReferencePromptText(prompt, images)),
  );
  const files = await Promise.all(
    images.map(async (image) =>
      dataUrlToFile({
        ...image,
        dataUrl: await imageToDataUrl(image, options),
      }),
    ),
  );
  const field = files.length > 1 ? "image[]" : "image";
  files.forEach((file) => form.append(field, file));
  if (masks[0]) {
    let dataUrl = await imageToDataUrl(masks[0], options);
    if (String(parameters.model).toLowerCase() === "wanx2.1-imageedit") {
      dataUrl = await encodeWanxImageEditMask(dataUrl, options?.signal);
    }
    form.set("mask", dataUrlToFile({ ...masks[0], name: "mask.png", type: "image/png", dataUrl }));
  }
  const payload = await requestSiteMediaJson<ImageApiResponse>(
    "/pg/images/edits",
    { method: "POST", body: form },
    options,
  );
  return parseImagePayload(payload);
}

export async function requestImageQuestion(
  config: AiConfig,
  messages: AiTextMessage[],
  onDelta: (text: string) => void,
  options?: MediaRequestOptions,
): Promise<string> {
  const selection = resolveSiteMediaModel(
    config,
    config.model || config.textModel,
    "text",
  );
  const systemPrompt = config.systemPrompt.trim();
  const model = getSiteContext().channels
    .find((channel) => channel.id === selection.group)?.models
    .find((item) => item.name === selection.model);
  if (model?.chatProtocol === "responses") {
    return requestSiteResponsesText({
      ...selection,
      messages: systemPrompt
        ? [{ role: "system", content: systemPrompt }, ...messages]
        : messages,
      signal: options?.signal,
      onDelta,
      reasoningEffort: config.reasoningEffort,
    });
  }
  return requestSiteMedia(
    "/pg/chat/completions",
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Accept: "text/event-stream",
      },
      body: JSON.stringify({
        ...selection,
        messages: systemPrompt
          ? [{ role: "system", content: systemPrompt }, ...messages]
          : messages,
        stream: true,
        ...(config.reasoningEffort === "auto"
          ? {}
          : { reasoning_effort: config.reasoningEffort }),
      }),
    },
    async (response) => {
      if (
        !response.body ||
        !response.headers.get("content-type")?.includes("text/event-stream")
      ) {
        const payload: ChatPayload = await response.json();
        assertMediaPayload(payload);
        const answer =
          payload.choices?.[0]?.message?.content || apiText("noContent");
        onDelta(answer);
        return answer;
      }
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      const state = { buffer: "", text: "", done: false };
      try {
        while (!state.done) {
          const chunk = await reader.read();
          consumeChatStream(
            state,
            decoder.decode(chunk.value, { stream: !chunk.done }),
            onDelta,
            chunk.done,
          );
          if (chunk.done) break;
        }
      } finally {
        await reader.cancel().catch(() => undefined);
        reader.releaseLock();
      }
      if (!state.text) onDelta(apiText("noContent"));
      return state.text || apiText("noContent");
    },
    options,
  );
}

function consumeChatStream(
  state: { buffer: string; text: string; done: boolean },
  chunk: string,
  onDelta: (text: string) => void,
  flush: boolean,
): void {
  state.buffer += chunk;
  const blocks = state.buffer.split(/\r?\n\r?\n/);
  state.buffer = blocks.pop() || "";
  if (flush && state.buffer.trim()) {
    blocks.push(state.buffer);
    state.buffer = "";
  }
  for (const block of blocks) {
    const data = block
      .split(/\r?\n/)
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice(5).trimStart())
      .join("\n")
      .trim();
    if (!data) continue;
    if (data === "[DONE]") {
      state.done = true;
      break;
    }
    const payload: ChatPayload = JSON.parse(data);
    assertMediaPayload(payload);
    const delta = payload.choices?.[0]?.delta?.content;
    if (delta) {
      state.text += delta;
      onDelta(state.text);
    }
  }
}

export async function fetchImageModels(
  _config: Pick<AiConfig, "baseUrl" | "apiKey" | "apiFormat">,
): Promise<string[]> {
  return siteMediaModels();
}

export async function fetchChannelModels(
  channel: ModelChannel,
): Promise<string[]> {
  return siteMediaModels(channel.id);
}
