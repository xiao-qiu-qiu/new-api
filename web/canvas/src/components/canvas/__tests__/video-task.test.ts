// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { fireEvent, screen } from "@testing-library/dom";
import { afterAll, afterEach, expect, test, vi } from "vitest";

import { CanvasNodeType, type CanvasNodeData } from "@/types/canvas";
import { CanvasNode } from "../canvas-node";

vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("@/integration/site-context", () => ({
  getSiteContext: () => ({ theme: "light", userId: 7 }),
}));
vi.mock("@/services/image-storage", () => ({
  previewUrlFor: () => undefined,
  subscribeImagePreviews: () => () => {},
  getImagePreviewRevision: () => 0,
}));

let root: Root | undefined;
vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
afterAll(() => vi.unstubAllGlobals());
afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
});

function renderVideo(metadata: CanvasNodeData["metadata"]) {
  const node: CanvasNodeData = {
    id: "existing-video",
    type: CanvasNodeType.Video,
    title: "Existing video",
    position: { x: 0, y: 0 },
    width: 320,
    height: 180,
    metadata,
  };
  const onRetry = vi.fn();
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() =>
    root?.render(
      createElement(CanvasNode, {
        data: node,
        scale: 1,
        isSelected: false,
        isRelated: false,
        isFocusRelated: false,
        isConnectionTarget: false,
        isConnecting: false,
        showPanel: false,
        showImageInfo: false,
        onMouseDown: vi.fn(),
        onHoverStart: vi.fn(),
        onHoverEnd: vi.fn(),
        onConnectStart: vi.fn(),
        onResizeStart: vi.fn(),
        onResize: vi.fn(),
        onResizeEnd: vi.fn(),
        onContentChange: vi.fn(),
        onTitleChange: vi.fn(),
        onContextMenu: vi.fn(),
        onRetry,
      }),
    ),
  );
  return { node, onRetry };
}

test("a completed video task being retrieved shows retrieval progress", () => {
  renderVideo({
    status: "loading",
    videoTaskId: "old-task",
    videoTaskStatus: "completed",
  });
  expect(
    screen.getByText("Video generated. Retrieving the file..."),
  ).toBeTruthy();
  expect(screen.queryByText("canvas.node.generating")).toBeNull();
});

test.each([
  ["completed", "Retrieve video"],
  ["pending", "Check task status"],
] as const)(
  "a %s video task error offers %s and retries the original node",
  (videoTaskStatus, label) => {
    const { node, onRetry } = renderVideo({
      status: "error",
      videoTaskId: "old-task",
      videoTaskStatus,
      errorDetails: "Network interrupted",
    });
    act(() => {
      fireEvent.click(screen.getByRole("button", { name: label }));
    });
    expect(onRetry).toHaveBeenCalledExactlyOnceWith(node);
    expect(onRetry.mock.calls[0][0]).toBe(node);
  },
);
