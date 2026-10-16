import { useRef, useState, type ReactNode } from "react";
import { Button, Dropdown, Tooltip } from "antd";
import { ArrowUp, Check, ChevronUp, Cpu, Gauge, Hand, ImagePlus, LoaderCircle, RefreshCw, ShieldAlert, ShieldCheck, ShieldOff, Square, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Select, SelectContent, SelectItem, SelectTrigger } from "@/components/ui/select";
import type { canvasThemes } from "@/lib/canvas-theme";
import { useAgentStore, type AgentModel, type AgentPermissionMode, type AgentReasoningEffort } from "@/stores/use-agent-store";
import type { AgentChatAttachment } from "./agent-chat-message";
import { AgentChatPromptInput } from "./agent-chat-prompt-input";

export function AgentChatComposer({
    prompt,
    attachments = [],
    disabled,
    sending,
    placeholder,
    theme,
    onPromptChange,
    onSubmit,
    onStop,
    onAddFiles,
    onRemoveAttachment,
    confirmTools,
    onConfirmToolsChange,
    permissionMode,
    onPermissionModeChange,
    models,
    model,
    reasoningEffort,
    onModelChange,
    onReasoningEffortChange,
    left,
    skillsEnabled,
    modelControls,
}: {
    prompt: string;
    attachments?: AgentChatAttachment[];
    disabled?: boolean;
    sending?: boolean;
    placeholder: string;
    theme: (typeof canvasThemes)[keyof typeof canvasThemes];
    onPromptChange: (value: string) => void;
    onSubmit: () => void;
    onStop?: () => void;
    onAddFiles?: (files: FileList | File[] | null) => void | Promise<void>;
    onRemoveAttachment?: (id: string) => void;
    confirmTools?: boolean;
    onConfirmToolsChange?: (confirmTools: boolean) => void;
    permissionMode?: AgentPermissionMode;
    onPermissionModeChange?: (permissionMode: AgentPermissionMode) => void;
    models?: AgentModel[];
    model?: string;
    reasoningEffort?: AgentReasoningEffort | "";
    onModelChange?: (model: string) => void;
    onReasoningEffortChange?: (effort: AgentReasoningEffort | "") => void;
    left?: ReactNode;
    skillsEnabled?: boolean;
    modelControls?: ReactNode;
}) {
    const { t } = useTranslation();
    const fileInputRef = useRef<HTMLInputElement>(null);
    const canvasReferences = useAgentStore((state) => state.canvasReferences);
    const canSubmit = !disabled && !sending && Boolean(prompt.trim() || attachments.length || canvasReferences.length);
    return (
        <div className="px-2 pb-2 pt-2" onWheelCapture={(event) => event.stopPropagation()}>
            <div className="rounded-[24px] border px-3 pb-3 pt-3 shadow-lg" style={{ background: theme.toolbar.panel, borderColor: theme.node.stroke }}>
                {attachments.length ? (
                    <div className="thin-scrollbar mb-2 flex gap-2 overflow-x-auto pb-1">
                        {attachments.map((item) => (
                            <div key={item.id} className="group relative size-14 shrink-0 overflow-hidden rounded-xl border" style={{ borderColor: theme.node.stroke }} title={item.name}>
                                <img src={item.url} alt={item.name} className="size-full object-cover" />
                                {onRemoveAttachment ? (
                                    <button type="button" className="absolute right-1 top-1 grid size-5 place-items-center rounded-full border opacity-0 shadow-sm transition group-hover:opacity-100" style={{ background: theme.toolbar.panel, borderColor: theme.node.stroke, color: theme.node.text }} onClick={() => onRemoveAttachment(item.id)} aria-label={t("agent.composer.removeImage")}>
                                        <X className="size-3" />
                                    </button>
                                ) : null}
                            </div>
                        ))}
                    </div>
                ) : null}
                <AgentChatPromptInput value={prompt} disabled={disabled || sending} placeholder={placeholder} theme={theme} onChange={onPromptChange} onSubmit={() => { if (canSubmit) void onSubmit(); }} onAddFiles={onAddFiles} skillsEnabled={skillsEnabled} />
                {modelControls || (models?.length && model && onModelChange && onReasoningEffortChange) ? (
                    <div className="mt-2 min-w-0" data-agent-model-controls>
                        {modelControls || <AgentModelControls models={models || []} model={model || ""} reasoningEffort={reasoningEffort || ""} disabled={sending} onModelChange={(value) => onModelChange?.(value)} onReasoningEffortChange={(value) => onReasoningEffortChange?.(value)} />}
                    </div>
                ) : null}
                <div className="@container mt-2 flex items-center justify-between gap-2">
                    <div className="flex min-w-0 items-center gap-1">
                        {onAddFiles ? (
                            <>
                                <input ref={fileInputRef} hidden type="file" accept="image/*" multiple onChange={(event) => {
                                    void onAddFiles(event.target.files);
                                    event.target.value = "";
                                }} />
                                <Tooltip title={t("agent.composer.uploadImage")}>
                                    <Button type="text" shape="circle" className="!h-9 !w-9 !min-w-9" disabled={disabled || sending} style={{ color: theme.node.muted }} icon={<ImagePlus className="size-4" />} onClick={() => fileInputRef.current?.click()} aria-label={t("agent.composer.uploadImage")} />
                                </Tooltip>
                            </>
                        ) : null}
                        {onConfirmToolsChange ? <ToolConfirmationMenu confirmTools={Boolean(confirmTools)} theme={theme} onChange={onConfirmToolsChange} /> : null}
                        {permissionMode && onPermissionModeChange ? <PermissionModeMenu permissionMode={permissionMode} theme={theme} onChange={onPermissionModeChange} /> : null}
                        {left}
                    </div>
                    <div className="flex shrink-0 items-center gap-1.5">
                        {sending && onStop ? (
                            <Tooltip title={t("agent.composer.stop")} placement="top"><Button danger shape="circle" className="!h-10 !w-10 !min-w-10" icon={<Square className="size-4" />} onClick={() => void onStop()} aria-label={t("agent.composer.stop")} /></Tooltip>
                        ) : (
                            <Tooltip title={t("agent.composer.send")} placement="top"><Button type="primary" shape="circle" className="!h-10 !w-10 !min-w-10" disabled={!canSubmit} icon={sending ? <LoaderCircle className="size-4 animate-spin" /> : <ArrowUp className="size-4" />} onClick={() => void onSubmit()} aria-label={t("agent.composer.send")} /></Tooltip>
                        )}
                    </div>
                </div>
            </div>
        </div>
    );
}

