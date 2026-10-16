import {
  convertDetectedLanguage,
  normalizeInterfaceLanguage,
  toIntlLocale,
} from "../../../src/i18n/languages";

// Keep upstream's English locale while sharing host language normalization.
export type AppLocale = "zh-CN" | "zh-TW" | "en-US" | "fr" | "ru" | "ja" | "vi";

export function getCanvasLocale(language?: string | null): AppLocale {
  const detected = convertDetectedLanguage(language || "en");
  const normalized = normalizeInterfaceLanguage(detected.split("-")[0]);
  const locale = toIntlLocale(normalized) || "en-US";
  return locale === "en" ? "en-US" : (locale as AppLocale);
}

export { toIntlLocale };
