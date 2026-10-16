import { create } from "zustand";
import i18n from "@/i18n";
import { getUserStorageKey, userLocalStorage } from "@/lib/site-storage";

import type { CanvasAgentOp, CanvasAgentSnapshot } from "@/lib/canvas/canvas-agent-ops";
import type { CanvasResourceReference } from "@/lib/canvas/canvas-resource-references";
import type { SiteAgentMessage } from "@/services/api/site-agent";

export type AgentChatRole = "user" | "assistant" | "system" | "tool" | "error";
export type AgentAttachment = { id: string; name: string; type: string; size: number; width: number; height: number; url: string; dataUrl: string };
export type AgentMessageAttachment = Pick<AgentAttachment, "id" | "name" | "url"> & Partial<Pick<AgentAttachment, "type" | "size" | "width" | "height" | "dataUrl">>;
export type AgentCanvasReference = Pick<CanvasResourceReference, "nodeId" | "label" | "title" | "kind" | "previewUrl" | "text">;
export type AgentSkillReference = { name: string; path: string; displayName?: string };
export type AgentChatItem = { id: string; itemId?: string; clientMessageId?: string; threadId?: string; turnId?: string; role: AgentChatRole; title?: string; text: string; meta?: string; detail?: unknown; attachments?: AgentMessageAttachment[]; canvasReferences?: AgentCanvasReference[]; skill?: AgentSkillReference; streamId?: string; activityItems?: Record<string, string> };
export type AgentEventLog = { id: string; time: string; title: string; text: string; raw?: unknown };
export type AgentPendingToolCall = { requestId: string; name: string; input?: { ops?: CanvasAgentOp[]; path?: string } & Record<string, unknown> };
export type AgentPermissionMode = "request" | "automatic" | "full";
export type AgentReasoningEffort = "none" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max" | "ultra";
export type AgentRoute = "site" | "local";
export type AgentModel = {
    id: string;
    model: string;
    displayName: string;
    defaultReasoningEffort: AgentReasoningEffort;
    supportedReasoningEfforts: Array<{ reasoningEffort: AgentReasoningEffort; description?: string }>;
    isDefault?: boolean;
};
export type AgentApprovalDecision = "accept" | "acceptForSession" | "decline";
export type AgentPendingApproval = { requestId: string; method: string; threadId?: string; turnId?: string; itemId?: string; reason?: string; command?: unknown; cwd?: string; grantRoot?: string; networkApprovalContext?: unknown; permissions?: unknown; deciding?: AgentApprovalDecision };
export type AgentCanvasContext = { snapshot: CanvasAgentSnapshot; applyOps: (ops?: CanvasAgentOp[]) => CanvasAgentSnapshot; undoOps: () => CanvasAgentSnapshot | null; canUndo: boolean };
export type AgentThreadSummary = { id: string; preview: string; name?: string | null; cwd?: string; status?: string; source?: unknown; createdAt?: number; updatedAt?: number };
export type AgentSiteThread = AgentThreadSummary & { messages: AgentChatItem[]; conversation: SiteAgentMessage[]; projectId?: string };
export type AgentTokenUsage = { input: number; cached: number; output: number };
export type AgentBootstrapStatus = { key: string; text: string; detail: string; status: "running" | "ready" | "error" };
export type AgentConversationState = {
    revision: number;
    conversationId: string;
    threadId: string;
    status: "idle" | "preparing" | "ready" | "warning" | "running" | "failed";
    mcpStatuses: Record<string, { status: "starting" | "ready" | "failed" | "cancelled"; error?: string | null; failureReason?: string | null }>;
    sourceClientId?: string;
    error?: string;
};
export type AgentPanelTab = "chat" | "setup" | "history" | "skills" | "log";

