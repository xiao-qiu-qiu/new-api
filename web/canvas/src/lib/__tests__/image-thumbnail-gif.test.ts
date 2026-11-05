import { afterEach, expect, test, vi } from "vitest";

import { createImageThumbnail } from "../image-thumbnail";

const originalBitmap = Object.getOwnPropertyDescriptor(
  globalThis,
  "createImageBitmap",
);

afterEach(() => {
  if (originalBitmap) {
    Object.defineProperty(globalThis, "createImageBitmap", originalBitmap);
  } else Reflect.deleteProperty(globalThis, "createImageBitmap");
});

test("GIF thumbnail creation skips bitmap decoding to preserve animation", async () => {
  const createBitmap = vi.fn(() => {
    throw new Error("GIF animation must not be flattened into a bitmap");
  });
  Object.defineProperty(globalThis, "createImageBitmap", {
    configurable: true,
    value: createBitmap,
  });
  const blob = new Blob(
    [
      Uint8Array.from(
        atob("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"),
        (char) => char.charCodeAt(0),
      ),
    ],
    { type: "image/gif" },
  );

  expect(await createImageThumbnail(blob)).toBeUndefined();
  expect(createBitmap).not.toHaveBeenCalled();
});
