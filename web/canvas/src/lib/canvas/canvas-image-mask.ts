import i18n from "@/i18n";

/** Brush alpha marks the edit region; OpenAI needs that region transparent. */
export function selectionToAlphaMask(pixels: Uint8ClampedArray): void {
    for (let offset = 0; offset < pixels.length; offset += 4) {
        pixels[offset] = 0;
        pixels[offset + 1] = 0;
        pixels[offset + 2] = 0;
        pixels[offset + 3] = 255 - pixels[offset + 3];
    }
}

/** Wanx uses opaque white for edits and black for the preserved region. */
export function alphaMaskToWanxMask(pixels: Uint8ClampedArray): void {
    for (let offset = 0; offset < pixels.length; offset += 4) {
        const value = 255 - pixels[offset + 3];
        pixels[offset] = value;
        pixels[offset + 1] = value;
        pixels[offset + 2] = value;
        pixels[offset + 3] = 255;
    }
}

export function createImageEditMask(selection: HTMLCanvasElement): string {
    const selectionContext = selection.getContext("2d", { willReadFrequently: true });
    const canvas = document.createElement("canvas");
    canvas.width = selection.width;
    canvas.height = selection.height;
    const context = canvas.getContext("2d");
    if (!context || !selectionContext) throw new Error(i18n.t("common.imageReadFailed"));
    const pixels = selectionContext.getImageData(0, 0, selection.width, selection.height);
    selectionToAlphaMask(pixels.data);
    context.putImageData(pixels, 0, 0);
    return canvas.toDataURL("image/png");
}

export async function encodeWanxImageEditMask(dataUrl: string, signal?: AbortSignal): Promise<string> {
    signal?.throwIfAborted();
    const image = await new Promise<HTMLImageElement>((resolve, reject) => {
        const element = new Image();
        const cleanup = () => {
            element.removeEventListener("load", load);
            element.removeEventListener("error", error);
            signal?.removeEventListener("abort", abort);
        };
        const abort = () => {
            cleanup();
            reject(signal?.reason || new DOMException("Aborted", "AbortError"));
        };
        const load = () => {
            cleanup();
            resolve(element);
        };
        const error = () => {
            cleanup();
            reject(new Error(i18n.t("common.imageReadFailed")));
        };
        element.addEventListener("load", load, { once: true });
        element.addEventListener("error", error, { once: true });
        signal?.addEventListener("abort", abort, { once: true });
        element.src = dataUrl;
    });
    signal?.throwIfAborted();
    const canvas = document.createElement("canvas");
    canvas.width = image.naturalWidth;
    canvas.height = image.naturalHeight;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    if (!context) throw new Error(i18n.t("common.imageReadFailed"));
    context.drawImage(image, 0, 0);
    const pixels = context.getImageData(0, 0, canvas.width, canvas.height);
    alphaMaskToWanxMask(pixels.data);
    context.putImageData(pixels, 0, 0);
    return canvas.toDataURL("image/png");
}
