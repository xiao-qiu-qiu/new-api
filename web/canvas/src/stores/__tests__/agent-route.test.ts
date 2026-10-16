import { afterEach, beforeEach, expect, test, vi } from "vitest";

vi.mock("@/i18n", () => ({ default: { t: (key: string) => key } }));
vi.mock("@/lib/site-storage", () => ({
    getUserStorageKey: (key: string) => `user-7:${key}`,
    userLocalStorage: { getItem: () => null, setItem: vi.fn() },
}));

beforeEach(() => {
    vi.resetModules();
    vi.stubGlobal("window", { sessionStorage: { getItem: () => null, setItem: vi.fn() } });
});
afterEach(() => vi.unstubAllGlobals());

test("switching routes restores each route's draft, history, model and reasoning without copying credentials", async () => {
    const { useAgentStore } = await import("../use-agent-store");
    useAgentStore.setState({ model: "site-model", reasoningEffort: "high", prompt: "site draft", messages: [{ id: "site", role: "assistant", text: "online history" }], siteHistoryUserId: "7", siteHistoryReady: true });
    useAgentStore.getState().setAgentRoute("local");
    expect(useAgentStore.getState()).toMatchObject({ model: "", reasoningEffort: "", messages: [], siteHistoryUserId: "", siteHistoryReady: false });
    useAgentStore.setState({ url: "http://127.0.0.1:17371", token: "LOCAL_FIXTURE", model: "codex-model", reasoningEffort: "xhigh", prompt: "local draft", messages: [{ id: "local", role: "assistant", text: "local history" }] });
    useAgentStore.getState().setAgentRoute("site");
    expect(useAgentStore.getState()).toMatchObject({ url: "", token: "", model: "site-model", reasoningEffort: "high", prompt: "site draft", siteHistoryUserId: "7", messages: [{ id: "site", text: "online history" }] });
    useAgentStore.getState().setAgentRoute("local");
    expect(useAgentStore.getState()).toMatchObject({ token: "LOCAL_FIXTURE", model: "codex-model", reasoningEffort: "xhigh", prompt: "local draft", messages: [{ id: "local", text: "local history" }], connected: false, enabled: false });
});

test("switching away cleans the old transport before state changes and rejects its epoch after switching back", async () => {
    const { useAgentStore, isAgentRouteCurrent, registerAgentRouteCleanup } = await import("../use-agent-store");
    const epoch = useAgentStore.getState().routeEpoch;
    const observed: string[] = [];
    const unregister = registerAgentRouteCleanup("site", () => observed.push(useAgentStore.getState().route));
    useAgentStore.setState({ sending: true, waiting: true, pendingTool: { requestId: "tool", name: "canvas_apply_ops" }, pendingApprovals: [{ requestId: "approval", method: "command" }] });
    useAgentStore.getState().setAgentRoute("local");
    expect(observed).toEqual(["site"]);
    expect(useAgentStore.getState()).toMatchObject({ sending: false, waiting: false, pendingTool: null, pendingApprovals: [] });
    useAgentStore.getState().setAgentRoute("site");
    expect(isAgentRouteCurrent("site", epoch)).toBe(false);
    expect(isAgentRouteCurrent("site", useAgentStore.getState().routeEpoch)).toBe(true);
    unregister();
});

test("disconnecting the local transport clears active approvals while keeping the conversation and draft", async () => {
    const { useAgentStore, registerAgentRouteCleanup, isAgentRouteCurrent } = await import("../use-agent-store");
    useAgentStore.getState().setAgentRoute("local");
    const epoch = useAgentStore.getState().routeEpoch;
    const cleanup = vi.fn();
    const unregister = registerAgentRouteCleanup("local", cleanup);
    useAgentStore.setState({ enabled: true, connected: true, prompt: "saved draft", model: "codex-model", sending: true, pendingApprovals: [{ requestId: "approval", method: "command" }], messages: [{ id: "history", role: "user", text: "prior request" }] });
    useAgentStore.getState().disconnectAgent();
    expect(cleanup).toHaveBeenCalledOnce();
    expect(isAgentRouteCurrent("local", epoch)).toBe(false);
    expect(useAgentStore.getState()).toMatchObject({ enabled: false, connected: false, sending: false, pendingApprovals: [], prompt: "saved draft", model: "codex-model", messages: [{ id: "history", text: "prior request" }] });
    unregister();
});