function AgentModelControls(props: { models: AgentModel[]; model: string; reasoningEffort: AgentReasoningEffort | ""; disabled?: boolean; onModelChange: (model: string) => void; onReasoningEffortChange: (effort: AgentReasoningEffort | "") => void }) {
    const { t } = useTranslation();
    const current = props.models.find((item) => item.model === props.model) || props.models[0];
    if (!current) return null;
    return (
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <Select value={props.model} disabled={props.disabled} onValueChange={props.onModelChange}>
                <SelectTrigger hideChevron className="h-8 min-w-0 flex-1 basis-36 gap-1.5 rounded-full border-0 bg-transparent px-2 text-xs font-medium shadow-none hover:bg-black/5 dark:bg-transparent dark:hover:bg-white/10" aria-label={t("agent.composer.selectModel", { model: current.displayName || current.model })}>
                    <Cpu className="size-3.5 shrink-0 opacity-70" />
                    <span className="min-w-0 flex-1 truncate text-left" title={current.displayName || current.model}>{current.displayName || current.model}</span>
                    <ChevronUp className="size-3 shrink-0 opacity-50" />
                </SelectTrigger>
                <SelectContent data-canvas-no-zoom position="popper" side="top" align="start" sideOffset={6} className="z-[1200] w-64 rounded-xl border border-border/70 bg-popover p-1 shadow-xl">
                    {props.models.map((item) => <SelectItem key={item.model} value={item.model}>{item.displayName || item.model}</SelectItem>)}
                </SelectContent>
            </Select>
            <AgentReasoningControl value={props.reasoningEffort} disabled={props.disabled} efforts={current.supportedReasoningEfforts.map((item) => item.reasoningEffort)} onChange={props.onReasoningEffortChange} />
        </div>
    );
}

