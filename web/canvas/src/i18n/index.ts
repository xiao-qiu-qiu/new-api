import i18n from "i18next";
import { initReactI18next } from "react-i18next";

import enUS from "@/i18n/locales/en-US";
import zhCN from "@/i18n/locales/zh-CN";
import { getSiteContext } from "@/integration/site-context";

export type AppLocale = "zh-CN" | "en-US";

const LOCALE_STORAGE_KEY = "infinite-canvas:locale";

i18n.use(initReactI18next).init({
    resources: {
        "zh-CN": { translation: zhCN },
        "en-US": { translation: enUS },
    },
    lng: getSiteContext().language.startsWith("zh") ? "zh-CN" : "en-US",
    fallbackLng: "zh-CN",
    supportedLngs: ["zh-CN", "en-US"],
    initAsync: false,
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
});

export function changeAppLocale(locale: AppLocale) {
    localStorage.setItem(LOCALE_STORAGE_KEY, locale);
    return i18n.changeLanguage(locale);
}

export default i18n;
