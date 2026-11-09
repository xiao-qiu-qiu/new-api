import {
  inferMediaScale,
  parseAspectRatio,
  parsePixelSize,
} from "@/lib/media-size";

export const GEMINI_IMAGE_RATIOS = [
  "1:1",
  "2:3",
  "3:2",
  "3:4",
  "4:3",
  "4:5",
  "5:4",
  "9:16",
  "16:9",
  "21:9",
] as const;

export function isGeminiImageModel(model: string): boolean {
  return /^gemini-(?:3(?:\.1)?-(?:[\w.-]+-)?image(?:-[\w.-]+)?|nano-banana-[\w.-]+)$/i.test(
    model.trim(),
  );
}

export function resolveGeminiImageSettings(
  size: string,
  quality: string,
): { scale: "auto" | "1k" | "2k" | "4k"; ratio: string } {
  const value = size.trim().toLowerCase();
  const resolution = quality.trim().toLowerCase();
  let inferredScale = /^[124]k$/.test(value) ? value : inferMediaScale(value);
  // These native wide sizes cross the generic canvas long-edge thresholds.
  if (value === "1584x672") inferredScale = "1k";
  if (value === "3168x1344") inferredScale = "2k";
  let scale: "auto" | "1k" | "2k" | "4k" = "auto";
  if (resolution === "1k" || resolution === "2k" || resolution === "4k") {
    scale = resolution;
  } else if (
    inferredScale === "1k" ||
    inferredScale === "2k" ||
    inferredScale === "4k"
  ) {
    // Legacy quality labels have no resolution semantics; recover it from size.
    scale = inferredScale;
  }

  if (GEMINI_IMAGE_RATIOS.some((ratio) => ratio === value)) {
    return { scale, ratio: value };
  }
  const dimensions = parsePixelSize(value) || parseAspectRatio(value);
  if (
    !dimensions ||
    !Number.isFinite(dimensions.width) ||
    !Number.isFinite(dimensions.height) ||
    dimensions.width <= 0 ||
    dimensions.height <= 0
  ) {
    return { scale, ratio: "auto" };
  }

  const target = dimensions.width / dimensions.height;
  let ratio: string = "auto";
  let nearestDistance = Infinity;
  for (const candidate of GEMINI_IMAGE_RATIOS) {
    const [width, height] = candidate.split(":").map(Number);
    const distance = Math.abs(width / height - target);
    if (distance === 0) return { scale, ratio: candidate };
    if (distance < nearestDistance) {
      ratio = candidate;
      nearestDistance = distance;
    }
  }
  return { scale, ratio };
}