const SITE_REASONING_EFFORTS: AgentReasoningEffort[] = ["none", "minimal", "low", "medium", "high", "xhigh"];

export function AgentReasoningControl(props: { value: AgentReasoningEffort | ""; disabled?: boolean; efforts?: AgentReasoningEffort[]; allowAuto?: boolean; onChange: (effort: AgentReasoningEffort | "") => void }) {
    const { t } = useTranslation();
    const label = props.value ? t(`agent.composer.effort.${props.value}`) : t("agent.composer.effort.auto", { defaultValue: "自动" });
    return (
        <Select value={props.value || "auto"} disabled={props.disabled} onValueChange={(value) => props.onChange(value === "auto" ? "" : value as AgentReasoningEffort)}>
            <SelectTrigger hideChevron className="h-8 w-auto shrink-0 gap-1.5 rounded-full border-0 bg-transparent px-2 text-xs font-medium shadow-none hover:bg-black/5 dark:bg-transparent dark:hover:bg-white/10" aria-label={t("agent.composer.selectReasoning", { effort: label })} title={t("agent.composer.reasoning", { effort: label })}>
                <Gauge className="size-3.5 shrink-0 opacity-70" />
                <span className="whitespace-nowrap">{label}</span>
                <ChevronUp className="size-3 shrink-0 opacity-50" />
            </SelectTrigger>
            <SelectContent data-canvas-no-zoom position="popper" side="top" align="end" sideOffset={6} className="z-[1200] min-w-32 rounded-xl border border-border/70 bg-popover p-1 shadow-xl">
                {props.allowAuto ? <SelectItem value="auto">{t("agent.composer.effort.auto", { defaultValue: "自动" })}</SelectItem> : null}
                {(props.efforts || SITE_REASONING_EFFORTS).map((effort) => <SelectItem key={effort} value={effort}>{t(`agent.composer.effort.${effort}`, { defaultValue: effort })}</SelectItem>)}
            </SelectContent>
        </Select>
    );
}

function PermissionModeMenu({ permissionMode, theme, onChange }: { permissionMode: AgentPermissionMode; theme: (typeof canvasThemes)[keyof typeof canvasThemes]; onChange: (permissionMode: AgentPermissionMode) => void }) {
    const { t } = useTranslation();
    const permissionOptions: Array<{ key: AgentPermissionMode; title: string; shortTitle: string; description: string; icon: ReactNode }> = [
        { key: "request", title: t("agent.composer.permission.request"), shortTitle: t("agent.composer.permission.request"), description: t("agent.composer.permission.requestDescription"), icon: <ShieldAlert className="size-3.5" /> },
        { key: "automatic", title: t("agent.composer.permission.automatic"), shortTitle: t("agent.composer.permission.automatic"), description: t("agent.composer.permission.automaticDescription"), icon: <ShieldCheck className="size-3.5" /> },
        { key: "full", title: t("agent.composer.permission.full"), shortTitle: t("agent.composer.permission.fullShort"), description: t("agent.composer.permission.fullDescription"), icon: <ShieldOff className="size-3.5" /> },
    ];
    const current = permissionOptions.find((item) => item.key === permissionMode) || permissionOptions[0];
    const [open, setOpen] = useState(false);
    return (
        <Tooltip title={t("agent.composer.permissionLabel", { mode: current.shortTitle })} placement="top" open={open ? false : undefined}>
            <span className="inline-flex shrink-0">
                <Dropdown
                    trigger={["click"]}
                    placement="topLeft"
                    open={open}
                    onOpenChange={setOpen}
                    menu={{
                        items: permissionOptions.map((item) => ({
                            key: item.key,
                            label: <ConfirmationOption icon={item.icon} title={item.title} description={item.description} selected={permissionMode === item.key} />,
                            onClick: () => onChange(item.key),
                        })),
                    }}
                >
                    <button type="button" className="flex h-9 w-9 min-w-9 shrink-0 items-center justify-center gap-0 rounded-full px-0 text-xs font-medium transition hover:bg-black/5 @min-[660px]:h-9 @min-[660px]:w-auto @min-[660px]:min-w-0 @min-[660px]:justify-start @min-[660px]:gap-1.5 @min-[660px]:px-2.5 dark:hover:bg-white/10" style={{ color: permissionMode === "full" ? "#ea580c" : theme.node.text }} aria-label={t("agent.composer.selectPermission", { mode: current.title })}>
                        {current.icon}
                        <span className="hidden @min-[660px]:inline">{current.shortTitle}</span>
                        <ChevronUp className="hidden size-3 opacity-50 @min-[660px]:block" />
                    </button>
                </Dropdown>
            </span>
        </Tooltip>
    );
}

