import { Button, Tooltip } from "antd";
import { Keyboard, Puzzle, Settings2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { GitHubLink } from "@/components/layout/github-link";
import { useConfigStore } from "@/stores/use-config-store";
type UserStatusActionsProps = {
    showConfig?: boolean;
    variant?: "default" | "canvas";
    onOpenShortcuts?: () => void;
    onOpenPlugins?: () => void;
};
export function UserStatusActions(props: UserStatusActionsProps) {
    const { t } = useTranslation();
    const openConfigDialog = useConfigStore((state) => state.openConfigDialog);
    return <div className="inline-flex shrink-0 items-center gap-1">
        {props.onOpenPlugins && <Tooltip title={t("topNav.plugins")}><Button type="text" icon={<Puzzle size={16} />} onClick={props.onOpenPlugins} aria-label={t("topNav.plugins")} /></Tooltip>}
        {props.showConfig !== false && <Tooltip title={t("navigation.config")}><Button type="text" icon={<Settings2 size={16} />} onClick={() => openConfigDialog(false, "preferences")} aria-label={t("navigation.config")} /></Tooltip>}
        {props.onOpenShortcuts && <Tooltip title={t("topNav.shortcuts")}><Button type="text" icon={<Keyboard size={16} />} onClick={props.onOpenShortcuts} aria-label={t("topNav.shortcuts")} /></Tooltip>}
        <GitHubLink className="size-7" />
    </div>;
}