type AgentStore = {
    route: AgentRoute;
    routeEpoch: number;
    routeStates: Partial<Record<AgentRoute, AgentRouteState>>;
    setAgentRoute: (route: AgentRoute) => void;
    width: number;
    panelOpen: boolean;
    panelMounted: boolean;
    panelClosing: boolean;
    canvasContext: AgentCanvasContext | null;
    url: string;
    token: string;
    connected: boolean;
    enabled: boolean;
    silentConnect: boolean;
    fragmentBootstrap: boolean;
    prompt: string;
    attachments: AgentAttachment[];
    canvasReferences: CanvasResourceReference[];
    sending: boolean;
    waiting: boolean;
    messages: AgentChatItem[];
    tokenUsage: AgentTokenUsage | null;
    eventLogs: AgentEventLog[];
    threads: AgentThreadSummary[];
    siteThreads: AgentSiteThread[];
    siteHistoryUserId: string;
    siteHistoryReady: boolean;
    siteBudgetReached: boolean;
    activeThreadId: string;
    activeTurnId: string;
    workspacePath: string;
    loadingThreads: boolean;
    activeTab: AgentPanelTab;
    confirmTools: boolean;
    permissionMode: AgentPermissionMode;
    models: AgentModel[];
    model: string;
    reasoningEffort: AgentReasoningEffort | "";
    activity: string;
    conversation: AgentConversationState;
    bootstrapStatus: AgentBootstrapStatus | null;
    mcpStartupStatuses: Record<string, AgentBootstrapStatus>;
    connectError: string;
    pendingTool: AgentPendingToolCall | null;
    pendingApprovals: AgentPendingApproval[];
    setAgentState: (patch: Partial<Omit<AgentStore, "setAgentState" | "connectAgent" | "disconnectAgent" | "addMessage" | "addEventLog" | "clearEventLogs" | "openPanel" | "closePanel" | "togglePanel" | "setCanvasContext">>) => void;
    openPanel: () => void;
    closePanel: () => void;
    togglePanel: () => void;
    setCanvasContext: (context: AgentCanvasContext | null) => void;
    connectAgent: (options?: { silent?: boolean }) => void;
    disconnectAgent: (patch?: Partial<Omit<AgentStore, "setAgentState" | "connectAgent" | "disconnectAgent" | "addMessage" | "addEventLog" | "clearEventLogs" | "openPanel" | "closePanel" | "togglePanel" | "setCanvasContext">>) => void;
    addMessage: (item: AgentChatItem) => void;
    addEventLog: (item: AgentEventLog) => void;
    clearEventLogs: () => void;
};

export const CANVAS_AGENT_PANEL_MOTION_MS = 500;

const routeFields = ["url", "token", "connected", "enabled", "silentConnect", "fragmentBootstrap", "prompt", "attachments", "canvasReferences", "sending", "waiting", "messages", "tokenUsage", "eventLogs", "threads", "siteThreads", "siteHistoryUserId", "siteHistoryReady", "siteBudgetReached", "activeThreadId", "activeTurnId", "workspacePath", "loadingThreads", "activeTab", "confirmTools", "permissionMode", "models", "model", "reasoningEffort", "activity", "conversation", "bootstrapStatus", "mcpStartupStatuses", "connectError", "pendingTool", "pendingApprovals"] as const;
type AgentRouteState = Pick<AgentStore, typeof routeFields[number]>;
const routeCleanups: Record<AgentRoute, Set<() => void>> = { site: new Set(), local: new Set() };

/** Dispose transport work synchronously before another route owns the shared view. */
export function registerAgentRouteCleanup(route: AgentRoute, cleanup: () => void): () => void {
    routeCleanups[route].add(cleanup);
    return () => { routeCleanups[route].delete(cleanup); };
}

export function isAgentRouteCurrent(route: AgentRoute, epoch: number): boolean {
    const state = useAgentStore.getState();
    return state.route === route && state.routeEpoch === epoch;
}

function routeDefaults(route: AgentRoute): AgentRouteState {
    return {
        url: route === "local" ? String(userLocalStorage.getItem("canvas-agent-url") || "http://127.0.0.1:17371") : "",
        token: route === "local" ? window.sessionStorage.getItem(getUserStorageKey("canvas-agent-token")) || "" : "",
        connected: false, enabled: false, silentConnect: false, fragmentBootstrap: false,
        prompt: "", attachments: [], canvasReferences: [], sending: false, waiting: false, messages: [], tokenUsage: null,
        eventLogs: [], threads: [], siteThreads: [], siteHistoryUserId: "", siteHistoryReady: false, siteBudgetReached: false,
        activeThreadId: "", activeTurnId: "", workspacePath: "", loadingThreads: false, activeTab: route === "local" ? "setup" : "chat",
        confirmTools: false, permissionMode: (userLocalStorage.getItem("canvas-agent-permission-mode") as AgentPermissionMode) || "request",
        models: [], model: route === "local" ? String(userLocalStorage.getItem("canvas-agent-model") || "") : "",
        reasoningEffort: (userLocalStorage.getItem(route === "local" ? "canvas-agent-local-reasoning-effort" : "canvas-agent-site-reasoning-effort") as AgentReasoningEffort) || "",
        activity: i18n.t("agent.state.ready"), conversation: { revision: 0, conversationId: "", threadId: "", status: "idle", mcpStatuses: {} },
        bootstrapStatus: null, mcpStartupStatuses: {}, connectError: "", pendingTool: null, pendingApprovals: [],
    };
}

