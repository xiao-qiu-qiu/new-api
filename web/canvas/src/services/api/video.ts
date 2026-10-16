import i18n from "@/i18n";
import { dataUrlToFile, readFileAsDataUrl } from "@/lib/image-utils";
import {
  clampVideoSeconds,
  computeVideoSize,
  inferVideoRatio,
} from "@/lib/media-size";
import {
  getMediaBlob,
  resolveMediaUrl,
  uploadMediaFile,
  type UploadedFile,
} from "@/services/file-storage";
import { imageToDataUrl } from "@/services/image-storage";
import { boolConfig, type AiConfig } from "@/stores/use-config-store";
import type { ReferenceImage } from "@/types/image";
import type { ReferenceAudio, ReferenceVideo } from "@/types/media";
import {
  assertMediaPayload,
  mediaErrorMessage,
  requestSiteMedia,
  requestSiteMediaBlob,
  requestSiteMediaJson,
  resolveSiteMediaModel,
  type MediaRequestOptions,
} from "./site-media";

type VideoResponse = {
  id: string;
  status?: string;
  error?: { message?: string };
  url?: string;
  result_url?: string;
  video_url?: string;
  content?: { video_url?: string; url?: string } | null;
};
type ApiVideoResponse =
  | VideoResponse
  | {
      code?: number | string;
      data?: VideoResponse | null;
      msg?: string;
      message?: string;
      error?: { message?: string };
    };
type VideoMediaOptions = MediaRequestOptions & {
  videos?: ReferenceVideo[];
  audios?: ReferenceAudio[];
};
const apiText = (key: string, options?: Record<string, unknown>) =>
  i18n.t(`apiErrors.${key}`, options);

export type VideoGenerationResult = {
  blob?: Blob;
  url?: string;
  mimeType?: string;
};
export type VideoGenerationTask = {
  id: string;
  provider: "openai" | "gemini" | "plugin";
  model: string;
};
export type VideoGenerationTaskState =
  | { status: "pending" }
  | { status: "completed"; result: VideoGenerationResult }
  | { status: "failed"; error: string };

export async function requestVideoGeneration(
  config: AiConfig,
  prompt: string,
  references: ReferenceImage[] = [],
  options?: VideoMediaOptions,
): Promise<VideoGenerationResult> {
  return waitForVideoGenerationTask(
    config,
    await createVideoGenerationTask(config, prompt, references, options),
    options,
  );
}

export async function waitForVideoGenerationTask(
  config: AiConfig,
  task: VideoGenerationTask,
  options?: MediaRequestOptions,
): Promise<VideoGenerationResult> {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    options?.signal?.throwIfAborted();
    const state = await pollVideoGenerationTask(config, task, options);
    if (state.status === "completed") return state.result;
    if (state.status === "failed") {
      const error = new Error(state.error);
      error.name = "VideoTaskFailed";
      throw error;
    }
    if (attempt === 119) break;
    await delay(2500, options?.signal);
  }
  throw new Error(apiText("videoTimeout", { provider: "" }));
}

export function isVideoTaskFailed(error: unknown): boolean {
  return error instanceof Error && error.name === "VideoTaskFailed";
}

export async function createVideoGenerationTask(
  config: AiConfig,
  prompt: string,
  references: ReferenceImage[] = [],
  options?: VideoMediaOptions,
): Promise<VideoGenerationTask> {
  const selection = resolveSiteMediaModel(
    config,
    config.model || config.videoModel,
    "video",
  );
  const images = await Promise.all(
    references.map((image) => imageToDataUrl(image, options)),
  );
  const videos = await Promise.all(
    (options?.videos || []).map((video) =>
      referenceMediaToDataUrl(video, "invalidReferenceVideo", options),
    ),
  );
  const audios = await Promise.all(
    (options?.audios || []).map((audio) =>
      referenceMediaToDataUrl(audio, "invalidReferenceAudio", options),
    ),
  );
  const parameters = {
    ...selection,
    prompt,
    seconds: clampVideoSeconds(config.videoSeconds),
    size: normalizeVideoSize(config.size, config.vquality) || "1280x720",
  };
  let init: RequestInit;
  // Keep OpenAI's single uploaded input_reference; plugin protocols can also
  // accept JSON, including providers such as Doubao that reject file uploads.
  if (
    !/^sora(?:-|$)/i.test(selection.model) ||
    images.length > 1 ||
    videos.length ||
    audios.length
  ) {
    init = {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(
        videoReferenceRequest(config, parameters, images, videos, audios),
      ),
    };
  } else {
    const form = new FormData();
    for (const [key, value] of Object.entries(parameters)) form.set(key, value);
    if (images[0]) {
      form.append(
        "input_reference",
        dataUrlToFile({ ...references[0], dataUrl: images[0] }),
      );
    }
    init = { method: "POST", body: form };
  }
  const created = unwrapVideoResponse(
    await requestSiteMediaJson<ApiVideoResponse>("/pg/videos", init, options),
  );
  if (!created.id) throw new Error(apiText("noVideoTaskId"));
  return {
    id: created.id,
    provider: "openai",
    model: `${selection.group}::${selection.model}`,
  };
}

