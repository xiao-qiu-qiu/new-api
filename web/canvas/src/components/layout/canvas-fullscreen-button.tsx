import { App, Button, Tooltip } from "antd";
import { Maximize, Minimize } from "lucide-react";
import { useSyncExternalStore } from "react";
import { useTranslation } from "react-i18next";

function subscribeFullscreen(onChange: () => void) {
  document.addEventListener("fullscreenchange", onChange);
  return () => document.removeEventListener("fullscreenchange", onChange);
}

function getFullscreenState() {
  if (document.fullscreenElement === document.documentElement) return "active";
  return document.fullscreenEnabled ? "available" : "unavailable";
}

export function CanvasFullscreenButton() {
  const state = useSyncExternalStore(subscribeFullscreen, getFullscreenState);
  const { t } = useTranslation();
  const { message } = App.useApp();
  if (state === "unavailable") return null;

  const active = state === "active";
  const label = active ? t("Exit fullscreen") : t("Enter fullscreen");
  const Icon = active ? Minimize : Maximize;
  const toggleFullscreen = async () => {
    try {
      if (active) await document.exitFullscreen();
      else await document.documentElement.requestFullscreen();
    } catch {
      void message.error(t("Could not switch fullscreen mode"));
    }
  };

  return (
    <Tooltip title={label}>
      <Button
        type="text"
        shape="circle"
        icon={<Icon className="size-4" aria-hidden="true" />}
        aria-label={label}
        onClick={toggleFullscreen}
      />
    </Tooltip>
  );
}
