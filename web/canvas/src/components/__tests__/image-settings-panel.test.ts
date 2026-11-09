// @vitest-environment jsdom
import { fireEvent, screen } from "@testing-library/dom";
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test, vi } from "vitest";
import type { SiteContext } from "@/integration/site-context";
import type { AiConfig } from "@/stores/use-config-store";

const context: SiteContext = {
  userId: 1,
  theme: "light",
  language: "en-US",
  group: "media",
  model: "gemini-3-pro-image",
  agentGroup: "media",
  agentModel: "gemini-3-pro-image",
  channels: [],
};
window.__newApiCanvasHost = {
  context,
  fetch: vi.fn(),
  selectAgentModel: vi.fn(),
};
const { ImageSettingsPanel } = await import("../image-settings-panel");
const { CanvasImageSettingsPopover } =
  await import("../canvas/canvas-image-settings-popover");
const { defaultConfig } = await import("@/stores/use-config-store");
const { canvasThemes } = await import("@/lib/canvas-theme");
const { default: i18n } = await import("@/i18n");
Object.defineProperty(globalThis, "IS_REACT_ACT_ENVIRONMENT", {
  configurable: true,
  value: true,
});
let root: Root | undefined;

test("a legacy Gemini node summary displays the effective resolution and ratio", () => {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root?.render(
      createElement(CanvasImageSettingsPopover, {
        config: {
          ...defaultConfig,
          model: "media::gemini-3-pro-image",
          size: "3840x2160",
          quality: "high",
          count: "1",
        },
        onConfigChange: vi.fn(),
      }),
    ),
  );

  const summary = screen.getByRole("button", { name: /4K · 16:9/ });
  expect(summary.textContent).not.toContain("3840x2160");
  expect(summary.textContent).not.toContain("high");
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  localStorage.clear();
  vi.clearAllMocks();
});

function renderPanel(overrides: Partial<AiConfig>) {
  let config: AiConfig = {
    ...defaultConfig,
    model: "media::gemini-3-pro-image",
    size: "16:9",
    quality: "2K",
    ...overrides,
  };
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  const render = () =>
    root?.render(
      createElement(ImageSettingsPanel, {
        config,
        onConfigChange,
        theme: canvasThemes.light,
      }),
    );
  const onConfigChange = vi.fn((key: keyof AiConfig, value: string) => {
    config = { ...config, [key]: value };
    render();
  });
  act(render);
  return { onConfigChange, getConfig: () => config };
}

test.each(["gemini-3-pro-image", "media::gemini-3-pro-image"])(
  "%s shows Gemini resolutions and supported ratios while hiding incompatible controls",
  (model) => {
    renderPanel({ model });

    for (const resolution of ["1K", "2K", "4K"]) {
      expect(screen.getByRole("button", { name: resolution })).toBeTruthy();
    }
    for (const ratio of [
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
    ]) {
      expect(screen.getByRole("button", { name: ratio })).toBeTruthy();
    }
    expect(screen.queryByRole("button", { name: "9:21" })).toBeNull();
    expect(
      screen.getAllByRole("button", {
        name: i18n.t("settingsPanels.common.auto"),
      }),
    ).toHaveLength(2);
    for (const quality of ["low", "medium", "high", "xhigh", "max"]) {
      expect(
        screen.queryByRole("button", {
          name: i18n.t(`settingsPanels.common.${quality}`),
        }),
      ).toBeNull();
    }
    expect(screen.queryByRole("spinbutton", { name: "W" })).toBeNull();
    expect(screen.queryByRole("spinbutton", { name: "H" })).toBeNull();
    expect(screen.queryByRole("switch")).toBeNull();
    expect(
      screen.queryByText(i18n.t("settingsPanels.image.transparent")),
    ).toBeNull();
  },
);

test("a Gemini imageModel without an explicit model uses the same constrained controls", () => {
  renderPanel({ model: "", imageModel: "media::gemini-3-pro-image" });

  expect(screen.getByRole("button", { name: "4:5" })).toBeTruthy();
  expect(screen.queryByRole("spinbutton", { name: "W" })).toBeNull();
  expect(
    screen.queryByRole("button", {
      name: i18n.t("settingsPanels.common.high"),
    }),
  ).toBeNull();
});