function ToolConfirmationMenu({ confirmTools, theme, onChange }: { confirmTools: boolean; theme: (typeof canvasThemes)[keyof typeof canvasThemes]; onChange: (confirmTools: boolean) => void }) {
    const { t } = useTranslation();
    const [open, setOpen] = useState(false);
    const mode = t(confirmTools ? "agent.composer.tools.manual" : "agent.composer.tools.automatic");
    return (
        <Tooltip title={t("agent.composer.tools.label", { mode })} placement="top" open={open ? false : undefined}>
            <span className="inline-flex shrink-0">
                <Dropdown
                    trigger={["click"]}
                    placement="topLeft"
                    open={open}
                    onOpenChange={setOpen}
                    menu={{
                        items: [
                            {
                                key: "manual",
                                label: <ConfirmationOption icon={<Hand className="size-4" />} title={t("agent.composer.tools.manual")} description={t("agent.composer.tools.manualDescription")} selected={confirmTools} />,
                                onClick: () => onChange(true),
                            },
                            {
                                key: "automatic",
                                label: <ConfirmationOption icon={<RefreshCw className="size-4" />} title={t("agent.composer.tools.automatic")} description={t("agent.composer.tools.automaticDescription")} selected={!confirmTools} />,
                                onClick: () => onChange(false),
                            },
                        ],
                    }}
                >
                    <button type="button" className="flex h-9 w-9 min-w-9 shrink-0 items-center justify-center gap-0 rounded-full px-0 text-xs font-medium transition hover:bg-black/5 @min-[660px]:w-auto @min-[660px]:min-w-0 @min-[660px]:justify-start @min-[660px]:gap-1.5 @min-[660px]:px-2.5 dark:hover:bg-white/10" style={{ color: theme.node.text }} aria-label={t("agent.composer.tools.select", { mode })}>
                        {confirmTools ? <Hand className="size-3.5" /> : <RefreshCw className="size-3.5" />}
                        <span className="hidden @min-[660px]:inline">{mode}</span>
                        <ChevronUp className="hidden size-3 opacity-50 @min-[660px]:block" />
                    </button>
                </Dropdown>
            </span>
        </Tooltip>
    );
}

function ConfirmationOption({ icon, title, description, selected }: { icon: ReactNode; title: string; description: string; selected: boolean }) {
    return (
        <div className="flex min-w-64 items-start gap-3 py-1">
            <span className="mt-0.5 shrink-0">{icon}</span>
            <span className="min-w-0 flex-1">
                <span className="block text-sm font-medium">{title}</span>
                <span className="mt-0.5 block text-xs leading-5 opacity-60">{description}</span>
            </span>
            {selected ? <Check className="mt-0.5 size-4 shrink-0" /> : null}
        </div>
    );
}
