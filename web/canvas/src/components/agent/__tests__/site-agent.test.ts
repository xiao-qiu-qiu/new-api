import { beforeEach, expect, test, vi } from "vitest";
import type { SiteAgentMessage, SiteAgentToolCall } from "@/services/api/site-agent";
import type { CanvasAgentOp, CanvasAgentSnapshot } from "@/lib/canvas/canvas-agent-ops";
import type { AgentCanvasContext } from "@/stores/use-agent-store";

const fixtures = vi.hoisted(() => ({
    context: {
        userId: 7, theme: "light", language: "en-US", group: "media", model: "image-model",
        agentGroup: "agent group", agentModel: "reasoning-model",
        channels: [{ id: "agent group", name: "Agent", baseUrl: "", apiKey: "SITE_CREDENTIAL_SENTINEL", apiFormat: "openai", models: [{ name: "reasoning-model", capability: "image", capabilities: ["image", "text"], chatProtocol: "responses" }] }],
    },
    request: vi.fn<(path: string, init?: RequestInit) => Promise<Response>>(),
    stores: new Map<string, Map<string, unknown>>(),
}));

// Replace the parent request bridge and browser IndexedDB, keeping the actual Agent and canvas code.
vi.mock("@/integration/site-context", () => ({ getSiteContext: () => fixtures.context, siteFetch: fixtures.request }));
vi.mock("localforage", () => ({
    default: {
        createInstance: (options: { name: string; storeName: string }) => {
            const name = `${options.name}/${options.storeName}`;
            let values = fixtures.stores.get(name);
            if (!values) { values = new Map(); fixtures.stores.set(name, values); }
            const records = values;
            return {
                getItem: async (key: string) => records.get(key) ?? null,
                setItem: async (key: string, value: unknown) => { records.set(key, structuredClone(value)); return value; },
                removeItem: async (key: string) => { records.delete(key); },
            };
        },
    },
}));

beforeEach(() => {
    vi.resetModules();
    fixtures.stores.clear();
    fixtures.request.mockReset();
    fixtures.context.userId = 7;
});

const tools = [{ type: "function" as const, function: { name: "canvas_get_state", description: "Read canvas", parameters: { type: "object", properties: {} } } }];
const initialMessages: SiteAgentMessage[] = [{ role: "user", content: "Read the canvas" }];

function call(id: string): SiteAgentToolCall {
    return { id, type: "function", function: { name: "canvas_get_state", arguments: "{}" } };
}

function chatResponse(calls: SiteAgentToolCall[] = [], content = "") {
    return Response.json({ choices: [{ message: { content, tool_calls: calls } }] });
}

test("Responses sends flat tools, replays reasoning and function results, and retains terminal text", async () => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    const reasoning = { type: "reasoning", id: "rs_1", summary: [], encrypted_content: "opaque-reasoning" };
    const functionCall = { type: "function_call", id: "fc_1", call_id: "call_1", name: "canvas_get_state", arguments: "{}", status: "completed" };
    fixtures.request.mockResolvedValueOnce(Response.json({ status: "completed", output: [reasoning, functionCall] }))
        .mockResolvedValueOnce(Response.json({ status: "completed", output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "Canvas read" }] }], usage: { input_tokens: 21, output_tokens: 4, input_tokens_details: { cached_tokens: 10 } } }));
    const executeTool = vi.fn(async () => ({ nodes: [{ id: "node_1" }] }));
    const onMessages = vi.fn<(messages: SiteAgentMessage[]) => void>();
    const onUsage = vi.fn();
    const onAssistant = vi.fn();
    const outcome = await runSiteAgentLoop({ model: "reasoning-model", group: "agent group", chatProtocol: "responses", messages: [{ role: "user", content: [{ type: "text", text: "Read this" }, { type: "image_url", image_url: { url: "data:image/png;base64,fixture" } }] }], tools, signal: new AbortController().signal, executeTool, onMessages, onUsage, onAssistant });
    const [path, init] = fixtures.request.mock.calls[0];
    const first = JSON.parse(String(init?.body));
    expect(path).toBe("/pg/responses");
    expect(first).toMatchObject({ model: "reasoning-model", group: "agent group", stream: false, store: false, tools: [{ type: "function", name: "canvas_get_state", strict: false }] });
    expect(first.tools[0]).not.toHaveProperty("function");
    expect(first.input[0].content).toEqual([{ type: "input_text", text: "Read this" }, { type: "input_image", image_url: "data:image/png;base64,fixture" }]);
    const second = JSON.parse(String(fixtures.request.mock.calls[1][1]?.body));
    expect(second.input.slice(1)).toEqual([reasoning, functionCall, { type: "function_call_output", call_id: "call_1", output: '{"nodes":[{"id":"node_1"}]}' }]);
    expect(executeTool).toHaveBeenCalledWith(call("call_1"));
    expect(outcome).toEqual({ status: "completed" });
    expect(onAssistant).toHaveBeenCalledWith("Canvas read");
    expect(onMessages.mock.lastCall?.[0].at(-1)).toMatchObject({ role: "assistant", content: "Canvas read" });
    expect(onUsage).toHaveBeenCalledWith({ prompt_tokens: 21, completion_tokens: 4, prompt_tokens_details: { cached_tokens: 10 } });
});

