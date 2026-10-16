import i18n from "@/i18n";
import { getSiteContext, siteFetch } from "@/integration/site-context";
import {
  decodeChannelModel,
  resolveModelChannel,
  resolveModelRequestConfig,
  type AiConfig,
  type ModelCapability,
} from "@/stores/use-config-store";

export type MediaRequestOptions = { signal?: AbortSignal };
export type SiteMediaModel = { model: string; group: string };

/** Channel IDs are site group names; never let the upstream resolver replace a stale selection. */
export function resolveSiteMediaModel(
  config: AiConfig,
  selection: string,
  capability: ModelCapability,
): SiteMediaModel {
  const value = selection.trim();
  const decoded = decodeChannelModel(value);
  const channel = resolveModelChannel(config, value);
  const name = decoded?.model || value;
  if (
    !value ||
    !channel.id ||
    (decoded && decoded.channelId !== channel.id) ||
    !channel.models.some(
      (model) =>
        model.name === name &&
        (model.capability === capability ||
          model.capabilities?.includes(capability)),
    )
  ) {
    throw new Error(
      i18n.t(
        "The selected model is unavailable in this group. Select another model.",
      ),
    );
  }
  const requestConfig = resolveModelRequestConfig(
    config,
    `${channel.id}::${name}`,
  );
  return { model: requestConfig.model, group: channel.id };
}

export function siteMediaModels(group?: string): string[] {
  return [
    ...new Set(
      getSiteContext()
        .channels.filter(
          (channel) => group === undefined || channel.id === group,
        )
        .flatMap((channel) => channel.models.map((model) => model.name)),
    ),
  ].sort((a, b) => a.localeCompare(b));
}

export function mediaErrorMessage(value: unknown): string {
  if (typeof value === "string") {
    try {
      return mediaErrorMessage(JSON.parse(value));
    } catch {
      if (/<[a-z][\s\S]*>/i.test(value)) return "";
      return value;
    }
  }
  if (!value || typeof value !== "object") return "";
  const payload = value as Record<string, unknown>;
  return (
    mediaErrorMessage(payload.error) ||
    mediaErrorMessage(payload.message) ||
    mediaErrorMessage(payload.msg) ||
    mediaErrorMessage(payload.detail)
  );
}

export function assertMediaPayload(payload: unknown): void {
  if (!payload || typeof payload !== "object") return;
  const value = payload as Record<string, unknown>;
  if (
    value.error ||
    value.success === false ||
    (value.code !== undefined && value.code !== 0 && value.code !== "0")
  ) {
    throw new Error(
      mediaErrorMessage(value) || i18n.t("apiErrors.requestFailed"),
    );
  }
}

/** Keep timeout and cancellation active until the response body (including SSE) has been consumed. */
export async function requestSiteMedia<T>(
  path: string,
  init: RequestInit,
  read: (response: Response) => Promise<T>,
  options?: MediaRequestOptions,
): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(
    () => controller.abort(new DOMException("Timed out", "TimeoutError")),
    10 * 60_000,
  );
  const signal = options?.signal
    ? AbortSignal.any([options.signal, controller.signal])
    : controller.signal;
  try {
    signal.throwIfAborted();
    const response = await siteFetch(path, { ...init, signal });
    if (!response.ok) {
      const message = mediaErrorMessage(await response.text());
      if (message) throw new Error(i18n.t(message));
      if (response.status === 401) {
        throw new Error(i18n.t("Your session has expired. Sign in again."));
      }
      if (response.status === 403) {
        throw new Error(
          i18n.t("You do not have access to this model or group."),
        );
      }
      if (response.status === 429) {
        throw new Error(i18n.t("apiErrors.rateLimited"));
      }
      throw new Error(
        i18n.t("Media request failed (HTTP {{status}}).", {
          status: response.status,
        }),
      );
    }
    const result = await read(response);
    signal.throwIfAborted();
    return result;
  } catch (error) {
    if (options?.signal?.aborted) {
      throw new DOMException("Aborted", "AbortError");
    }
    if (controller.signal.aborted) {
      throw new Error(i18n.t("Media request timed out. Try again."));
    }
    throw error;
  } finally {
    clearTimeout(timer);
  }
}

export function requestSiteMediaJson<T>(
  path: string,
  init: RequestInit,
  options?: MediaRequestOptions,
): Promise<T> {
  return requestSiteMedia(
    path,
    init,
    async (response) => {
      const payload: T = await response.json();
      assertMediaPayload(payload);
      return payload;
    },
    options,
  );
}

export function requestSiteMediaBlob(
  path: string,
  init: RequestInit,
  emptyMessage: string,
  options?: MediaRequestOptions,
): Promise<Blob> {
  return requestSiteMedia(
    path,
    init,
    async (response) => {
      const blob = await response.blob();
      // A successful HTTP status can still contain a gateway/provider error envelope.
      if (!blob.size || /json|text\/|html/i.test(blob.type)) {
        throw new Error(mediaErrorMessage(await blob.text()) || emptyMessage);
      }
      return blob;
    },
    options,
  );
}
