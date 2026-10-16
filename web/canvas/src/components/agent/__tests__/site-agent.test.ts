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
    historyWrites: 0,
    historyRead: undefined as undefined | ((key: string) => Promise<unknown>),
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
                getItem: async (key: string) => fixtures.historyRead ? fixtures.historyRead(key) : records.get(key) ?? null,
                setItem: async (key: string, value: unknown) => { fixtures.historyWrites++; records.set(key, structuredClone(value)); return value; },
                removeItem: async (key: string) => { records.delete(key); },
            };
        },
    },
}));

beforeEach(() => {
    vi.resetModules();
    fixtures.stores.clear();
    fixtures.request.mockReset();
    fixtures.historyWrites = 0;
    fixtures.historyRead = undefined;
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

function eventStream(events: unknown[]): Response {
    const text = events.map((event) => `data: ${typeof event === "string" ? event : JSON.stringify(event)}\r\n\r\n`).join("");
    const bytes = new TextEncoder().encode(text);
    return new Response(new ReadableStream({
        start(controller) {
            // Split both SSE delimiters and a Chinese UTF-8 character across reads.
            const character = bytes.findIndex((byte) => byte >= 0xe0);
            const split = character < 0 ? 7 : character + 1;
            controller.enqueue(bytes.slice(0, split));
            controller.enqueue(bytes.slice(split, split + 1));
            controller.enqueue(bytes.slice(split + 1));
            controller.close();
        },
    }), { headers: { "Content-Type": "text/event-stream" } });
}

test("Chat streams actual text deltas and assembles interleaved tool arguments before execution", async () => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    fixtures.request.mockResolvedValueOnce(eventStream([
        { choices: [{ index: 0, delta: { content: "读", tool_calls: [{ index: 0, id: "first", type: "function", function: { name: "canvas_", arguments: "{" } }] } }] },
        { choices: [{ index: 0, delta: { content: "取", tool_calls: [{ index: 1, id: "second", type: "function", function: { name: "canvas_get_state", arguments: "{}" } }, { index: 0, function: { name: "get_state", arguments: "}" } }] } }] },
        { choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }] },
        { choices: [], usage: { prompt_tokens: 9, completion_tokens: 3 } },
        "[DONE]",
    ])).mockResolvedValueOnce(chatResponse([], "Finished"));
    const executeTool = vi.fn(async (_call: SiteAgentToolCall) => ({ ok: true }));
    const onAssistantDelta = vi.fn((_delta: string) => expect(executeTool).not.toHaveBeenCalled());
    const onAssistant = vi.fn();
    const onUsage = vi.fn();
    await runSiteAgentLoop({ model: "m", group: "g", reasoningEffort: "high", messages: initialMessages, tools, signal: new AbortController().signal, executeTool, onAssistant, onAssistantDelta, onMessages: vi.fn(), onUsage });
    expect(onAssistantDelta.mock.calls.map(([delta]) => delta)).toEqual(["读", "取"]);
    expect(executeTool.mock.calls.map(([tool]) => tool)).toEqual([call("first"), call("second")]);
    expect(onAssistant.mock.calls).toEqual([["读取"], ["Finished"]]);
    expect(onUsage).toHaveBeenCalledWith({ prompt_tokens: 9, completion_tokens: 3 });
    expect(JSON.parse(String(fixtures.request.mock.calls[0][1]?.body))).toMatchObject({ stream: true, reasoning_effort: "high" });
});

