import { useEffect, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { App, Button } from "antd";
import { Music2, Trash2, Upload } from "lucide-react";
import { nanoid } from "nanoid";
import { useTranslation } from "react-i18next";

import { uploadMediaFile } from "@/services/file-storage";
import type { ReferenceAudio } from "@/types/media";

type ReferenceAudioInputProps = {
    value: ReferenceAudio[];
    onChange: Dispatch<SetStateAction<ReferenceAudio[]>>;
    onUploadingChange?: (uploading: boolean) => void;
    maxCount?: number;
};

export function ReferenceAudioInput(props: ReferenceAudioInputProps) {
    const { t } = useTranslation();
    const { message } = App.useApp();
    const inputRef = useRef<HTMLInputElement>(null);
    const mountedRef = useRef(true);
    const [uploading, setUploading] = useState(false);
    const full = props.maxCount !== undefined && props.value.length >= props.maxCount;
    useEffect(() => {
        mountedRef.current = true;
        return () => { mountedRef.current = false; };
    }, []);

    const addAudio = async (files: File[]) => {
        if (uploading || full) return;
        const audioFiles = files.filter((file) => file.type.startsWith("audio/"));
        if (audioFiles.length !== files.length) message.warning(t("Only audio files are supported"));
        const capacity = props.maxCount === undefined ? audioFiles.length : Math.max(0, props.maxCount - props.value.length);
        if (!audioFiles.length || !capacity) return;
        setUploading(true);
        props.onUploadingChange?.(true);
        try {
            const audios = await Promise.all(audioFiles.slice(0, capacity).map(async (file) => {
                const stored = await uploadMediaFile(file, "audio");
                return { id: nanoid(), name: file.name, type: stored.mimeType, url: stored.url, storageKey: stored.storageKey, durationMs: stored.durationMs };
            }));
            if (mountedRef.current) props.onChange((value) => [...value, ...audios].slice(0, props.maxCount));
        } catch {
            if (mountedRef.current) message.error(t("Failed to upload audio"));
        } finally {
            if (mountedRef.current) {
                setUploading(false);
                props.onUploadingChange?.(false);
            }
        }
    };

    return (
        <div className="min-w-0">
            <div className="mb-2 flex items-center justify-between gap-3">
                <span className="text-base font-semibold">{t("canvas.configNode.audioReferences")}</span>
                <Button size="small" icon={<Upload className="size-3.5" />} loading={uploading} disabled={uploading || full} title={props.maxCount === 0 ? t("This model does not support reference audio") : undefined} onClick={() => inputRef.current?.click()}>
                    {t("Upload audio")}
                </Button>
            </div>
            <div className="space-y-2 rounded-lg border border-dashed border-stone-300 p-2 dark:border-stone-700" onDragOver={(event) => event.preventDefault()} onDrop={(event) => {
                event.preventDefault();
                void addAudio([...event.dataTransfer.files]);
            }}>
                {props.value.map((item) => (
                    <div key={item.id} className="min-w-0 rounded-md border border-stone-200 bg-stone-50 p-2 dark:border-stone-800 dark:bg-stone-900">
                        <div className="mb-2 flex min-w-0 items-center gap-2">
                            <Music2 className="size-4 shrink-0 text-stone-500" />
                            <span className="min-w-0 flex-1 truncate text-xs" title={item.name}>{item.name}</span>
                            <Button size="small" type="text" icon={<Trash2 className="size-3.5" />} aria-label={`${t("Remove audio")}: ${item.name}`} onClick={() => props.onChange((value) => value.filter((audio) => audio.id !== item.id))} />
                        </div>
                        <audio src={item.url} controls preload="metadata" aria-label={t("Preview audio: {{name}}", { name: item.name })} className="h-10 w-full min-w-0" />
                    </div>
                ))}
                {!props.value.length ? <div className="flex min-h-16 items-center justify-center text-sm text-stone-500">{t("No reference audio")}</div> : null}
            </div>
            <input ref={inputRef} type="file" accept="audio/*" multiple className="hidden" aria-label={t("Upload audio")} disabled={uploading || full} onChange={(event) => {
                void addAudio([...event.target.files || []]);
                event.target.value = "";
            }} />
        </div>
    );
}
