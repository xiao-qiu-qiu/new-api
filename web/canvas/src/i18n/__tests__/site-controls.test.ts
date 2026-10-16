import { afterAll, expect, it, vi } from "vitest";

import { toIntlLocale } from "../languages";
import siteEn from "../locales/site-controls/en.json";
import siteFr from "../locales/site-controls/fr.json";
import siteJa from "../locales/site-controls/ja.json";
import siteRu from "../locales/site-controls/ru.json";
import siteVi from "../locales/site-controls/vi.json";
import siteZh from "../locales/site-controls/zh.json";
import siteZhTW from "../locales/site-controls/zh-TW.json";

vi.stubGlobal("window", {
  __newApiCanvasHost: { context: { language: "zhCN" } },
});
const { default: i18n, getCanvasLocale } = await import("../index");
afterAll(() => vi.unstubAllGlobals());

it.each([
  ["zhCN", "zh-CN"],
  ["zh-CN", "zh-CN"],
  ["zh", "zh-CN"],
  ["zhTW", "zh-TW"],
  ["zh-TW", "zh-TW"],
  ["zh-Hant", "zh-TW"],
  ["en", "en-US"],
  ["en-US", "en-US"],
  ["fr", "fr"],
  ["fr-FR", "fr"],
  ["ru", "ru"],
  ["ja", "ja"],
  ["vi", "vi"],
  ["invalid", "en-US"],
  ["", "en-US"],
  [null, "en-US"],
  [undefined, "en-US"],
])(
  "maps host language %s to a supported canvas and Intl locale",
  (language, expected) => {
    const locale = getCanvasLocale(language);
    expect(locale).toBe(expected);
    expect(() => new Intl.DateTimeFormat(toIntlLocale(locale))).not.toThrow();
  },
);

it("keeps the new controls complete and preserves interpolation in all seven languages", () => {
  const keys = Object.keys(siteEn).sort();
  for (const [language, bundle] of Object.entries({
    en: siteEn,
    zh: siteZh,
    "zh-TW": siteZhTW,
    fr: siteFr,
    ru: siteRu,
    ja: siteJa,
    vi: siteVi,
  })) {
    expect(Object.keys(bundle).sort()).toEqual(keys);
    for (const key of keys) {
      const source = siteEn[key as keyof typeof siteEn];
      const value = bundle[key as keyof typeof siteEn];
      expect(value.trim()).not.toBe("");
      expect((value.match(/{{\w+}}/g) || []).sort()).toEqual(
        (source.match(/{{\w+}}/g) || []).sort(),
      );
      expect(i18n.t(key, { lng: getCanvasLocale(language) })).toBe(value);
    }
  }
});

it("updates new controls when switching languages and falls back to upstream English", async () => {
  await i18n.changeLanguage(getCanvasLocale("zhCN"));
  expect(i18n.t("Model groups")).toBe(siteZh["Model groups"]);
  expect(i18n.t("common.cancel")).toBe("取消");
  expect(i18n.t("agent.composer.selectModel", { model: "gpt-6" })).toBe(
    "选择模型。当前：gpt-6",
  );

  await i18n.changeLanguage(getCanvasLocale("fr"));
  expect(i18n.t("Model groups")).toBe(siteFr["Model groups"]);
  expect(i18n.t("agent.route.site")).toBe(siteFr["agent.route.site"]);
  expect(i18n.t("common.cancel")).toBe("Cancel");
  expect(i18n.t("generation.pending", { returnObjects: true })).toEqual([
    "Creating image",
    "Almost there",
    "Just a little longer",
    "Refining details",
  ]);

  await i18n.changeLanguage(getCanvasLocale("zhTW"));
  expect(i18n.t("agent.composer.effort.auto")).toBe("自動");
  expect(i18n.t("common.cancel")).toBe("Cancel");
});
