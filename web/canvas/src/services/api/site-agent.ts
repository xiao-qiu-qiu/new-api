import { getSiteContext, siteFetch } from "@/integration/site-context";
import i18n from "@/i18n";

export const SITE_AGENT_MAX_ROUNDS = 24;
export const SITE_AGENT_MAX_TOOL_CALLS = 32;

export type SiteAgentToolCall = { id: string; type: "function"; function: { name: string; arguments: string } };
export type SiteAgentContent = string | Array<{ type: "text"; text: string } | { type: "image_url"; image_url: { url: string } }>;
export type SiteAgentResponseItem = { type: string; [key: string]: unknown };
export type SiteAgentMessage = {
    role: "system" | "user" | "assistant" | "tool";
    content: SiteAgentContent | null;
    tool_calls?: SiteAgentToolCall[];
    tool_call_id?: string;
    responseItems?: SiteAgentResponseItem[];
};
export type SiteAgentTool = { type: "function"; function: { name: string; description: string; parameters: Record<string, unknown> } };
export type SiteAgentUsage = { prompt_tokens?: number; completion_tokens?: number; prompt_tokens_details?: { cached_tokens?: number } };
type SiteAgentResponse = {
    choices?: Array<{ message?: { content?: string | null; tool_calls?: SiteAgentToolCall[]; refusal?: string }; finish_reason?: string }>;
    usage?: SiteAgentUsage;
    error?: { message?: string } | string;
    message?: string;
    success?: boolean;
    output?: SiteAgentResponseItem[];
    status?: string;
};
type SiteAgentResponsesUsage = { input_tokens?: number; output_tokens?: number; input_tokens_details?: { cached_tokens?: number } };

/** Replay native output items, including reasoning, when continuing a stateless Responses turn. */
function responsesInput(messages: SiteAgentMessage[]): SiteAgentResponseItem[] {
    const input: SiteAgentResponseItem[] = [];
    for (const message of messages) {
        if (message.role === "tool") {
            input.push({ type: "function_call_output", call_id: message.tool_call_id, output: message.content || "" });
            continue;
        }
        if (message.role === "assistant" && message.responseItems) {
            input.push(...message.responseItems);
            continue;
        }
        if (message.content) {
            let content: unknown = message.content;
            if (Array.isArray(message.content)) {
                content = message.content.map((part) => {
                    if (part.type === "image_url") return { type: "input_image", image_url: part.image_url.url };
                    return { type: "input_text", text: part.text };
                });
            }
            input.push({ type: "message", role: message.role, content });
        }
        for (const call of message.tool_calls || []) {
            input.push({ type: "function_call", call_id: call.id, name: call.function.name, arguments: call.function.arguments });
        }
    }
    return input;
}

function responsesReply(data: SiteAgentResponse) {
    if (data.status === "incomplete") throw new Error(i18n.t("agent.site.truncatedResponse", { defaultValue: "模型回复被截断，请缩短请求后重试" }));
    if (!Array.isArray(data.output)) throw new Error(i18n.t("agent.site.emptyResponse", { defaultValue: "模型没有返回内容" }));
    const text: string[] = [];
    const toolCalls: SiteAgentToolCall[] = [];
    for (const item of data.output) {
        if (item.type === "function_call") {
            toolCalls.push({ id: item.call_id as string, type: "function", function: { name: item.name as string, arguments: item.arguments as string } });
        }
        if (item.type === "message" && Array.isArray(item.content)) {
            for (const part of item.content) {
                if (part.type === "output_text" && typeof part.text === "string") text.push(part.text);
                if (part.type === "refusal") throw new Error(String(part.refusal));
            }
        }
    }
    const responseUsage = data.usage as SiteAgentResponsesUsage | undefined;
    let usage: SiteAgentUsage | undefined;
    if (responseUsage) usage = { prompt_tokens: responseUsage.input_tokens, completion_tokens: responseUsage.output_tokens, prompt_tokens_details: responseUsage.input_tokens_details };
    return { reply: { content: text.join("\n"), tool_calls: toolCalls }, responseItems: data.output, usage };
}

/** One non-streaming text/vision request; compatible with image.ts AiTextMessage[]. */
export async function requestSiteResponsesText(options: {
    model: string;
    group: string;
    messages: SiteAgentMessage[];
    signal?: AbortSignal;
    onDelta?: (text: string) => void;
    reasoningEffort?: string;
    request?: typeof siteFetch;
}): Promise<string> {
    options.signal?.throwIfAborted();
    const body: Record<string, unknown> = { model: options.model, group: options.group, input: responsesInput(options.messages), stream: false, store: false };
    if (options.reasoningEffort && options.reasoningEffort !== "auto") body.reasoning = { effort: options.reasoningEffort };
    const response = await (options.request || siteFetch)("/pg/responses", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: options.signal,
    });
    options.signal?.throwIfAborted();
    const data = await response.json() as SiteAgentResponse;
    options.signal?.throwIfAborted();
    if (!response.ok || data.success === false || data.error) {
        const error = typeof data.error === "string" ? data.error : data.error?.message;
        throw new Error(error || data.message || i18n.t("agent.site.requestFailed", { defaultValue: "请求失败（{{status}}）", status: response.status }));
    }
    const { reply } = responsesReply(data);
    if (reply.tool_calls.length) throw new Error(i18n.t("agent.site.invalidToolCall", { defaultValue: "模型返回了无效的工具调用" }));
    if (!reply.content.trim()) throw new Error(i18n.t("agent.site.emptyResponse", { defaultValue: "模型没有返回内容" }));
    options.onDelta?.(reply.content);
    return reply.content;
}

