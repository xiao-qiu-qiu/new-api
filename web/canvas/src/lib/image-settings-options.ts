import i18n from "@/i18n";
import {
  inferMediaRatio,
  inferMediaScale,
  mediaRatioOptions,
  mediaScaleOptions,
} from "@/lib/media-size";

export const qualityOptions = [
  { value: "auto", labelKey: "auto" },
  { value: "low", labelKey: "low" },
  { value: "medium", labelKey: "medium" },
  { value: "high", labelKey: "high" },
  { value: "xhigh", labelKey: "xhigh" },
  { value: "max", labelKey: "max" },
];

export const imageQualityOptions = qualityOptions.map((item) => ({
  value: item.value,
  get label() {
    return i18n.t(`settingsPanels.common.${item.labelKey}`);
  },
}));
export const imageAspectOptions = mediaRatioOptions.map((item) => ({
  value: item.value,
  label:
    item.value === "auto" ? i18n.t("settingsPanels.common.auto") : item.value,
}));
export const imageScaleOptions = mediaScaleOptions.map((value) => ({
  value,
  label: value === "auto" ? i18n.t("settingsPanels.common.auto") : value,
}));

export function imageQualityLabel(value: string) {
  return qualityOptions.some((item) => item.value === value)
    ? i18n.t(`settingsPanels.common.${value}`)
    : value;
}

export function imageSizeLabel(size: string) {
  const scale = inferMediaScale(size);
  const ratio = inferMediaRatio(size);
  if (ratio === "auto" || size === "auto") {
    return i18n.t("settingsPanels.common.auto");
  }
  if (scale === "auto") return ratio;
  return `${scale} · ${ratio}`;
}