test("Responses text/vision helper sends messages and reasoning effort, then calls onDelta once with full text", async () => {
    const { requestSiteResponsesText } = await import("@/services/api/site-agent");
    fixtures.request.mockResolvedValue(Response.json({ status: "completed", output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "First" }, { type: "output_text", text: "Second" }] }] }));
    const onDelta = vi.fn();
    const signal = new AbortController().signal;
    expect(await requestSiteResponsesText({ model: "vision-model", group: "media", messages: [{ role: "system", content: "Be concise" }, { role: "user", content: [{ type: "text", text: "Describe this" }, { type: "image_url", image_url: { url: "data:image/png;base64,fixture" } }] }], signal, onDelta, reasoningEffort: "high" })).toBe("First\nSecond");
    const [path, init] = fixtures.request.mock.calls[0];
    expect(path).toBe("/pg/responses");
    expect(init?.signal).toBe(signal);
    const body = JSON.parse(String(init?.body));
    expect(body).toMatchObject({ model: "vision-model", group: "media", stream: false, store: false, reasoning: { effort: "high" }, input: [{ role: "system", content: "Be concise" }, { role: "user", content: [{ type: "input_text", text: "Describe this" }, { type: "input_image", image_url: "data:image/png;base64,fixture" }] }] });
    expect(body).not.toHaveProperty("tools");
    expect(onDelta).toHaveBeenCalledExactlyOnceWith("First\nSecond");
});

test("Responses text helper omits auto reasoning and suppresses onDelta after cancellation during body parsing", async () => {
    const { requestSiteResponsesText } = await import("@/services/api/site-agent");
    const controller = new AbortController();
    const onDelta = vi.fn();
    const error = new DOMException("Stopped", "AbortError");
    fixtures.request.mockResolvedValue({ ok: true, json: async () => { controller.abort(error); return { output: [{ type: "message", content: [{ type: "output_text", text: "Too late" }] }] }; } } as Response);
    await expect(requestSiteResponsesText({ model: "m", group: "g", messages: initialMessages, signal: controller.signal, onDelta, reasoningEffort: "auto" })).rejects.toBe(error);
    expect(JSON.parse(String(fixtures.request.mock.calls[0][1]?.body))).not.toHaveProperty("reasoning");
    expect(onDelta).not.toHaveBeenCalled();
});

