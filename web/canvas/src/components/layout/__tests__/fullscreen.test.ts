// @vitest-environment jsdom
import { fireEvent, screen } from "@testing-library/dom";
import { App, ConfigProvider } from "antd";
import { createInstance } from "i18next";
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, expect, test, vi } from "vitest";

import { CanvasFullscreenButton } from "../canvas-fullscreen-button";

const i18n = createInstance();
await i18n.init({ lng: "en", fallbackLng: false, resources: { en: {} } });

const enterLabel = "Enter fullscreen";
const exitLabel = "Exit fullscreen";
const restoreFullscreenProperties: Array<() => void> = [];
let root: Root;
let container: HTMLDivElement;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  for (const restore of restoreFullscreenProperties.splice(0).reverse()) {
    restore();
  }
  vi.unstubAllGlobals();
});

function stubFullscreenProperty(
  target: Document | HTMLElement,
  key: string,
  descriptor: PropertyDescriptor,
): void {
  const original = Object.getOwnPropertyDescriptor(target, key);
  Object.defineProperty(target, key, { configurable: true, ...descriptor });
  restoreFullscreenProperties.push(() => {
    if (original) Object.defineProperty(target, key, original);
    else Reflect.deleteProperty(target, key);
  });
}

function installFullscreen({ enabled = true, active = false } = {}) {
  const element = document.documentElement;
  let fullscreenElement: Element | null = active ? element : null;
  function changeFullscreen(next: Element | null): void {
    fullscreenElement = next;
    fireEvent(element, new Event("fullscreenchange", { bubbles: true }));
  }

  const requestFullscreen = vi.fn(async () => changeFullscreen(element));
  const exitFullscreen = vi.fn(async () => changeFullscreen(null));
  stubFullscreenProperty(document, "fullscreenEnabled", { value: enabled });
  stubFullscreenProperty(document, "fullscreenElement", {
    get: () => fullscreenElement,
  });
  stubFullscreenProperty(element, "requestFullscreen", {
    value: requestFullscreen,
  });
  stubFullscreenProperty(document, "exitFullscreen", { value: exitFullscreen });

  return { requestFullscreen, exitFullscreen, changeFullscreen };
}

function renderButton(): void {
  act(() => {
    root.render(
      createElement(
        I18nextProvider,
        { i18n },
        createElement(
          ConfigProvider,
          { theme: { token: { motion: false } } },
          createElement(App, null, createElement(CanvasFullscreenButton)),
        ),
      ),
    );
  });
}

test("clicking the fullscreen button enters and exits the current document", async () => {
  const fullscreen = installFullscreen();
  renderButton();

  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: enterLabel })),
  );
  expect(fullscreen.requestFullscreen).toHaveBeenCalledOnce();
  expect(fullscreen.requestFullscreen.mock.contexts[0]).toBe(
    document.documentElement,
  );

  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: exitLabel })),
  );
  expect(fullscreen.exitFullscreen).toHaveBeenCalledOnce();
  expect(fullscreen.exitFullscreen.mock.contexts[0]).toBe(document);
  expect(screen.getByRole("button", { name: enterLabel })).toBeTruthy();
});

test("a browser-initiated exit resets the button without requesting another exit", () => {
  const fullscreen = installFullscreen({ active: true });
  renderButton();
  expect(screen.getByRole("button", { name: exitLabel })).toBeTruthy();

  act(() => fullscreen.changeFullscreen(null));
  expect(screen.getByRole("button", { name: enterLabel })).toBeTruthy();
  expect(fullscreen.exitFullscreen).not.toHaveBeenCalled();
});

test("the fullscreen button is hidden when the document cannot enter fullscreen", () => {
  installFullscreen({ enabled: false });
  renderButton();

  expect(screen.queryByRole("button")).toBeNull();
});

test("a rejected fullscreen request leaves the button in enter mode and shows an error", async () => {
  const fullscreen = installFullscreen();
  const request = Promise.withResolvers<void>();
  fullscreen.requestFullscreen.mockReturnValueOnce(request.promise);
  renderButton();

  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: enterLabel })),
  );
  expect(fullscreen.requestFullscreen).toHaveBeenCalledOnce();
  expect(screen.queryByRole("button", { name: exitLabel })).toBeNull();

  await act(async () => request.reject(new DOMException("Fullscreen denied")));

  expect(screen.getByRole("button", { name: enterLabel })).toBeTruthy();
  expect(screen.queryByRole("button", { name: exitLabel })).toBeNull();
  expect(screen.getByText("Could not switch fullscreen mode")).toBeTruthy();
});
