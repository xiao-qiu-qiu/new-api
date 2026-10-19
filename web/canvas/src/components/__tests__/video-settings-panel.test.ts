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
  model: "video",
  agentGroup: "media",
  agentModel: "video",
  channels: [],
};
window.__newApiCanvasHost = {
  context,
  fetch: vi.fn(),
  selectAgentModel: vi.fn(),
};
const { VideoSettingsPanel } = await import("../video-settings-panel");
const { defaultConfig } = await import("@/stores/use-config-store");
const { canvasThemes } = await import("@/lib/canvas-theme");
const { normalizeVideoConfig } = await import("@/lib/video-model-constraints");
Object.defineProperty(globalThis, "IS_REACT_ACT_ENVIRONMENT", {
  configurable: true,
  value: true,
});
let root: Root | undefined;
afterEach(() => {
  act(() => root?.unmount());
  document.body.replaceChildren();
});

test("a constrained model shows only supported video choices and blocks free dimensions", () => {
  const onConfigChange = vi.fn((key: keyof AiConfig, value: string) => {
    config = { ...config, [key]: value };
    renderPanel();
  });
  let config: AiConfig = {
    ...defaultConfig,
    model: "media::video",
    vquality: "1080",
    size: "720x1280",
    channels: [
      {
        id: "media",
        name: "Media",
        baseUrl: "",
        apiKey: "",
        apiFormat: "openai",
        models: [
          {
            name: "video",
            capability: "video",
            videoCapabilities: {
              resolutions: ["720p"],
              aspect_ratios: ["16:9"],
              durations: [5, 10],
            },
          },
          {
            name: "restricted-video",
            capability: "video",
            videoCapabilities: {
              resolutions: ["480p"],
              aspect_ratios: ["auto", "16:9"],
              durations: [4, 8],
            },
          },
        ],
      },
    ],
  };
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  const renderPanel = () =>
    root?.render(
      createElement(VideoSettingsPanel, {
        config,
        onConfigChange,
        theme: canvasThemes.light,
      }),
    );
  act(renderPanel);

  expect(screen.queryByRole("button", { name: "480p" })).toBeNull();
  expect(screen.getByRole("button", { name: "720p" })).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "720p" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(
    screen.getByRole("button", { name: "16:9" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(
    screen.getByRole("button", { name: "5s" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(screen.queryByRole("button", { name: "9:16" })).toBeNull();
  expect(
    screen
      .getAllByRole("spinbutton")
      .every((input) => (input as HTMLInputElement).disabled),
  ).toBe(true);
  expect(
    (
      screen.getByRole("spinbutton", {
        name: "Video width",
      }) as HTMLInputElement
    ).disabled,
  ).toBe(true);
  expect(
    (
      screen.getByRole("spinbutton", {
        name: "Video height",
      }) as HTMLInputElement
    ).disabled,
  ).toBe(true);
  act(() => fireEvent.click(screen.getByRole("button", { name: "10s" })));
  expect(onConfigChange).toHaveBeenCalledWith("videoSeconds", "10");
  expect(
    screen.getByRole("button", { name: "10s" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(
    screen.getByRole("button", { name: "5s" }).getAttribute("aria-pressed"),
  ).toBe("false");
  act(() => fireEvent.click(screen.getByRole("button", { name: "720p" })));
  expect(config).toMatchObject({ vquality: "720", size: "1280x720" });

  act(() => {
    config = {
      ...config,
      model: "media::restricted-video",
      vquality: "1080",
      size: "720x1280",
    };
    renderPanel();
  });
  expect(screen.queryByRole("button", { name: "720p" })).toBeNull();
  expect(
    screen.getByRole("button", { name: "480p" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(
    screen.getByRole("button", { name: "Auto" }).getAttribute("aria-pressed"),
  ).toBe("true");
  act(() => fireEvent.click(screen.getByRole("button", { name: "480p" })));
  expect(config).toMatchObject({ vquality: "480", size: "auto" });
  act(() => fireEvent.click(screen.getByRole("button", { name: "16:9" })));
  expect(config).toMatchObject({ vquality: "480", size: "854x480" });
  expect(
    screen.getByRole("button", { name: "16:9" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(
    screen.getByRole("button", { name: "Auto" }).getAttribute("aria-pressed"),
  ).toBe("false");
  expect(
    screen.getByRole("button", { name: "8s" }).getAttribute("aria-pressed"),
  ).toBe("true");
  expect(normalizeVideoConfig(config)).toMatchObject({
    vquality: "480",
    size: "854x480",
  });
});
