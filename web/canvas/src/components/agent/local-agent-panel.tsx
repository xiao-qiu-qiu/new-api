import { useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { App, Button, Tooltip } from "antd";
import { Bot, History, MessageSquare, PanelRightClose, Plus, Terminal, Undo2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useShallow } from "zustand/react/shallow";

import { getSiteContext } from "@/integration/site-context";
import { canvasThemes } from "@/lib/canvas-theme";
import { useAgentStore } from "@/stores/use-agent-store";
import { useThemeStore } from "@/stores/use-theme-store";
import { AgentChatTimeline, AgentUsageBar } from "./agent-chat";
import { AgentChatComposer } from "./agent-chat-composer";
import { AgentHistoryView } from "./agent-history-view";
import { AgentLogView } from "./agent-log-view";
import { AgentPanelTabs } from "./agent-panel-tabs";
import { addSiteAgentImages, decideSiteAgentTool, deleteSiteAgentThreads, initializeSiteAgent, newSiteAgentThread, resumeSiteAgentThread, sendSiteAgent, stopSiteAgent } from "./site-agent-runtime";

// Keep this entry point for the upstream layout; all execution is now in the browser.
export function LocalAgentPanel(props: { embedded?: boolean; headless?: boolean; autoConnect?: boolean }) {
    const { t } = useTranslation();
    const navigate = useNavigate();
    const { message, modal } = App.useApp();
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const state = useAgentStore(useShallow((current) => ({
        prompt: current.prompt, attachments: current.attachments, sending: current.sending, waiting: current.waiting,
        threads: current.threads, activeThreadId: current.activeThreadId, loadingThreads: current.loadingThreads,
        activeTab: current.activeTab, confirmTools: current.confirmTools, pendingTool: current.pendingTool,
        tokenUsage: current.tokenUsage, eventLogs: current.eventLogs, activity: current.activity,
        connected: current.connected, connectError: current.connectError, siteHistoryReady: current.siteHistoryReady,
        model: current.model, workspacePath: current.workspacePath, canUndo: current.canvasContext?.canUndo || false, siteBudgetReached: current.siteBudgetReached,
    })));
    const setAgentState = useAgentStore((current) => current.setAgentState);
    const closePanel = useAgentStore((current) => current.closePanel);

    useEffect(() => {
        const sync = () => { void initializeSiteAgent().catch((error: unknown) => useAgentStore.setState({ connectError: error instanceof Error ? error.message : String(error) })); };
        const receive = (event: MessageEvent) => {
            if (event.source === window.parent && event.origin === window.location.origin && event.data?.type === "new-api:canvas-context") queueMicrotask(sync);
        };
        sync();
        window.addEventListener("message", receive);
        return () => window.removeEventListener("message", receive);
    }, []);

    if (props.headless) return null;
    const busy = state.sending || state.waiting;
    const tab = state.activeTab === "history" || state.activeTab === "log" ? state.activeTab : "chat";
    const items = [
        { value: "chat" as const, label: t("agent.panel.chat"), icon: <MessageSquare className="size-4" /> },
        { value: "history" as const, label: t("agent.panel.history"), icon: <History className="size-4" />, count: state.threads.length },
        { value: "log" as const, label: t("agent.panel.logs"), icon: <Terminal className="size-4" />, count: state.eventLogs.length },
    ];
    const reportError = (error: unknown) => { void message.error(error instanceof Error ? error.message : t("agent.runtime.sendFailed")); };
    const siteContext = getSiteContext();

    return (
        <div className="flex h-full min-h-0 flex-col" style={{ color: theme.node.text }} data-canvas-shortcuts-ignore>
            <AgentPanelTabs value={tab} items={items} theme={theme} onChange={(activeTab) => setAgentState({ activeTab })}
                leading={<span className="flex items-center gap-1.5 px-1 text-sm font-medium"><Bot className="size-4" />Agent</span>}
                right={<>
                    <Tooltip title={t("canvas.undo")}><Button type="text" shape="circle" disabled={!state.canUndo || busy} aria-label={t("canvas.undo")} icon={<Undo2 className="size-4" />} onClick={() => useAgentStore.getState().canvasContext?.undoOps()} /></Tooltip>
                    <Tooltip title={t("agent.history.newThread")}><Button type="text" shape="circle" disabled={!state.siteHistoryReady || busy} aria-label={t("agent.history.newThread")} icon={<Plus className="size-4" />} onClick={newSiteAgentThread} /></Tooltip>
                    {props.embedded ? <Tooltip title={t("agent.panel.collapse")}><Button type="text" shape="circle" aria-label={t("agent.panel.collapseLabel")} icon={<PanelRightClose className="size-4" />} onClick={closePanel} /></Tooltip> : null}
                </>}
            />
            {state.connectError ? <div role="alert" className="px-4 pt-3 text-xs text-red-600 dark:text-red-400">{state.connectError}</div> : null}
            {tab === "history" ? <AgentHistoryView theme={theme} threads={state.threads} activeThreadId={state.activeThreadId} workspacePath={state.workspacePath} loading={state.loadingThreads} busy={busy} connected={state.connected}
                onRefresh={() => { void initializeSiteAgent(true).catch(reportError); }}
                onNewThread={newSiteAgentThread} onResumeThread={resumeSiteAgentThread}
                onDeleteThreads={(ids) => modal.confirm({ title: t("agent.runtime.deleteConversations", { count: ids.length }), content: t("agent.runtime.deleteConversationsDescription"), onOk: () => deleteSiteAgentThreads(ids) })}
            /> : null}
            {tab === "log" ? <AgentLogView logs={state.eventLogs} theme={theme} context={{ endpoint: "/pg/chat/completions", connected: state.connected, enabled: true, activity: state.activity, waiting: state.waiting, sending: state.sending, messages: useAgentStore.getState().messages.length, pendingTool: state.pendingTool?.name }} onClear={() => useAgentStore.getState().clearEventLogs()} onCopied={(text) => { void message.success(text); }} onCopyBlocked={(text) => { void message.error(text); }} /> : null}
            {tab === "chat" ? <>
                <AgentChatTimeline theme={theme} pendingTool={state.pendingTool} pendingApprovals={[]} sending={state.sending} waiting={state.waiting} onApproveTool={() => decideSiteAgentTool(true)} onRejectTool={() => decideSiteAgentTool(false)} onApprovalDecision={() => {}} />
                {state.tokenUsage ? <AgentUsageBar usage={state.tokenUsage} theme={theme} /> : null}
                {state.siteBudgetReached && !busy ? <Button type="text" onClick={() => {
                    setAgentState({ prompt: t("agent.site.continuePrompt", { defaultValue: "继续完成当前任务" }) });
                    void sendSiteAgent(navigate).catch(reportError);
                }}>{t("agent.site.continue", { defaultValue: "继续" })}</Button> : null}
                <AgentChatComposer prompt={state.prompt} attachments={state.attachments} disabled={!state.siteHistoryReady || !state.model} sending={busy} skillsEnabled={false}
                    placeholder={t("agent.site.placeholder", { defaultValue: "描述任务，或输入 @ 引用画布素材" })} theme={theme}
                    onPromptChange={(prompt) => setAgentState({ prompt })}
                    onSubmit={() => { void sendSiteAgent(navigate).catch(reportError); }} onStop={stopSiteAgent}
                    onAddFiles={(files) => addSiteAgentImages(files).catch(reportError)}
                    onRemoveAttachment={(id) => setAgentState({ attachments: state.attachments.filter((item) => item.id !== id) })}
                    confirmTools={state.confirmTools} onConfirmToolsChange={(confirmTools) => setAgentState({ confirmTools })}
                    left={<span className="max-w-40 truncate px-1 text-xs" title={`${siteContext.agentGroup} · ${siteContext.agentModel}`} style={{ color: theme.node.muted }}>{state.model || t("settingsPanels.model.select")}</span>}
                />
            </> : null}
        </div>
    );
}