test("Responses streams text once while replaying complete native reasoning and assembled tool output", async () => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    const reasoning = { type: "reasoning", id: "rs_stream", summary: [{ type: "summary_text", text: "Reasoned" }], encrypted_content: "opaque-stream-reasoning" };
    const message = { type: "message", id: "msg_stream", role: "assistant", status: "completed", content: [{ type: "output_text", text: "我来读", annotations: [] }] };
    const functionCall = { type: "function_call", id: "fc_stream", call_id: "stream_call", name: "canvas_get_state", arguments: "{}", status: "completed" };
    fixtures.request.mockResolvedValueOnce(eventStream([
        { type: "response.output_item.added", output_index: 0, item: { type: "reasoning", id: "rs_stream", summary: [] } },
        { type: "response.output_item.done", output_index: 0, item: reasoning },
        { type: "response.output_text.delta", output_index: 1, content_index: 0, item_id: "msg_stream", delta: "我" },
        { type: "response.output_text.delta", output_index: 1, content_index: 0, item_id: "msg_stream", delta: "来读" },
        { type: "response.output_text.done", output_index: 1, content_index: 0, text: "我来读" },
        { type: "response.output_item.done", output_index: 1, item: message },
        { type: "response.output_item.added", output_index: 2, item: { ...functionCall, arguments: "", status: "in_progress" } },
        { type: "response.function_call_arguments.delta", output_index: 2, item_id: "fc_stream", delta: "{" },
        { type: "response.function_call_arguments.delta", output_index: 2, item_id: "fc_stream", delta: "}" },
        { type: "response.function_call_arguments.done", output_index: 2, arguments: "{}" },
        { type: "response.output_item.done", output_index: 2, item: functionCall },
        { type: "response.completed", response: { status: "completed", output: [reasoning, message, functionCall] } },
    ])).mockResolvedValueOnce(eventStream([
        { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: "已读" },
        { type: "response.completed", response: { status: "completed", output: [{ ...message, content: [{ type: "output_text", text: "已读" }] }], usage: { input_tokens: 12, output_tokens: 2 } } },
    ]));
    const onAssistantDelta = vi.fn();
    const onAssistant = vi.fn();
    const executeTool = vi.fn(async () => ({ nodes: [] }));
    await runSiteAgentLoop({ model: "m", group: "g", chatProtocol: "responses", reasoningEffort: "xhigh", messages: initialMessages, tools, signal: new AbortController().signal, executeTool, onAssistant, onAssistantDelta, onMessages: vi.fn() });
    expect(onAssistantDelta.mock.calls).toEqual([["我"], ["来读"], ["已读"]]);
    expect(onAssistant.mock.calls).toEqual([["我来读"], ["已读"]]);
    expect(executeTool).toHaveBeenCalledExactlyOnceWith(call("stream_call"));
    const second = JSON.parse(String(fixtures.request.mock.calls[1][1]?.body));
    expect(second).toMatchObject({ stream: true, reasoning: { effort: "xhigh" } });
    expect(second).not.toHaveProperty("reasoning_effort");
    expect(second.input.slice(1)).toEqual([reasoning, message, functionCall, { type: "function_call_output", call_id: "stream_call", output: '{"nodes":[]}' }]);
});

test.each([
    ["chat", [{ choices: [{ delta: { content: "Partial", tool_calls: [{ index: 0, ...call("partial") }] } }] }]],
    ["chat", [{ choices: [{ delta: { tool_calls: [{ index: 0, ...call("partial") }] }, finish_reason: "length" }] }, "[DONE]"]],
    ["chat", [{ error: { message: "Stream failed" } }]],
    ["chat", ["{broken"]],
    ["responses", [{ type: "response.output_item.done", output_index: 0, item: { type: "function_call", call_id: "partial", name: "canvas_get_state", arguments: "{}" } }]],
    ["responses", [{ type: "response.incomplete", response: { status: "incomplete", output: [] } }]],
    ["responses", [{ type: "response.failed", response: { error: { message: "Stream failed" } } }]],
] as const)("%s stream failure or truncation never executes pending tools (%j)", async (chatProtocol, events) => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    fixtures.request.mockResolvedValue(eventStream([...events]));
    const executeTool = vi.fn(async () => ({ ok: true }));
    const onMessages = vi.fn();
    const onAssistantRoundEnd = vi.fn();
    await expect(runSiteAgentLoop({ model: "m", group: "g", chatProtocol, messages: initialMessages, tools, signal: new AbortController().signal, executeTool, onAssistant: vi.fn(), onAssistantDelta: vi.fn(), onAssistantRoundEnd, onMessages })).rejects.toThrow();
    expect(executeTool).not.toHaveBeenCalled();
    expect(onMessages).not.toHaveBeenCalled();
    expect(onAssistantRoundEnd).toHaveBeenCalledExactlyOnceWith(0);
});