/** Keep assistant tool calls and every matching tool result together in history. */
export async function runSiteAgentLoop(options: {
    model: string;
    group: string;
    chatProtocol?: "chat" | "responses";
    messages: SiteAgentMessage[];
    tools: SiteAgentTool[];
    signal: AbortSignal;
    executeTool: (call: SiteAgentToolCall) => Promise<unknown>;
    onAssistant: (text: string) => void;
    onMessages: (messages: SiteAgentMessage[]) => void | Promise<void>;
    onUsage?: (usage: SiteAgentUsage) => void;
    request?: typeof siteFetch;
}) {
    const messages = [...options.messages];
    const availableTools = new Set(options.tools.map((tool) => tool.function.name));
    let totalToolCalls = 0;
    const useResponses = options.chatProtocol === "responses";
    for (let round = 0; round < SITE_AGENT_MAX_ROUNDS; round++) {
        options.signal.throwIfAborted();
        let path = "/pg/chat/completions";
        let body: Record<string, unknown> = { model: options.model, group: options.group, messages: messages.map(({ role, content, tool_calls, tool_call_id }) => ({ role, content, tool_calls, tool_call_id })), tools: options.tools, tool_choice: "auto", stream: false };
        if (useResponses) {
            path = "/pg/responses";
            body = { model: options.model, group: options.group, input: responsesInput(messages), tools: options.tools.map((tool) => ({ type: "function", ...tool.function, strict: false })), tool_choice: "auto", stream: false, store: false, include: ["reasoning.encrypted_content"] };
        }
        const response = await (options.request || siteFetch)(path, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
            signal: options.signal,
        });
        options.signal.throwIfAborted();
        const data = await response.json() as SiteAgentResponse;
        if (!response.ok || data.success === false || data.error) {
            const error = typeof data.error === "string" ? data.error : data.error?.message;
            throw new Error(error || data.message || i18n.t("agent.site.requestFailed", { defaultValue: "请求失败（{{status}}）", status: response.status }));
        }
        const choice = data.choices?.[0];
        let reply = choice?.message;
        let responseItems: SiteAgentResponseItem[] | undefined;
        let usage = data.usage;
        if (useResponses) {
            const normalized = responsesReply(data);
            reply = normalized.reply;
            responseItems = normalized.responseItems;
            usage = normalized.usage;
        }
        if (!reply) throw new Error(i18n.t("agent.site.emptyResponse", { defaultValue: "模型没有返回内容" }));
        if (choice?.finish_reason === "length") throw new Error(i18n.t("agent.site.truncatedResponse", { defaultValue: "模型回复被截断，请缩短请求后重试" }));
        if (reply.refusal) throw new Error(reply.refusal);
        if (usage) options.onUsage?.(usage);
        const calls = reply.tool_calls || [];
        if (!Array.isArray(calls)) throw new Error(i18n.t("agent.site.invalidToolCall", { defaultValue: "模型返回了无效的工具调用" }));
        if (totalToolCalls + calls.length > SITE_AGENT_MAX_TOOL_CALLS) return { status: "budget" as const, reason: "tool_calls" as const };
        if (calls.length && (calls.some((call) => !call.id || call.type !== "function" || !call.function?.name || typeof call.function.arguments !== "string") || new Set(calls.map((call) => call.id)).size !== calls.length)) {
            throw new Error(i18n.t("agent.site.invalidToolCall", { defaultValue: "模型返回了无效的工具调用" }));
        }
        options.signal.throwIfAborted();
        if (reply.content) options.onAssistant(reply.content);
        if (!calls.length) {
            if (!reply.content?.trim()) throw new Error(i18n.t("agent.site.emptyResponse", { defaultValue: "模型没有返回内容" }));
            messages.push({ role: "assistant", content: reply.content, responseItems });
            await options.onMessages([...messages]);
            return { status: "completed" as const };
        }
        totalToolCalls += calls.length;
        messages.push({ role: "assistant", content: reply.content || null, tool_calls: calls, responseItems });
        let canceled = false;
        for (const call of calls) {
            let result: unknown;
            try {
                options.signal.throwIfAborted();
                if (!availableTools.has(call.function.name)) throw new Error(`Unknown tool: ${call.function.name}`);
                result = await options.executeTool(call);
            } catch (error) {
                canceled ||= options.signal.aborted;
                let text = error instanceof Error ? error.message : String(error);
                if (canceled) text = "Agent turn canceled; this tool was not completed";
                result = { error: text };
            }
            messages.push({ role: "tool", tool_call_id: call.id, content: JSON.stringify(result ?? { ok: true }) });
        }
        await options.onMessages([...messages]);
        if (canceled) options.signal.throwIfAborted();
    }
    return { status: "budget" as const, reason: "rounds" as const };
}

export function siteAgentUserId() {
    const userId = getSiteContext()?.userId;
    return userId === undefined || userId === null ? "" : String(userId);
}
