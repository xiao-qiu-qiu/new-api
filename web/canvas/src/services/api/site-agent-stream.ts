import i18n from "@/i18n";
import type { SiteAgentResponse, SiteAgentResponseItem, SiteAgentToolCall } from "./site-agent";

type SseEvent = { event: string; data: string };
type ChatDelta = { content?: string; refusal?: string; tool_calls?: Array<{ index: number; id?: string; type?: string; function?: { name?: string; arguments?: string } }> };
type AgentStreamEvent = Omit<SiteAgentResponse, "choices"> & {
    type?: string;
    delta?: string;
    output_index?: number;
    content_index?: number;
    item?: SiteAgentResponseItem;
    arguments?: string;
    response?: SiteAgentResponse;
    choices?: Array<{ index?: number; delta?: ChatDelta; message?: { content?: string | null; tool_calls?: SiteAgentToolCall[]; refusal?: string }; finish_reason?: string }>;
};

function interruptedStream(): Error {
    return new Error(i18n.t("agent.site.truncatedResponse", { defaultValue: "模型回复被截断，请缩短请求后重试" }));
}

/** SSE line framing and UTF-8 decoding remain incremental across arbitrary transport boundaries. */
async function* siteSseEvents(response: Response, signal: AbortSignal): AsyncGenerator<SseEvent> {
    if (!response.body) throw interruptedStream();
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    const abort = () => { void reader.cancel(signal.reason).catch(() => undefined); };
    signal.addEventListener("abort", abort, { once: true });
    let buffer = "";
    let event = "";
    let data: string[] = [];
    try {
        while (true) {
            signal.throwIfAborted();
            const chunk = await reader.read();
            signal.throwIfAborted();
            buffer += decoder.decode(chunk.value, { stream: !chunk.done });
            while (buffer.length) {
                const newline = buffer.search(/[\r\n]/);
                if (newline < 0 && !chunk.done) break;
                if (newline === buffer.length - 1 && buffer[newline] === "\r" && !chunk.done) break;
                const line = newline < 0 ? buffer : buffer.slice(0, newline);
                let end = newline + 1;
                if (buffer[newline] === "\r" && buffer[end] === "\n") end++;
                buffer = newline < 0 ? "" : buffer.slice(end);
                if (!line) {
                    if (data.length) yield { event, data: data.join("\n") };
                    event = "";
                    data = [];
                    continue;
                }
                const colon = line.indexOf(":");
                const field = colon < 0 ? line : line.slice(0, colon);
                const value = colon < 0 ? "" : line.slice(colon + 1).replace(/^ /, "");
                if (field === "data") data.push(value);
                if (field === "event") event = value;
            }
            if (chunk.done) {
                if (data.length) yield { event, data: data.join("\n") };
                return;
            }
        }
    } finally {
        signal.removeEventListener("abort", abort);
        await reader.cancel().catch(() => undefined);
        reader.releaseLock();
    }
}

/** A positive protocol completion is required before returning any assembled tool calls. */
export async function readSiteAgentStream(response: Response, protocol: "chat" | "responses", signal: AbortSignal, onDelta?: (text: string) => void): Promise<SiteAgentResponse> {
    const calls = new Map<number, SiteAgentToolCall>();
    const items = new Map<number, SiteAgentResponseItem>();
    const textParts = new Set<string>();
    let content = "";
    let refusal = "";
    let finishReason = "";
    let usage: SiteAgentResponse["usage"];
    let completed: SiteAgentResponse | undefined;
    for await (const event of siteSseEvents(response, signal)) {
        signal.throwIfAborted();
        if (!event.data.trim()) continue;
        if (event.data.trim() === "[DONE]") break;
        let data: AgentStreamEvent;
        try {
            data = JSON.parse(event.data) as AgentStreamEvent;
        } catch {
            throw interruptedStream();
        }
        if (!data || typeof data !== "object" || Array.isArray(data)) throw interruptedStream();
        const type = data.type || event.event;
        const failure = data.error || data.response?.error;
        if (failure || data.success === false || type === "error" || type === "response.failed") {
            const message = typeof failure === "string" ? failure : failure?.message;
            throw new Error(message || data.message || interruptedStream().message);
        }
        if (protocol === "chat") {
            if (data.usage) usage = data.usage;
            const choice = data.choices?.find((item) => item.index === undefined || item.index === 0);
            if (!choice) continue;
            if (choice.finish_reason) finishReason = choice.finish_reason;
            if (choice.delta?.refusal) refusal += choice.delta.refusal;
            const delta = choice.delta?.content;
            if (typeof delta === "string" && delta) {
                content += delta;
                onDelta?.(delta);
            }
            if (choice.message) {
                content = choice.message.content || content;
                refusal = choice.message.refusal || refusal;
                choice.message.tool_calls?.forEach((call, index) => calls.set(index, call));
            }
            if (choice.delta?.tool_calls && !Array.isArray(choice.delta.tool_calls)) throw interruptedStream();
            for (const part of choice.delta?.tool_calls || []) {
                if (!Number.isInteger(part.index) || part.index < 0 || part.type && part.type !== "function") throw interruptedStream();
                const call = calls.get(part.index) || { id: "", type: "function", function: { name: "", arguments: "" } };
                // IDs may be repeated by compatible providers; names and arguments are deltas.
                if (part.id && call.id !== part.id) call.id += part.id;
                if (part.function?.name) call.function.name += part.function.name;
                if (part.function?.arguments) call.function.arguments += part.function.arguments;
                calls.set(part.index, call);
            }
            continue;
        }
        if (type === "response.incomplete") throw interruptedStream();
        if (type === "response.completed") {
            if (!data.response) throw interruptedStream();
            completed = { ...data.response, output: data.response.output || [...items.entries()].sort(([a], [b]) => a - b).map(([, item]) => item) };
            break;
        }
        if (type === "response.output_item.added" || type === "response.output_item.done") {
            if (!data.item || !Number.isInteger(data.output_index)) throw interruptedStream();
            items.set(data.output_index as number, { ...data.item });
            continue;
        }
        if (type === "response.function_call_arguments.delta" || type === "response.function_call_arguments.done") {
            const item = items.get(data.output_index as number);
            if (!item || item.type !== "function_call") throw interruptedStream();
            if (type === "response.function_call_arguments.done") item.arguments = data.arguments;
            else item.arguments = String(item.arguments || "") + (data.delta || "");
            continue;
        }
        if (type === "response.output_text.delta" && typeof data.delta === "string" && data.delta) {
            const key = `${data.output_index ?? 0}:${data.content_index ?? 0}`;
            const separator = textParts.size && !textParts.has(key) ? "\n" : "";
            textParts.add(key);
            onDelta?.(separator + data.delta);
        }
    }
    signal.throwIfAborted();
    if (protocol === "responses") {
        if (!completed || completed.status && completed.status !== "completed") throw interruptedStream();
        return completed;
    }
    if (finishReason !== "stop" && finishReason !== "tool_calls") throw interruptedStream();
    return { choices: [{ message: { content, tool_calls: [...calls.entries()].sort(([a], [b]) => a - b).map(([, call]) => call), refusal }, finish_reason: finishReason }], usage };
}