test.each(["chat", "responses"] as const)("%s omits auto reasoning while preserving JSON response compatibility", async (chatProtocol) => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    const response = chatProtocol === "chat" ? chatResponse([], "Answer") : Response.json({ status: "completed", output: [{ type: "message", content: [{ type: "output_text", text: "Answer" }] }] });
    fixtures.request.mockResolvedValue(response);
    const onAssistant = vi.fn();
    const onAssistantDelta = vi.fn();
    await runSiteAgentLoop({ model: "m", group: "g", chatProtocol, reasoningEffort: "auto", messages: initialMessages, tools, signal: new AbortController().signal, executeTool: vi.fn(), onAssistant, onAssistantDelta, onMessages: vi.fn() });
    const body = JSON.parse(String(fixtures.request.mock.calls[0][1]?.body));
    expect(body).not.toHaveProperty("reasoning");
    expect(body).not.toHaveProperty("reasoning_effort");
    expect(onAssistant).toHaveBeenCalledExactlyOnceWith("Answer");
    expect(onAssistantDelta).not.toHaveBeenCalled();
});

test("canceling a pending stream read immediately releases it and retains only the already displayed increment", async () => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    const controller = new AbortController();
    const cancel = vi.fn();
    fixtures.request.mockResolvedValue(new Response(new ReadableStream({
        start(stream) { stream.enqueue(new TextEncoder().encode('data: {"choices":[{"delta":{"content":"Partial"}}]}\n\n')); },
        cancel,
    }), { headers: { "Content-Type": "text/event-stream" } }));
    let displayed!: () => void;
    const firstDelta = new Promise<void>((resolve) => { displayed = resolve; });
    const onAssistantDelta = vi.fn(displayed);
    const onAssistant = vi.fn();
    const executeTool = vi.fn();
    const onAssistantRoundEnd = vi.fn();
    const result = runSiteAgentLoop({ model: "m", group: "g", messages: initialMessages, tools, signal: controller.signal, executeTool, onAssistant, onAssistantDelta, onAssistantRoundEnd, onMessages: vi.fn() });
    const error = new DOMException("Stopped", "AbortError");
    const rejected = expect(result).rejects.toBe(error);
    await firstDelta;
    controller.abort(error);
    await rejected;
    expect(onAssistantDelta).toHaveBeenCalledExactlyOnceWith("Partial");
    expect(cancel).toHaveBeenCalled();
    expect(onAssistant).not.toHaveBeenCalled();
    expect(executeTool).not.toHaveBeenCalled();
    expect(onAssistantRoundEnd).toHaveBeenCalledExactlyOnceWith(0);
});

test("malformed assembled tool arguments reject the entire round before any tool runs", async () => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    fixtures.request.mockResolvedValue(eventStream([
        { choices: [{ delta: { tool_calls: [{ index: 0, ...call("valid") }, { index: 1, ...call("broken"), function: { name: "canvas_get_state", arguments: "{" } }] }, finish_reason: "tool_calls" }] },
        "[DONE]",
    ]));
    const executeTool = vi.fn();
    await expect(runSiteAgentLoop({ model: "m", group: "g", messages: initialMessages, tools, signal: new AbortController().signal, executeTool, onAssistant: vi.fn(), onMessages: vi.fn() })).rejects.toThrow();
    expect(executeTool).not.toHaveBeenCalled();
});