test.each(["chat", "responses"] as const)("%s rejects a batch exceeding the remaining cumulative 32-tool budget before storing it", async (chatProtocol) => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    const batches = [Array.from({ length: 20 }, (_, index) => call(`first_${index}`)), Array.from({ length: 13 }, (_, index) => call(`second_${index}`))];
    fixtures.request.mockImplementation(async () => {
        const batch = batches.shift() || [];
        if (chatProtocol === "responses") return Response.json({ status: "completed", output: batch.map((item) => ({ type: "function_call", call_id: item.id, ...item.function })) });
        return chatResponse(batch);
    });
    const executeTool = vi.fn(async () => ({ ok: true }));
    const onMessages = vi.fn<(messages: SiteAgentMessage[]) => void>();
    expect(await runSiteAgentLoop({ model: "m", group: "g", chatProtocol, messages: initialMessages, tools, signal: new AbortController().signal, executeTool, onMessages, onAssistant: vi.fn() })).toEqual({ status: "budget", reason: "tool_calls" });
    expect(fixtures.request).toHaveBeenCalledTimes(2);
    expect(executeTool).toHaveBeenCalledTimes(20);
    expect(onMessages).toHaveBeenCalledTimes(1);
    const history = onMessages.mock.lastCall?.[0] || [];
    expect(history.filter((item) => item.role === "tool")).toHaveLength(20);
    expect(history.some((item) => item.tool_calls?.some((itemCall) => itemCall.id.startsWith("second_")))).toBe(false);
});

test("chat defaults to /pg/chat/completions and pauses at 24 requests with complete tool pairs", async () => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    fixtures.request.mockImplementation(async () => chatResponse([call(`call_${fixtures.request.mock.calls.length}`)]));
    const executeTool = vi.fn(async () => ({ ok: true }));
    const onMessages = vi.fn<(messages: SiteAgentMessage[]) => void>();
    expect(await runSiteAgentLoop({ model: "m", group: "g", messages: initialMessages, tools, signal: new AbortController().signal, executeTool, onMessages, onAssistant: vi.fn() })).toEqual({ status: "budget", reason: "rounds" });
    expect(fixtures.request).toHaveBeenCalledTimes(24);
    expect(fixtures.request.mock.calls[0][0]).toBe("/pg/chat/completions");
    expect(JSON.parse(String(fixtures.request.mock.calls[0][1]?.body))).toMatchObject({ model: "m", group: "g", messages: initialMessages, stream: false });
    expect(onMessages.mock.lastCall?.[0].filter((item) => item.role === "tool")).toHaveLength(24);
});

test("canceling a tool skips queued execution and saves a result for every announced call", async () => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    const controller = new AbortController();
    const error = new DOMException("Stopped", "AbortError");
    fixtures.request.mockResolvedValue(chatResponse([call("first"), call("second"), call("third")]));
    const executeTool = vi.fn(async () => { controller.abort(error); throw error; });
    const onMessages = vi.fn<(messages: SiteAgentMessage[]) => void>();
    await expect(runSiteAgentLoop({ model: "m", group: "g", messages: initialMessages, tools, signal: controller.signal, executeTool, onMessages, onAssistant: vi.fn() })).rejects.toBe(error);
    expect(executeTool).toHaveBeenCalledTimes(1);
    expect(fixtures.request).toHaveBeenCalledTimes(1);
    expect(onMessages.mock.lastCall?.[0].filter((item) => item.role === "tool").map((item) => item.tool_call_id)).toEqual(["first", "second", "third"]);
});