function videoReferenceRequest(
  config: AiConfig,
  parameters: {
    model: string;
    group: string;
    prompt: string;
    seconds: string;
    size: string;
  },
  images: string[],
  videos: string[],
  audios: string[],
): Record<string, unknown> {
  const frames =
    config.videoMode !== "reference" &&
    images.length <= 2 &&
    !videos.length &&
    !audios.length;
  const ratio = inferVideoRatio(config.size || "16:9");
  const resolution = normalizeVideoResolution(config.vquality);
  const generateAudio = boolConfig(config.videoGenerateAudio, true);
  const watermark = boolConfig(config.videoWatermark, false);
  const media = {
    ...(videos.length ? { videos } : {}),
    ...(audios.length ? { audios } : {}),
  };
  const body: Record<string, unknown> = {
    ...parameters,
    ...(images.length ? { images, input_reference: images[0] } : {}),
  };
  let metadata: Record<string, unknown> = {
    ...media,
    ...(ratio !== "auto" ? { ratio, aspect_ratio: ratio } : {}),
    resolution,
    resolution_name: resolution,
    generate_audio: generateAudio,
    watermark,
    ...(frames && images[0]
      ? { first_frame: images[0], first_frame_image: images[0] }
      : {}),
    ...(frames && images[1]
      ? { last_frame: images[1], last_frame_image: images[1] }
      : {}),
  };

  if (
    /^(?:doubao[-_]|seedance(?:-|$)|ep-)/i.test(parameters.model) ||
    /^minimax-h3$/i.test(parameters.model)
  ) {
    // Both multimodal plugins consume content roles. Avoid req.images here:
    // Doubao prepends it to content, duplicating every image otherwise.
    delete body.images;
    const content: Array<Record<string, unknown>> = images.map((url, index) => {
      let role = "reference_image";
      if (frames) role = index === 0 ? "first_frame" : "last_frame";
      return { type: "image_url", image_url: { url }, role };
    });
    content.push(
      ...videos.map((url) => ({
        type: "video_url",
        video_url: { url },
        role: "reference_video",
      })),
    );
    content.push(
      ...audios.map((url) => ({
        type: "audio_url",
        audio_url: { url },
        role: "reference_audio",
      })),
    );
    metadata = {
      content,
      resolution,
      generate_audio: generateAudio,
      watermark,
      ...(ratio !== "auto" ? { ratio } : {}),
    };
    if (/^minimax-h3$/i.test(parameters.model)) {
      metadata.resolution =
        resolution === "720p" ? "768P" : resolution.toUpperCase();
      metadata.aigc_watermark = watermark;
    }
  } else if (/^grok-imagine-video(?:-|$)/i.test(parameters.model)) {
    // xAI treats req.images with two items as reference images, even when the
    // caller intended first/last frames. Use its explicit frame/reference fields.
    delete body.images;
    metadata = {
      ...media,
      resolution,
      generate_audio: generateAudio,
      ...(ratio !== "auto" ? { aspect_ratio: ratio } : {}),
    };
    if (frames) {
      if (images[1]) metadata.last_frame = images[1];
    } else {
      delete body.input_reference;
      if (images.length) metadata.reference_images = images;
      if (audios.length) {
        metadata.reference_audios = audios.map((url) => ({ url }));
      }
    }
  } else if (/^kling(?:-|$)/i.test(parameters.model)) {
    // Kling's mode is std/pro, not the canvas frame/reference mode.
    metadata = {
      ...media,
      ...(ratio !== "auto" ? { aspect_ratio: ratio } : {}),
      ...(frames && images[1] ? { image_tail: images[1] } : {}),
    };
  } else if (/^vidu/i.test(parameters.model)) {
    metadata = { ...media, resolution, bgm: generateAudio };
    if (!frames && images.length) metadata.action = "reference_to_video";
  } else if (/^(?:jimeng|jimeng_)/i.test(parameters.model)) {
    // Jimeng sends image bodies in binary_data_base64, without a data-URL header.
    if (images.length) {
      body.images = images.map((image) =>
        image.replace(/^data:[^,]+;base64,/, ""),
      );
    }
    metadata = {
      ...media,
      ...(ratio !== "auto" ? { aspect_ratio: ratio } : {}),
    };
  } else if (/^veo(?:[.-]|$)/i.test(parameters.model)) {
    metadata = {
      resolution,
      generateAudio,
      ...(ratio !== "auto" ? { aspectRatio: ratio } : {}),
    };
    // The current Google/Vertex drivers consume the first images item.
    // Retain all extra media at the request boundary rather than copying it
    // into provider parameters, where it would invalidate the native request.
    Object.assign(body, media);
  }
  body.metadata = metadata;
  return body;
}

