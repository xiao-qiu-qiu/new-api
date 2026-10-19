// @vitest-environment jsdom
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { act, createElement, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { fireEvent, getByLabelText, getByRole, getByText, queryByLabelText } from "@testing-library/dom";
import { App } from "antd";
import type { UploadedFile } from "@/services/file-storage";
import type { ReferenceAudio } from "@/types/media";

const fixtures = vi.hoisted(() => ({ upload: vi.fn<(file: Blob, prefix: string) => Promise<UploadedFile>>() }));
vi.mock("@/services/file-storage", () => ({ uploadMediaFile: fixtures.upload }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string, options?: { name?: string }) => key.replace("{{name}}", options?.name || "") }) }));

const { ReferenceAudioInput } = await import("../reference-audio-input");
let container: HTMLDivElement;
let root: Root;

function AudioForm(props: { maxCount?: number; onUploadingChange?: (value: boolean) => void }) {
    const [value, setValue] = useState<ReferenceAudio[]>([]);
    return createElement(App, null, createElement(ReferenceAudioInput, { value, onChange: setValue, maxCount: props.maxCount, onUploadingChange: props.onUploadingChange }));
}

beforeEach(() => {
    Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
    fixtures.upload.mockReset();
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
});
afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
});

test("uploading an audio reference exposes playback and removing it frees the model's reference slot", async () => {
    fixtures.upload.mockResolvedValue({ url: "blob:voice", storageKey: "audio:voice", bytes: 12, mimeType: "audio/wav", durationMs: 1000 });
    await act(async () => root.render(createElement(AudioForm, { maxCount: 1 })));
    const file = new File(["voice"], "voice.wav", { type: "audio/wav" });
    await act(async () => fireEvent.change(getByLabelText(container, "Upload audio"), { target: { files: [file] } }));
    expect(fixtures.upload).toHaveBeenCalledWith(file, "audio");
    const preview = getByLabelText(container, "Preview audio: voice.wav") as HTMLAudioElement;
    expect(preview.src).toBe("blob:voice");
    expect(preview.controls).toBe(true);
    expect((getByRole(container, "button", { name: "Upload audio" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => fireEvent.click(getByRole(container, "button", { name: "Remove audio: voice.wav" })));
    expect(queryByLabelText(container, "Preview audio: voice.wav")).toBeNull();
    expect(getByText(container, "No reference audio")).toBeTruthy();
    expect((getByRole(container, "button", { name: "Upload audio" }) as HTMLButtonElement).disabled).toBe(false);

    const pendingUpload = Promise.withResolvers<UploadedFile>();
    fixtures.upload.mockReturnValueOnce(pendingUpload.promise);
    const onChange = vi.fn();
    await act(async () => root.render(createElement(App, null, createElement(ReferenceAudioInput, { value: [], onChange }))));
    await act(async () => fireEvent.change(getByLabelText(container, "Upload audio"), { target: { files: [file] } }));
    await act(async () => root.render(null));
    await act(async () => pendingUpload.resolve({ url: "blob:old-session", storageKey: "audio:old-session", bytes: 12, mimeType: "audio/wav" }));
    expect(onChange).not.toHaveBeenCalled();
});

test("a model with zero audio references disables uploading and ignores selected audio", async () => {
    await act(async () => root.render(createElement(AudioForm, { maxCount: 0 })));
    const button = getByRole(container, "button", { name: "Upload audio" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.title).toBe("This model does not support reference audio");
    await act(async () => fireEvent.change(getByLabelText(container, "Upload audio"), { target: { files: [new File(["voice"], "voice.wav", { type: "audio/wav" })] } }));
    expect(fixtures.upload).not.toHaveBeenCalled();
    expect(getByText(container, "No reference audio")).toBeTruthy();
});

test("an audio storage failure restores the upload action and reports the failure", async () => {
    const onUploadingChange = vi.fn();
    fixtures.upload.mockRejectedValue(new Error("storage full"));
    await act(async () => root.render(createElement(AudioForm, { onUploadingChange })));
    await act(async () => fireEvent.change(getByLabelText(container, "Upload audio"), { target: { files: [new File(["voice"], "voice.wav", { type: "audio/wav" })] } }));
    expect(getByText(document.body, "Failed to upload audio")).toBeTruthy();
    expect(getByText(container, "No reference audio")).toBeTruthy();
    expect((getByRole(container, "button", { name: "Upload audio" }) as HTMLButtonElement).disabled).toBe(false);
    expect(onUploadingChange.mock.calls).toEqual([[true], [false]]);
});