test("canvas mutation validates the whole batch, supports intra-batch links, and uses original undo", async () => {
    const { executeSiteAgentTool } = await import("../site-agent-tools");
    const { registerNodeDefinitions } = await import("@/lib/canvas/node-registry");
    const { applyCanvasAgentOps } = await import("@/lib/canvas/canvas-agent-ops");
    registerNodeDefinitions(["text", "config"].map((type) => ({ type, title: type, icon: null, defaultSize: { width: 340, height: 240 } })));
    const snapshot: CanvasAgentSnapshot = { projectId: "project", title: "Canvas", nodes: [], connections: [], selectedNodeIds: [], viewport: { x: 0, y: 0, k: 1 } };
    let previous = snapshot;
    const context: AgentCanvasContext = {
        snapshot, canUndo: false,
        applyOps: vi.fn((ops?: CanvasAgentOp[]) => { previous = context.snapshot; context.snapshot = applyCanvasAgentOps(context.snapshot, ops); return context.snapshot; }),
        undoOps: vi.fn(() => { context.snapshot = previous; return previous; }),
    };
    const options = { getCanvas: () => context, navigate: vi.fn(), attachments: [], signal: new AbortController().signal, confirm: vi.fn(async () => true) };
    await expect(executeSiteAgentTool("canvas_apply_ops", { ops: [{ type: "add_node", nodeType: "text", id: "a" }, { type: "connect_nodes", fromNodeId: "a", toNodeId: "missing" }] }, options)).rejects.toThrow("Invalid connection endpoints");
    expect(context.applyOps).not.toHaveBeenCalled();
    options.confirm.mockResolvedValueOnce(false);
    expect(await executeSiteAgentTool("canvas_create_text_node", { text: "Denied" }, options)).toEqual({ canceled: true });
    expect(context.applyOps).not.toHaveBeenCalled();
    await executeSiteAgentTool("canvas_apply_ops", { ops: [{ type: "add_node", nodeType: "text", id: "a" }, { type: "add_node", nodeType: "config", id: "b" }, { type: "update_node", id: "a", metadata: { content: "Updated" } }, { type: "connect_nodes", fromNodeId: "a", toNodeId: "b" }] }, options);
    expect(context.snapshot.nodes[0].metadata?.content).toBe("Updated");
    expect(context.snapshot.connections).toMatchObject([{ fromNodeId: "a", toNodeId: "b" }]);
    await executeSiteAgentTool("canvas_undo_ops", {}, options);
    expect(context.undoOps).toHaveBeenCalledTimes(1);
    expect(context.snapshot.nodes).toEqual([]);
});

test("parent protocol/capabilities drive Agent requests; history reload isolates users and excludes credentials/usage", async () => {
    const runtime = await import("../site-agent-runtime");
    const { useAgentStore } = await import("@/stores/use-agent-store");
    await runtime.initializeSiteAgent();
    expect(useAgentStore.getState()).toMatchObject({ connected: true, enabled: true, model: "reasoning-model", token: "", url: "" });
    expect(runtime.isSiteAgentTextModel({ capability: ["image", "text"] })).toBe(true);
    expect(runtime.isSiteAgentTextModel({ capability: "image", capabilities: ["text"] })).toBe(true);
    expect(runtime.isSiteAgentTextModel({ capability: "image" })).toBe(false);
    fixtures.request.mockResolvedValue(Response.json({ status: "completed", output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "Saved answer" }] }], usage: { input_tokens: 99, output_tokens: 5 } }));
    useAgentStore.setState({ prompt: "Saved question", token: "AGENT_CREDENTIAL_SENTINEL" });
    await runtime.sendSiteAgent(vi.fn());
    expect(fixtures.request.mock.calls[0][0]).toBe("/pg/responses");
    const key = runtime.siteAgentHistoryKey("7");
    const stored = fixtures.stores.get("new-api:canvas:7:infinite-canvas/site_agent_history")?.get(key);
    expect(stored).toBeDefined();
    expect(JSON.stringify(stored)).not.toMatch(/CREDENTIAL_SENTINEL|tokenUsage|apiKey|input_tokens|output_tokens/);
    expect(useAgentStore.getState().tokenUsage).toEqual({ input: 99, cached: 0, output: 5 });
    fixtures.context.userId = 8;
    vi.resetModules();
    const other = await import("../site-agent-runtime");
    const { useAgentStore: otherStore } = await import("@/stores/use-agent-store");
    await other.initializeSiteAgent();
    expect(otherStore.getState().messages).toEqual([]);
    expect(other.siteAgentHistoryKey("8")).not.toBe(key);
    fixtures.context.userId = 7;
    vi.resetModules();
    const reloaded = await import("../site-agent-runtime");
    const { useAgentStore: reloadedStore } = await import("@/stores/use-agent-store");
    await reloaded.initializeSiteAgent();
    expect(reloadedStore.getState().messages.map((item) => item.text)).toEqual(["Saved question", "Saved answer"]);
});