export async function pollVideoGenerationTask(
  config: AiConfig,
  task: VideoGenerationTask,
  options?: MediaRequestOptions,
): Promise<VideoGenerationTaskState> {
  if (task.provider !== "openai") {
    return {
      status: "failed",
      error: i18n.t(
        "This video task predates the site integration. Generate it again.",
      ),
    };
  }
  const selection = resolveSiteMediaModel(config, task.model, "video");
  const path = `/pg/videos/${encodeURIComponent(task.id)}`;
  const groupQuery = `?group=${encodeURIComponent(selection.group)}`;
  const video = await requestSiteMedia(
    path + groupQuery,
    { method: "GET" },
    async (response) =>
      unwrapVideoResponse((await response.json()) as ApiVideoResponse),
    options,
  );
  if (
    video.status === "failed" ||
    video.status === "cancelled" ||
    video.status === "canceled"
  ) {
    return {
      status: "failed",
      error: mediaErrorMessage(video.error) || apiText("videoGenerationFailed"),
    };
  }
  // Always download through the authenticated site endpoint, even when upstream exposes a URL.
  const hasResultUrl = [
    video.video_url,
    video.result_url,
    video.url,
    video.content?.video_url,
    video.content?.url,
  ].some((url) => typeof url === "string" && Boolean(url));
  if (video.status !== "completed" && !(hasResultUrl && !video.status)) {
    return { status: "pending" };
  }
  const blob = await requestSiteMediaBlob(
    `${path}/content${groupQuery}`,
    { method: "GET" },
    apiText("videoDownloadFailed"),
    options,
  );
  return { status: "completed", result: { blob } };
}

export async function storeGeneratedVideo(
  result: VideoGenerationResult,
): Promise<UploadedFile> {
  if (result.blob) return uploadMediaFile(result.blob, "video");
  if (result.url) {
    try {
      return await uploadMediaFile(result.url, "video");
    } catch {
      return {
        url: result.url,
        storageKey: "",
        bytes: 0,
        mimeType: result.mimeType || "video/mp4",
      };
    }
  }
  throw new Error(apiText("noPlayableVideo"));
}

function unwrapVideoResponse(payload: ApiVideoResponse): VideoResponse {
  if (!payload) throw new Error(apiText("noVideoTask"));
  if ("code" in payload && payload.code !== undefined) {
    assertMediaPayload(payload);
    if (!payload.data) throw new Error(apiText("noVideoTask"));
    return payload.data;
  }
  return payload as VideoResponse;
}

async function referenceMediaToDataUrl(
  item: ReferenceVideo | ReferenceAudio,
  errorKey: string,
  options?: MediaRequestOptions,
): Promise<string> {
  options?.signal?.throwIfAborted();
  if (!item.storageKey && item.url.startsWith("data:")) {
    if (!item.url.split(",", 2)[1]) throw new Error(apiText(errorKey));
    return item.url;
  }
  let blob = item.storageKey ? await getMediaBlob(item.storageKey) : null;
  if (!blob) {
    const url = item.storageKey
      ? await resolveMediaUrl(item.storageKey, item.url)
      : item.url;
    if (!url) throw new Error(apiText(errorKey));
    const response = await fetch(url, { signal: options?.signal });
    if (!response.ok) throw new Error(apiText(errorKey));
    blob = await response.blob();
  }
  options?.signal?.throwIfAborted();
  if (!blob.size) throw new Error(apiText(errorKey));
  return readFileAsDataUrl(
    new File([blob], item.name || "reference", {
      type: item.type || blob.type || "application/octet-stream",
    }),
  );
}

function normalizeVideoSize(value: string, resolution?: string): string | null {
  if (value === "auto") return null;
  if (/^\d+x\d+$/.test(value || "")) return value;
  const ratio = inferVideoRatio(value || "16:9");
  if (ratio === "auto") return null;
  return computeVideoSize(resolution || "720", ratio);
}

function normalizeVideoResolution(value: string): string {
  value = value.trim().toLowerCase();
  if (value === "4k" || value === "2k") return value;
  if (value === "low") return "480p";
  if (value === "auto" || value === "high" || value === "medium") return "720p";
  return `${value.replace(/p$/i, "") || "720"}p`;
}

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