test("JSON cancellation during parsing suppresses completed text and tool execution", async () => {
    const { runSiteAgentLoop } = await import("@/services/api/site-agent");
    const controller = new AbortController();
    const error = new DOMException("Stopped", "AbortError");
    fixtures.request.mockResolvedValue({ ok: true, json: async () => { controller.abort(error); return { choices: [{ message: { content: "Too late", tool_calls: [call("late")] } }] }; } } as Response);
    const executeTool = vi.fn();
    const onAssistant = vi.fn();
    await expect(runSiteAgentLoop({ model: "m", group: "g", messages: initialMessages, tools, signal: controller.signal, executeTool, onAssistant, onMessages: vi.fn() })).rejects.toBe(error);
    expect(executeTool).not.toHaveBeenCalled();
    expect(onAssistant).not.toHaveBeenCalled();
});

test.each([false, true])("runtime displays before response completion, writes history at boundaries, and finalizes one bubble (cancel=%s)", async (cancel) => {
    const runtime = await import("../site-agent-runtime");
    const { useAgentStore } = await import("@/stores/use-agent-store");
    await runtime.initializeSiteAgent();
    useAgentStore.setState({ prompt: "Read", reasoningEffort: "high" });
    let stream!: ReadableStreamDefaultController<Uint8Array>;
    const readerCancel = vi.fn();
    fixtures.request.mockResolvedValue(new Response(new ReadableStream<Uint8Array>({ start(controller) { stream = controller; }, cancel: readerCancel }), { headers: { "Content-Type": "text/event-stream" } }));
    const displayed = (text: string) => new Promise<void>((resolve) => {
        const unsubscribe = useAgentStore.subscribe((state) => {
            if (!state.messages.some((item) => item.role === "assistant" && item.text === text)) return;
            unsubscribe();
            resolve();
        });
    });
    const firstDelta = displayed("我");
    const sending = runtime.sendSiteAgent(vi.fn());
    stream.enqueue(new TextEncoder().encode('event: response.output_text.delta\ndata: {"output_index":0,\ndata: "content_index":0,"delta":"我"}\n\n'));
    await firstDelta;
    const writes = fixtures.historyWrites;
    expect(useAgentStore.getState().messages.at(-1)).toMatchObject({ role: "assistant", text: "我", streamId: expect.any(String) });
    const secondDelta = displayed("我来读");
    stream.enqueue(new TextEncoder().encode('data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"来读"}\n\n'));
    await secondDelta;
    expect(fixtures.historyWrites).toBe(writes);
    if (cancel) runtime.stopSiteAgent();
    else {
        stream.enqueue(new TextEncoder().encode('data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"我来读"}]}]}}\n\n'));
        stream.close();
    }
    await sending;
    const assistants = useAgentStore.getState().messages.filter((item) => item.role === "assistant");
    expect(assistants).toHaveLength(1);
    expect(assistants[0].text).toBe("我来读");
    expect(assistants[0].streamId).toBeUndefined();
    expect(useAgentStore.getState().sending).toBe(false);
    expect(fixtures.historyWrites).toBeGreaterThan(writes);
    if (cancel) expect(readerCancel).toHaveBeenCalled();
    expect(JSON.parse(String(fixtures.request.mock.calls[0][1]?.body))).toMatchObject({ group: "agent group", model: "reasoning-model", reasoning: { effort: "high" } });
});

