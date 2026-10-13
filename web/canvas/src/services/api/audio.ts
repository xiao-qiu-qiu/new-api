import i18n from "@/i18n";
import {
  audioMimeType,
  normalizeAudioFormatValue,
  normalizeAudioSpeedValue,
  normalizeAudioVoiceValue,
} from "@/lib/audio-generation";
import { uploadMediaFile, type UploadedFile } from "@/services/file-storage";
import type { AiConfig } from "@/stores/use-config-store";
import {
  requestSiteMediaBlob,
  resolveSiteMediaModel,
  type MediaRequestOptions,
} from "./site-media";

export async function requestAudioGeneration(
  config: AiConfig,
  prompt: string,
  options?: MediaRequestOptions,
): Promise<Blob> {
  const selection = resolveSiteMediaModel(
    config,
    config.model || config.audioModel,
    "audio",
  );
  const format = normalizeAudioFormatValue(config.audioFormat);
  const instructions = config.audioInstructions.trim();
  const blob = await requestSiteMediaBlob(
    "/pg/audio/speech",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...selection,
        input: prompt,
        voice: normalizeAudioVoiceValue(config.audioVoice),
        response_format: format,
        speed: Number(normalizeAudioSpeedValue(config.audioSpeed)),
        ...(instructions ? { instructions } : {}),
      }),
    },
    i18n.t("apiErrors.audioGenerationFailed"),
    options,
  );
  return blob.type.startsWith("audio/")
    ? blob
    : new Blob([blob], { type: audioMimeType(format) });
}

export async function storeGeneratedAudio(
  blob: Blob,
  format = "mp3",
): Promise<UploadedFile> {
  const audio = blob.type.startsWith("audio/")
    ? blob
    : new Blob([blob], { type: audioMimeType(format) });
  return uploadMediaFile(audio, "audio");
}
