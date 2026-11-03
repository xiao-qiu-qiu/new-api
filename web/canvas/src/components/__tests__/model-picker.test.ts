// @vitest-environment jsdom
import { fireEvent, screen, waitFor, within } from "@testing-library/dom";
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test, vi } from "vitest";

import type { AiConfig } from "@/stores/use-config-store";

window.__newApiCanvasHost = {
  context: {
    userId: 1,
    theme: "light",
    language: "en-US",
    group: "standard",
    model: "image",
    agentGroup: "",
    agentModel: "",
    channels: [],
  },
  fetch: vi.fn(),
  selectAgentModel: vi.fn(),
};
const { ConfigProvider } = await import("antd");
const { ModelPicker } = await import("../model-picker");
const { defaultConfig } = await import("@/stores/use-config-store");
const { default: i18n } = await import("@/i18n");
let root: Root;
let viewportWidth = 1200;

beforeEach(async () => {
  viewportWidth = 1200;
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches:
      query.includes("min-width") &&
      viewportWidth >= Number(query.match(/\d+/)?.[0]),
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await i18n.changeLanguage("en-US");
});
afterEach(() => {
  act(() => root.unmount());
  document.body.replaceChildren();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function renderPicker(
  config: AiConfig,
  onChange = vi.fn(),
  capability: "image" | "video" = "image",
) {
  act(() =>
    root.render(
      createElement(
        ConfigProvider,
        { theme: { token: { motion: false } } },
        createElement(ModelPicker, {
          config,
          value: config.model,
          capability,
          onChange,
        }),
      ),
    ),
  );
  return onChange;
}

function imageConfig(): AiConfig {
  return {
    ...defaultConfig,
    model: "standard::image",
    channels: [
      {
        id: "standard",
        name: "Standard",
        baseUrl: "",
        apiKey: "",
        apiFormat: "openai",
        models: [
          {
            name: "image",
            capability: "image",
            pricing: { summary: "$0.04 / image" },
          },
          { name: "unpriced-image", capability: "image" },
        ],
      },
      {
        id: "discount",
        name: "Discount",
        baseUrl: "",
        apiKey: "",
        apiFormat: "openai",
        models: [
          {
            name: "image",
            capability: "image",
            pricing: { summary: "$0.02 / image" },
          },
        ],
      },
    ],
  };
}

test("image options show each group's own price and identify unavailable pricing", async () => {
  const onChange = renderPicker(imageConfig());
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "image（Standard）" })),
  );
  const list = screen.getByRole("listbox");
  const image = within(list).getByRole("option", { name: "image" });
  expect(within(image).getByText("$0.04 / image")).toBeTruthy();
  expect(image.getAttribute("aria-selected")).toBe("true");
  expect(within(list).getByText("Price unavailable")).toBeTruthy();

  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Discount" })),
  );
  const discounted = screen.getByRole("option", { name: "image" });
  expect(within(discounted).getByText("$0.02 / image")).toBeTruthy();
  expect(discounted.getAttribute("aria-selected")).toBe("false");
  await act(async () => fireEvent.click(discounted));
  expect(onChange).toHaveBeenCalledWith("discount::image");
  await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
});

test.each([1200, 390])(
  "video details appear on hover and keyboard focus at %dpx without selecting the model",
  async (width) => {
    viewportWidth = width;
    const description =
      "A video model with a long description covering reference images, generated audio and supported camera controls.";
    const config: AiConfig = {
      ...defaultConfig,
      model: "standard::video",
      channels: [
        {
          id: "standard",
          name: "Standard",
          baseUrl: "",
          apiKey: "",
          apiFormat: "openai",
          models: [
            {
              name: "video",
              capability: "video",
              description,
              pricing: {
                summary: "$0.10–0.20 / second",
                details: ["720p: $0.10 / second", "1080p: $0.20 / second"],
              },
            },
            {
              name: "other-video",
              capability: "video",
              description: "Another video model",
              pricing: { summary: "$0.30 / second" },
            },
          ],
        },
      ],
    };
    const onChange = renderPicker(config, vi.fn(), "video");
    const trigger = screen.getByRole("button", { name: "video（Standard）" });
    await act(async () => fireEvent.click(trigger));
    const option = screen.getByRole("option", { name: "video" });
    expect(within(option).getByText("$0.10–0.20 / second")).toBeTruthy();
    expect(screen.queryByText(description)).toBeNull();

    vi.useFakeTimers();
    await act(async () => {
      fireEvent.mouseOver(option);
      await vi.advanceTimersByTimeAsync(250);
    });
    expect(screen.getByText(description)).toBeTruthy();
    expect(screen.getByText("1080p: $0.20 / second")).toBeTruthy();
    expect(onChange).not.toHaveBeenCalled();

    await act(async () => fireEvent.mouseOut(option));
    await act(async () =>
      fireEvent.keyDown(screen.getByRole("textbox"), { key: "ArrowDown" }),
    );
    expect(document.activeElement).toBe(option);
    expect(screen.getByText(description)).toBeTruthy();
    await act(async () => {
      fireEvent.mouseOver(screen.getByRole("option", { name: "other-video" }));
      await vi.advanceTimersByTimeAsync(250);
    });
    expect(screen.queryByText(description)).toBeNull();
    expect(screen.getByText("Another video model")).toBeTruthy();
    await act(async () => fireEvent.keyDown(option, { key: "Escape" }));
    vi.useRealTimers();
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
    expect(document.activeElement).toBe(trigger);
    expect(onChange).not.toHaveBeenCalled();
  },
);

test("an open picker refreshes model prices from host updates and keeps search usable", async () => {
  const config = imageConfig();
  renderPicker(config);
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "image（Standard）" })),
  );
  const updated = structuredClone(config);
  updated.channels[0].models[0].pricing = { summary: "¥0.28 / image" };
  renderPicker(updated);
  expect(
    within(screen.getByRole("option", { name: "image" })).getByText(
      "¥0.28 / image",
    ),
  ).toBeTruthy();
  expect(screen.queryByText("$0.04 / image")).toBeNull();
  await act(async () =>
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "no-such-model" },
    }),
  );
  expect(screen.queryByRole("option")).toBeNull();
  expect(screen.getByRole("status").textContent).toBe("No matching models");
});