export const useAgentStore = create<AgentStore>((set, get) => ({
    route: "site",
    routeEpoch: 0,
    routeStates: {},
    setAgentRoute: (route) => {
        if (get().route === route) return;
        for (const cleanup of routeCleanups[get().route]) cleanup();
        const state = get();
        const saved = Object.fromEntries(routeFields.map((key) => [key, state[key]])) as AgentRouteState;
        // The native transport releases Blob URLs on unmount; keep drafts usable on return.
        saved.attachments = saved.attachments.map((item) => ({ ...item, url: item.dataUrl || item.url }));
        saved.messages = saved.messages.map((item) => ({ ...item, streamId: undefined }));
        const stopped = { sending: false, waiting: false, loadingThreads: false, pendingTool: null, pendingApprovals: [], activeTurnId: "", bootstrapStatus: null, mcpStartupStatuses: {} };
        const restored = state.routeStates[route] || routeDefaults(route);
        set({
            ...restored, ...stopped, connected: route === "site" && restored.connected, enabled: route === "site" && restored.enabled,
            conversation: { ...restored.conversation, status: restored.activeThreadId ? "ready" : "idle", mcpStatuses: {} },
            route, routeEpoch: state.routeEpoch + 1,
            routeStates: { ...state.routeStates, [state.route]: { ...saved, ...stopped, connected: state.route === "site" && saved.connected, enabled: state.route === "site" && saved.enabled } },
        });
    },
    width: Number(userLocalStorage.getItem("canvas-agent-panel-width")) || 440,
    panelOpen: false,
    panelMounted: true,
    panelClosing: false,
    canvasContext: null,
    url: "",
    token: "",
    connected: false,
    enabled: false,
    silentConnect: false,
    fragmentBootstrap: false,
    prompt: "",
    attachments: [],
    canvasReferences: [],
    sending: false,
    waiting: false,
    messages: [],
    tokenUsage: null,
    eventLogs: [],
    threads: [],
    siteThreads: [],
    siteHistoryUserId: "",
    siteHistoryReady: false,
    siteBudgetReached: false,
    activeThreadId: "",
    activeTurnId: "",
    workspacePath: "",
    loadingThreads: false,
    activeTab: "chat",
    confirmTools: false,
    permissionMode: (userLocalStorage.getItem("canvas-agent-permission-mode") as AgentPermissionMode) || "request",
    models: [],
    model: "",
    reasoningEffort: (userLocalStorage.getItem("canvas-agent-site-reasoning-effort") as AgentReasoningEffort) || "",
    activity: i18n.t("agent.state.ready"),
    conversation: { revision: 0, conversationId: "", threadId: "", status: "idle", mcpStatuses: {} },
    bootstrapStatus: null,
    mcpStartupStatuses: {},
    connectError: "",
    pendingTool: null,
    pendingApprovals: [],
    setAgentState: (patch) => set(patch),
    openPanel: () => set({ panelOpen: true, panelMounted: true, panelClosing: false }),
    closePanel: () => {
        if (!get().panelMounted || get().panelClosing) return;
        set({ panelOpen: false, panelClosing: true });
        setTimeout(() => {
            if (get().panelClosing) set({ panelClosing: false });
        }, CANVAS_AGENT_PANEL_MOTION_MS);
    },
    togglePanel: () => (get().panelOpen ? get().closePanel() : get().openPanel()),
    setCanvasContext: (canvasContext) => set({ canvasContext }),
    connectAgent: (options) => {
        if (get().route === "site") return set({ enabled: true, connected: true, fragmentBootstrap: false, activity: i18n.t("agent.state.ready"), connectError: "", activeTab: "chat" });
        const endpoint = get().url.trim().replace(/\/$/, "");
        const token = get().token.trim();
        if (!endpoint || !token) return set({ connectError: options?.silent ? "" : i18n.t("agent.state.connectionRequired") });
        try {
            if (!["http:", "https:"].includes(new URL(endpoint).protocol)) throw new Error();
        } catch {
            return set({ connectError: options?.silent ? "" : i18n.t("agent.state.invalidUrl") });
        }
        userLocalStorage.setItem("canvas-agent-url", endpoint);
        window.sessionStorage.setItem(getUserStorageKey("canvas-agent-token"), token);
        set({ url: endpoint, token, enabled: true, connected: false, silentConnect: options?.silent || false, connectError: "", activeTab: "setup" });
    },
    disconnectAgent: (patch = {}) => {
        for (const cleanup of routeCleanups[get().route]) cleanup();
        const routeEpoch = get().routeEpoch + Number(get().route === "local");
        set({ enabled: false, connected: false, sending: false, waiting: false, pendingTool: null, pendingApprovals: [], activeTurnId: "", silentConnect: false, fragmentBootstrap: false, activity: i18n.t("agent.state.offline"), conversation: { revision: 0, conversationId: "", threadId: "", status: "idle", mcpStatuses: {} }, bootstrapStatus: null, mcpStartupStatuses: {}, ...patch, routeEpoch });
    },
    addMessage: (item) => set((state) => ({ messages: [...state.messages, item] })),
    addEventLog: (item) => set((state) => ({ eventLogs: [...state.eventLogs.slice(-160), item] })),
    clearEventLogs: () => set({ eventLogs: [] }),
}));
