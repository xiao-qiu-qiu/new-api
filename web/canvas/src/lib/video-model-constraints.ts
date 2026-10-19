import i18n from "@/i18n";
import {
  computeVideoSize,
  inferVideoRatio,
  parseAspectRatio,
  parsePixelSize,
  parseVideoResolution,
  VIDEO_SECONDS_MAX,
  VIDEO_SECONDS_MIN,
} from "@/lib/media-size";
import { decodeChannelModel, type AiConfig } from "@/stores/use-config-store";

export type VideoModelCapabilities = {
  resolutions?: string[];
  aspect_ratios?: string[];
  durations?: number[];
  max_duration_seconds?: number;
  max_reference_images?: number;
  max_reference_audio?: number;
  max_reference_videos?: number;
};

export type VideoModelConstraints = {
  resolutions?: string[];
  aspectRatios?: string[];
  durations?: number[];
  minDurationSeconds: number;
  maxDurationSeconds: number;
  maxReferenceImages?: number;
  maxReferenceAudio?: number;
  maxReferenceVideos?: number;
};

export function getVideoModelConstraints(
  config: AiConfig,
  model = config.model || config.videoModel,
): VideoModelConstraints {
  const decoded = decodeChannelModel(model);
  const channel = decoded
    ? config.channels.find((item) => item.id === decoded.channelId)
    : config.channels.find((item) =>
        item.models.some((entry) => entry.name === model),
      );
  const capabilities = channel?.models.find(
    (item) => item.name === (decoded?.model || model),
  )?.videoCapabilities;
  const resolutions = [
    ...new Set(
      (capabilities?.resolutions || [])
        .filter(
          (value) =>
            /^\d+p?$/i.test(value) && Number(value.replace(/p$/i, "")) > 0,
        )
        .map((value) => String(Number(value.replace(/p$/i, "")))),
    ),
  ];
  const aspectRatios = [
    ...new Set(
      (capabilities?.aspect_ratios || []).filter(
        (value) => value === "auto" || Boolean(parseAspectRatio(value)),
      ),
    ),
  ];
  const durations = [
    ...new Set(
      (capabilities?.durations || []).filter(
        (value) => Number.isInteger(value) && value > 0,
      ),
    ),
  ].sort((a, b) => a - b);
  const configuredMaximum = capabilities?.max_duration_seconds;
  const maxDurationSeconds =
    configuredMaximum &&
    Number.isInteger(configuredMaximum) &&
    configuredMaximum > 0
      ? configuredMaximum
      : durations.at(-1) || VIDEO_SECONDS_MAX;
  const allowedDurations = durations.filter(
    (value) => value <= maxDurationSeconds,
  );
  return {
    resolutions: resolutions.length ? resolutions : undefined,
    aspectRatios: aspectRatios.length ? aspectRatios : undefined,
    durations: allowedDurations.length ? allowedDurations : undefined,
    minDurationSeconds:
      allowedDurations[0] || Math.min(VIDEO_SECONDS_MIN, maxDurationSeconds),
    maxDurationSeconds,
    maxReferenceImages: referenceLimit(capabilities?.max_reference_images),
    maxReferenceAudio: referenceLimit(capabilities?.max_reference_audio),
    maxReferenceVideos: referenceLimit(capabilities?.max_reference_videos),
  };
}

export function normalizeVideoConfig(
  config: AiConfig,
  model = config.model || config.videoModel,
): AiConfig {
  const constraints = getVideoModelConstraints(config, model);
  const currentResolution = parseVideoResolution(config.vquality);
  const resolution =
    constraints.resolutions?.includes(currentResolution) === false
      ? constraints.resolutions[0]
      : currentResolution;
  const currentRatio = getVideoConfigAspectRatio(config, constraints);
  const ratio =
    constraints.aspectRatios?.includes(currentRatio) === false
      ? constraints.aspectRatios[0]
      : currentRatio;
  let size = config.size;
  if (
    ratio !== currentRatio ||
    resolution !== currentResolution ||
    constraints.aspectRatios ||
    constraints.resolutions
  ) {
    size = computeVideoSize(resolution, ratio);
  }
  const rawSeconds = Number(config.videoSeconds);
  let seconds = Math.min(
    constraints.maxDurationSeconds,
    Math.max(
      constraints.minDurationSeconds,
      Math.floor(
        Number.isFinite(rawSeconds) && rawSeconds > 0 ? rawSeconds : 6,
      ),
    ),
  );
  if (constraints.durations) {
    seconds = constraints.durations.reduce(
      (closest, value) =>
        Math.abs(value - seconds) < Math.abs(closest - seconds)
          ? value
          : closest,
      constraints.durations[0],
    );
  }
  return {
    ...config,
    vquality: resolution,
    size,
    videoSeconds: String(seconds),
  };
}

export function getVideoConfigAspectRatio(
  config: AiConfig,
  constraints = getVideoModelConstraints(config),
): string {
  if (constraints.aspectRatios?.includes(config.size)) return config.size;
  const dimensions =
    parsePixelSize(config.size) || parseAspectRatio(config.size);
  const configuredRatio = constraints.aspectRatios?.find((value) => {
    const ratio = parseAspectRatio(value);
    return (
      ratio &&
      dimensions &&
      Math.abs(
        ratio.width / ratio.height - dimensions.width / dimensions.height,
      ) < 0.01
    );
  });
  return configuredRatio || inferVideoRatio(config.size);
}

export function validateVideoReferenceCounts(
  config: AiConfig,
  counts: { images: number; audios: number; videos: number },
): void {
  const constraints = getVideoModelConstraints(config);
  const limits = [
    {
      count: counts.images,
      max: constraints.maxReferenceImages,
      key: "This model supports up to {{max}} reference images.",
    },
    {
      count: counts.audios,
      max: constraints.maxReferenceAudio,
      key: "This model supports up to {{max}} reference audio files.",
    },
    {
      count: counts.videos,
      max: constraints.maxReferenceVideos,
      key: "This model supports up to {{max}} reference videos.",
    },
  ];
  for (const limit of limits) {
    if (limit.max !== undefined && limit.count > limit.max) {
      throw new Error(i18n.t(limit.key, { max: limit.max }));
    }
  }
}

function referenceLimit(value: number | undefined): number | undefined {
  return Number.isInteger(value) && value !== undefined && value >= 0
    ? value
    : undefined;
}