test("a legacy 3840x2160/high Gemini node displays 4K and 16:9 and preserves 4K when its ratio changes", () => {
  const panel = renderPanel({ size: "3840x2160", quality: "high" });

  expect(
    screen.getByRole("button", { name: "4K" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(
    screen.getByRole("button", { name: "16:9" }).getAttribute("aria-pressed"),
  ).toBe("true");

  act(() => fireEvent.click(screen.getByRole("button", { name: "4:5" })));

  expect(panel.onConfigChange).toHaveBeenCalledWith("size", "4:5");
  expect(panel.getConfig()).toMatchObject({ size: "4:5", quality: "4K" });
  expect(
    screen.getByRole("button", { name: "4K" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(
    screen.getByRole("button", { name: "4:5" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(
    screen.getByRole("button", { name: "16:9" }).getAttribute("aria-pressed"),
  ).toBe("false");
});

test.each(["1K", "2K", "4K"])(
  "selecting Gemini resolution %s saves uppercase quality and preserves the selected ratio",
  (resolution) => {
    const panel = renderPanel({ size: "4:5", quality: "auto" });

    act(() =>
      fireEvent.click(screen.getByRole("button", { name: resolution })),
    );

    expect(panel.onConfigChange).toHaveBeenCalledWith("quality", resolution);
    expect(panel.getConfig()).toMatchObject({
      size: "4:5",
      quality: resolution,
    });
    expect(
      screen
        .getByRole("button", { name: resolution })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen.getByRole("button", { name: "4:5" }).getAttribute("aria-pressed"),
    ).toBe("true");
  },
);

test("selecting Gemini Auto resolution preserves the ratio and its independent selected state", () => {
  const panel = renderPanel({ size: "4:5", quality: "2K" });
  const [resolutionAuto, ratioAuto] = screen.getAllByRole("button", {
    name: i18n.t("settingsPanels.common.auto"),
  });

  act(() => fireEvent.click(resolutionAuto));

  expect(panel.onConfigChange).toHaveBeenCalledWith("quality", "auto");
  expect(panel.getConfig()).toMatchObject({ size: "4:5", quality: "auto" });
  expect(resolutionAuto.getAttribute("aria-pressed")).toBe("true");
  expect(ratioAuto.getAttribute("aria-pressed")).toBe("false");
  expect(
    screen.getByRole("button", { name: "4:5" }).getAttribute("aria-pressed"),
  ).toBe("true");
});

test("selecting Gemini Auto ratio preserves an explicit resolution", () => {
  const panel = renderPanel({ size: "16:9", quality: "2K" });
  const [resolutionAuto, ratioAuto] = screen.getAllByRole("button", {
    name: i18n.t("settingsPanels.common.auto"),
  });

  act(() => fireEvent.click(ratioAuto));

  expect(panel.onConfigChange).toHaveBeenCalledWith("size", "auto");
  expect(panel.getConfig()).toMatchObject({ size: "auto", quality: "2K" });
  expect(ratioAuto.getAttribute("aria-pressed")).toBe("true");
  expect(resolutionAuto.getAttribute("aria-pressed")).toBe("false");
  expect(
    screen.getByRole("button", { name: "2K" }).getAttribute("aria-pressed"),
  ).toBe("true");
});

test("a GPT image model retains generic quality and editable pixel dimensions", () => {
  const panel = renderPanel({
    model: "media::gpt-image-1",
    size: "1024x1024",
    quality: "auto",
  });

  for (const quality of ["low", "medium", "high", "xhigh", "max"]) {
    expect(
      screen.getByRole("button", {
        name: i18n.t(`settingsPanels.common.${quality}`),
      }),
    ).toBeTruthy();
  }
  act(() =>
    fireEvent.click(
      screen.getByRole("button", {
        name: i18n.t("settingsPanels.common.high"),
      }),
    ),
  );
  expect(panel.onConfigChange).toHaveBeenCalledWith("quality", "high");

  const width = screen.getByRole("spinbutton", {
    name: "W",
  }) as HTMLInputElement;
  const height = screen.getByRole("spinbutton", {
    name: "H",
  }) as HTMLInputElement;
  expect(width.disabled).toBe(false);
  expect(height.disabled).toBe(false);
  expect(width.value).toBe("1024");
  expect(height.value).toBe("1024");

  act(() => {
    width.focus();
    fireEvent.change(width, { target: { value: "1537" } });
    width.blur();
  });

  expect(panel.onConfigChange).toHaveBeenCalledWith("size", "1552x1024");
  expect(panel.getConfig()).toMatchObject({
    size: "1552x1024",
    quality: "high",
  });
});
