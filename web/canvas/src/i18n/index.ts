import i18n from "i18next";
import { initReactI18next } from "react-i18next";

import enUS from "@/i18n/locales/en-US";
import zhCN from "@/i18n/locales/zh-CN";
import { getSiteContext } from "@/integration/site-context";
import { getCanvasLocale, type AppLocale } from "./languages";
import siteEn from "./locales/site-controls/en.json";
import siteZh from "./locales/site-controls/zh.json";
import siteZhTW from "./locales/site-controls/zh-TW.json";
import siteFr from "./locales/site-controls/fr.json";
import siteRu from "./locales/site-controls/ru.json";
import siteJa from "./locales/site-controls/ja.json";
import siteVi from "./locales/site-controls/vi.json";

export { getCanvasLocale, type AppLocale } from "./languages";

// Expand only the small flat overlay; upstream nested keys and arrays stay intact.
function withSiteControls(
  base: Record<string, unknown>,
  controls: Record<string, string>,
): Record<string, unknown> {
  const resource = structuredClone(base);
  for (const [key, value] of Object.entries(controls)) {
    const segments = key.startsWith("agent.") ? key.split(".") : [key];
    const leaf = segments.pop() || key;
    let target = resource;
    for (const segment of segments) {
      const child = target[segment];
        if (!child || typeof child !== "object" || Array.isArray(child)) {
        target[segment] = {};
        }
      target = target[segment] as Record<string, unknown>;
    }
    target[leaf] = value;
  }
  return resource;
}

const LOCALE_STORAGE_KEY = "infinite-canvas:locale";

i18n.use(initReactI18next).init({
  resources: {
    "zh-CN": { translation: withSiteControls(zhCN, siteZh) },
    "en-US": { translation: withSiteControls(enUS, siteEn) },
    "zh-TW": { translation: withSiteControls({}, siteZhTW) },
    fr: { translation: withSiteControls({}, siteFr) },
    ru: { translation: withSiteControls({}, siteRu) },
    ja: { translation: withSiteControls({}, siteJa) },
    vi: { translation: withSiteControls({}, siteVi) },
  },
  lng: getCanvasLocale(getSiteContext().language),
  fallbackLng: "en-US",
  supportedLngs: ["zh-CN", "zh-TW", "en-US", "fr", "ru", "ja", "vi"],
  initAsync: false,
  interpolation: { escapeValue: false },
  react: { useSuspense: false },
});

export function changeAppLocale(locale: AppLocale) {
  localStorage.setItem(LOCALE_STORAGE_KEY, locale);
  return i18n.changeLanguage(locale);
}

export default i18n;
