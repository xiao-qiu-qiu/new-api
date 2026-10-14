import { useEffect } from "react";
import { Button, Segmented, Tooltip } from "antd";
import { Settings2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { canvasThemes } from "@/lib/canvas-theme";
import { useAgentStore, type AgentRoute } from "@/stores/use-agent-store";
import { useThemeStore } from "@/stores/use-theme-store";
import { LocalCodexAgentPanel } from "./local-codex-agent-panel";
import { SiteAgentPanel } from "./site-agent-panel";

/** Only the selected route mounts its transport and conversation lifecycle. */
export function LocalAgentPanel(props: { embedded?: boolean; headless?: boolean; autoConnect?: boolean }) {
    const { t } = useTranslation();
    const route = useAgentStore((state) => state.route);
    const setAgentRoute = useAgentStore((state) => state.setAgentRoute);
    const theme = canvasThemes[useThemeStore((state) => state.theme)];

    useEffect(() => {
        const connect = (url: string, token: string) => {
            const state = useAgentStore.getState();
            state.setAgentRoute("local");
            useAgentStore.getState().setAgentState({ url, token, activeTab: "setup" });
            useAgentStore.getState().connectAgent({ silent: true });
            useAgentStore.getState().openPanel();
        };
        const receive = (event: MessageEvent) => {
            if (event.source !== window.parent || event.origin !== window.location.origin || event.data?.type !== "new-api:canvas-agent-connect") return;
            if (typeof event.data.url === "string" && typeof event.data.token === "string") connect(event.data.url, event.data.token);
        };
        window.addEventListener("message", receive);
        return () => window.removeEventListener("message", receive);
    }, [setAgentRoute]);

    return (
        <div className="flex h-full min-h-0 flex-col" style={{ color: theme.node.text }} data-canvas-shortcuts-ignore>
            {!props.headless ? <div className="flex shrink-0 items-center gap-2 border-b px-2 py-1.5" style={{ borderColor: theme.node.stroke }}>
                <Segmented block className="min-w-0 flex-1" value={route} onChange={(value) => setAgentRoute(value as AgentRoute)} options={[
                    { value: "site", label: t("agent.route.site", { defaultValue: "站内在线" }) },
                    { value: "local", label: t("agent.route.local", { defaultValue: "本地 Codex" }) },
                ]} aria-label={t("agent.route.select", { defaultValue: "Agent 接入方式" })} />
                <Tooltip title={t("agent.route.localSettings", { defaultValue: "本地连接配置" })}>
                    <Button type="text" shape="circle" className="!h-8 !w-8 !min-w-8" aria-label={t("agent.route.localSettings", { defaultValue: "本地连接配置" })} icon={<Settings2 className="size-4" />} onClick={() => {
                        setAgentRoute("local");
                        useAgentStore.getState().setAgentState({ activeTab: "setup" });
                    }} />
                </Tooltip>
            </div> : null}
            <div className="flex min-h-0 flex-1 flex-col">
                {route === "local" ? <LocalCodexAgentPanel key="local" embedded headless={props.headless} autoConnect={props.autoConnect} /> : <SiteAgentPanel key="site" embedded={props.embedded} headless={props.headless} />}
            </div>
        </div>
    );
}
