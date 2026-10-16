import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Button, Input, Popover } from "antd";
import { Check, ChevronsUpDown, Cpu, Search } from "lucide-react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import {
  decodeChannelModel,
  modelOptionLabel,
  modelOptionName,
  selectableModelsByCapability,
  type AiConfig,
  type ModelCapability,
} from "@/stores/use-config-store";

type ModelPickerProps = {
  config: AiConfig;
  value?: string;
  onChange: (model: string) => void;
  capability?: ModelCapability;
  className?: string;
  fullWidth?: boolean;
  placeholder?: string;
  onMissingConfig?: () => void;
  disabled?: boolean;
};

/** Shared by workbenches, canvas nodes and the online Agent composer. */
export function ModelPicker(props: ModelPickerProps) {
  const { t } = useTranslation();
  const pickerId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [group, setGroup] = useState("");
  const [search, setSearch] = useState("");
  const options = useMemo(
    () => [...new Set(selectableModelsByCapability(props.config, props.capability))],
    [props.config, props.capability],
  );
  const groups = useMemo(() => {
    const available = new Set(options.map((value) => decodeChannelModel(value)?.channelId || ""));
    return props.config.channels.filter((channel) => available.has(channel.id));
  }, [props.config.channels, options]);
  const current = props.value || "";
  const currentGroup = decodeChannelModel(current)?.channelId || "";
  const activeGroup = groups.find((item) => item.id === group)?.id || groups.find((item) => item.id === currentGroup)?.id || groups[0]?.id || "";
  const currentGroupName = props.config.channels.find((item) => item.id === currentGroup)?.name;
  const query = search.trim().toLowerCase();
  const visible = options.filter((value) => (decodeChannelModel(value)?.channelId || "") === activeGroup && modelOptionName(value).toLowerCase().includes(query));
  const placeholder = props.placeholder || t("settingsPanels.model.select");

  useEffect(() => {
    const closeOtherPicker = (event: Event) => {
      if ((event as CustomEvent<string>).detail !== pickerId) setOpen(false);
    };
    window.addEventListener("model-picker-open", closeOtherPicker);
    return () => window.removeEventListener("model-picker-open", closeOtherPicker);
  }, [pickerId]);

  const close = () => {
    setOpen(false);
    triggerRef.current?.focus();
  };
  const navigateOptions = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      close();
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    const target = event.target as HTMLElement;
    if (target.tagName === "INPUT" && event.key !== "ArrowDown") return;
    const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="option"]')];
    if (!buttons.length) return;
    event.preventDefault();
    event.stopPropagation();
    const index = buttons.indexOf(target.closest("button") as HTMLButtonElement);
    let next = index + (event.key === "ArrowUp" ? -1 : 1);
    if (event.key === "Home") next = 0;
    if (event.key === "End") next = buttons.length - 1;
    buttons[(next + buttons.length) % buttons.length]?.focus();
  };

  return (
    <Popover
      open={open && !props.disabled}
      onOpenChange={(nextOpen) => {
        if (props.disabled) return;
        if (nextOpen) {
          if (!options.length) props.onMissingConfig?.();
          setGroup(currentGroup);
          setSearch("");
          window.dispatchEvent(new CustomEvent("model-picker-open", { detail: pickerId }));
        }
        setOpen(nextOpen);
      }}
      trigger="click"
      placement="bottomLeft"
      arrow={false}
      destroyOnHidden
      zIndex={1300}
      styles={{ container: { padding: 0, borderRadius: 18, overflow: "hidden" } }}
      content={
        <div id={pickerId} role="dialog" aria-label={placeholder} className="canvas-model-menu flex w-[540px] max-w-[calc(100vw-24px)] overflow-hidden rounded-[18px] border border-border bg-popover text-popover-foreground" data-canvas-no-zoom data-canvas-shortcuts-ignore onPointerDown={(event) => event.stopPropagation()} onMouseDown={(event) => event.stopPropagation()} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => { if (event.key === "Escape") { event.stopPropagation(); close(); } }}>
          <div className="flex w-[30%] min-w-24 shrink-0 flex-col border-r border-border/60 p-2">
            <span className="px-2 pb-2 pt-1 text-xs font-medium text-muted-foreground">{t("Model groups")}</span>
            <div className="min-h-0 flex-1 space-y-1 overflow-y-auto" aria-label={t("Model groups")}>
              {groups.map((item) => (
                <Button key={item.id} type="text" block className={cn("!h-auto !min-h-9 !justify-start !rounded-xl !px-2 !py-2 !text-left", item.id === activeGroup && "!bg-accent !text-accent-foreground")} aria-pressed={item.id === activeGroup} title={item.name} onClick={() => setGroup(item.id)}>
                  <span className="min-w-0 flex-1 break-words whitespace-normal">{item.name}</span>
                  {item.id === currentGroup && <Check className="size-3.5 shrink-0" />}
                </Button>
              ))}
            </div>
          </div>
          <div className="flex min-w-0 flex-1 flex-col p-2" onKeyDown={navigateOptions}>
            <Input autoFocus allowClear prefix={<Search className="size-4 text-muted-foreground" />} value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t("Search models...")} aria-label={t("Search models...")} className="!mb-2 !rounded-xl" />
            <div role="listbox" aria-label={placeholder} className="min-h-0 flex-1 space-y-1 overflow-y-auto overscroll-contain">
              {visible.map((value) => (
                <Button key={value} role="option" aria-selected={current === value} type="text" block className={cn("!h-auto !min-h-10 !justify-start !rounded-xl !px-2 !py-2 !text-left", current === value && "!bg-accent !font-medium !text-accent-foreground")} title={modelOptionName(value)} onClick={() => { props.onChange(value); close(); }}>
                  <ModelIcon model={value} />
                  <span className="min-w-0 flex-1 break-words whitespace-normal">{modelOptionName(value)}</span>
                  {current === value && <Check className="size-4 shrink-0" />}
                </Button>
              ))}
              {!visible.length && <p role="status" className="px-3 py-6 text-center text-sm text-muted-foreground">{t("No matching models")}</p>}
            </div>
          </div>
        </div>
      }
    >
      <Button
        ref={triggerRef}
        disabled={props.disabled}
        className={cn("canvas-composer-model-picker !inline-flex !h-8 !max-w-full !gap-2 !rounded-full !border-input !bg-transparent !px-3 !text-sm !font-normal !text-foreground !shadow-sm", props.fullWidth ? "!w-full !min-w-0 !justify-start" : "!w-fit !min-w-0 !justify-start", props.className)}
        onMouseDown={(event) => event.stopPropagation()}
        onPointerDown={(event) => event.stopPropagation()}
        aria-label={current ? modelOptionLabel(props.config, current) : placeholder}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? pickerId : undefined}
        title={current ? modelOptionLabel(props.config, current) : placeholder}
      >
        <ModelIcon model={current} />
        <span className="canvas-model-picker-text min-w-0 flex-1 truncate text-left">{current ? modelOptionName(current) : placeholder}</span>
        {currentGroupName && <span className="canvas-model-picker-group max-w-[7rem] truncate rounded-md bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">{currentGroupName}</span>}
        <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
      </Button>
    </Popover>
  );
}

function ModelIcon({ model }: { model: string }) {
  const icon = resolveModelIcon(modelOptionName(model));
  return icon ? (
    <img src={icon} alt="" className="size-4 shrink-0 dark:invert" />
  ) : (
    <Cpu className="size-4 shrink-0 opacity-70" />
  );
}

function resolveModelIcon(model: string) {
  const name = model.toLowerCase();
  if (name.includes("claude") || name.includes("anthropic")) {
    return "/canvas-app/icons/claude.svg";
  }
  if (name.includes("gemini") || name.includes("google")) {
    return "/canvas-app/icons/gemini.svg";
  }
  if (name.includes("gpt") || name.includes("openai")) {
    return "/canvas-app/icons/openai.svg";
  }
  if (name.includes("grok")) return "/canvas-app/icons/grok.svg";
  if (name.includes("deepseek")) return "/canvas-app/icons/deepseek.svg";
  if (name.includes("glm")) return "/canvas-app/icons/glm.svg";
  return "";
}
