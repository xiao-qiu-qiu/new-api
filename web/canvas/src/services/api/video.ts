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
  // OpenAI accepts one uploaded input_reference. Additional media uses the gateway's JSON task extension.
  if (images.length > 1 || videos.length || audios.length) {
    const mode =
      config.videoMode === "reference" || images.length > 2
        ? "reference"
        : "frames";
    init = {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...parameters,
        ...(images.length ? { images } : {}),
        metadata: {
          mode,
          ratio: inferVideoRatio(config.size || "16:9"),
          resolution_name: normalizeVideoResolution(config.vquality),
          generate_audio: boolConfig(config.videoGenerateAudio, true),
          watermark: boolConfig(config.videoWatermark, false),
          ...(mode === "frames" && images[0] ? { first_frame: images[0] } : {}),
          ...(mode === "frames" && images[1] ? { last_frame: images[1] } : {}),
          ...(videos.length ? { videos } : {}),
          ...(audios.length ? { audios } : {}),
        },
      }),
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
    // Vendor options stay optional for the strict OpenAI models.
    if (!/^sora(?:-|$)/i.test(selection.model)) {
      form.set(
        "generate_audio",
        String(boolConfig(config.videoGenerateAudio, true)),
      );
      form.set("watermark", String(boolConfig(config.videoWatermark, false)));
      form.set("resolution_name", normalizeVideoResolution(config.vquality));
      const ratio = inferVideoRatio(config.size || "16:9");
      if (ratio !== "auto") form.set("ratio", ratio);
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