test.each(["local", "site"] as const)("a canceled old route run never updates %s state after its delayed JSON body resolves", async (destination) => {
    const runtime = await import("../site-agent-runtime");
    const { useAgentStore } = await import("@/stores/use-agent-store");
    await runtime.initializeSiteAgent();
    const localMessage = { id: "local", role: "assistant" as const, text: "Local reply" };
    useAgentStore.setState({ routeStates: { local: { ...useAgentStore.getState(), messages: [localMessage], model: "local-model", url: "http://localhost:17371", token: "LOCAL_SENTINEL" } }, prompt: "Old request" });
    let parsing!: () => void;
    const parsed = new Promise<void>((resolve) => { parsing = resolve; });
    let release!: (body: unknown) => void;
    const body = new Promise<unknown>((resolve) => { release = resolve; });
    fixtures.request.mockResolvedValueOnce({ ok: true, headers: new Headers({ "Content-Type": "application/json" }), json: () => { parsing(); return body; } } as Response);
    const oldSend = runtime.sendSiteAgent(vi.fn());
    await parsed;
    useAgentStore.getState().setAgentRoute("local");
    expect(fixtures.request.mock.calls[0][1]?.signal?.aborted).toBe(true);
    const local = useAgentStore.getState();
    await runtime.initializeSiteAgent();
    expect(useAgentStore.getState()).toBe(local);
    let finishNew = () => {};
    let newSend: Promise<void> | undefined;
    if (destination === "site") {
        useAgentStore.getState().setAgentRoute("site");
        await runtime.initializeSiteAgent();
        useAgentStore.setState({ prompt: "New request" });
        let stream!: ReadableStreamDefaultController<Uint8Array>;
        fixtures.request.mockResolvedValueOnce(new Response(new ReadableStream<Uint8Array>({ start(controller) { stream = controller; } }), { headers: { "Content-Type": "text/event-stream" } }));
        const displayed = new Promise<void>((resolve) => {
            const unsubscribe = useAgentStore.subscribe((state) => {
                if (!state.messages.some((item) => item.text === "New reply")) return;
                unsubscribe();
                resolve();
            });
        });
        newSend = runtime.sendSiteAgent(vi.fn());
        stream.enqueue(new TextEncoder().encode('data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"New reply"}\n\n'));
        await displayed;
        finishNew = () => {
            stream.enqueue(new TextEncoder().encode('data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"New reply"}]}],"usage":{"input_tokens":3,"output_tokens":2}}}\n\n'));
            stream.close();
        };
    }
    const current = useAgentStore.getState();
    release({ status: "completed", output: [{ type: "message", content: [{ type: "output_text", text: "Stale reply" }] }], usage: { input_tokens: 999, output_tokens: 999 } });
    await oldSend;
    expect(useAgentStore.getState()).toBe(current);
    if (destination === "site") {
        expect(current.sending).toBe(true);
        expect(current.messages.at(-1)?.text).toBe("New reply");
        finishNew();
        await newSend;
        expect(useAgentStore.getState().tokenUsage).toEqual({ input: 3, cached: 0, output: 2 });
    } else expect(current.messages).toEqual([localMessage]);
});

test.each([
    ["local", false],
    ["local", true],
    ["site", false],
    ["site", true],
] as const)("a stale asynchronous history read never changes %s state (failure=%s)", async (destination, failure) => {
    const runtime = await import("../site-agent-runtime");
    const { useAgentStore } = await import("@/stores/use-agent-store");
    useAgentStore.setState({ routeStates: { local: { ...useAgentStore.getState(), model: "local-model", connectError: "Local status" } } });
    let reading!: () => void;
    const started = new Promise<void>((resolve) => { reading = resolve; });
    let release!: () => void;
    fixtures.historyRead = () => new Promise((resolve, reject) => {
        reading();
        release = () => {
            if (failure) reject(new Error("Stale history error"));
            else resolve({ version: 1, threads: [{ id: "stale", messages: [{ id: "stale", role: "assistant", text: "Stale history" }], conversation: [] }] });
        };
    });
    const initialized = runtime.initializeSiteAgent();
    await started;
    useAgentStore.getState().setAgentRoute("local");
    if (destination === "site") {
        useAgentStore.getState().setAgentRoute("site");
        useAgentStore.setState({ connectError: "New site status" });
    }
    const current = useAgentStore.getState();
    release();
    await initialized;
    expect(useAgentStore.getState()).toBe(current);
});

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
    expect(first).toMatchObject({ model: "reasoning-model", group: "agent group", stream: true, store: false, tools: [{ type: "function", name: "canvas_get_state", strict: false }] });
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
    expect(JSON.parse(String(fixtures.request.mock.calls[0][1]?.body))).toMatchObject({ model: "m", group: "g", messages: initialMessages, stream: true });
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
