import type { NavigateFunction } from "react-router-dom";
import { nanoid } from "nanoid";

import i18n from "@/i18n";
import { isSiteTool, runSiteTool } from "@/lib/agent/agent-site-tools";
import { applyCanvasAgentOps, type CanvasAgentOp, type CanvasAgentSnapshot } from "@/lib/canvas/canvas-agent-ops";
import { imageMetadata } from "@/lib/canvas/canvas-node-factory";
import { fitNodeSize } from "@/lib/canvas/canvas-node-size";
import { getNodeSpec, isRegisteredNodeType, listNodeDefinitions } from "@/lib/canvas/node-registry";
import { uploadImage } from "@/services/image-storage";
import type { SiteAgentTool } from "@/services/api/site-agent";
import type { AgentAttachment, AgentCanvasContext } from "@/stores/use-agent-store";
import type { CanvasGenerationMode, CanvasNodeMetadata } from "@/types/canvas";

const string = { type: "string" };
const number = { type: "number" };
const strings = { type: "array", items: string };
const record = { type: "object", additionalProperties: true };
const position = { type: "object", properties: { x: number, y: number }, required: ["x", "y"] };
const viewport = { type: "object", properties: { x: number, y: number, k: { type: "number", exclusiveMinimum: 0 } }, required: ["x", "y", "k"] };
const mode = { type: "string", enum: ["text", "image", "video", "audio"] };
const flowProperties = {
    prompt: string, title: string, mode, x: number, y: number, referenceNodeIds: strings, autoRun: { type: "boolean" },
    model: string, size: string, quality: string, count: { type: "integer", minimum: 1 }, seconds: string, vquality: string,
    generateAudio: string, watermark: string, videoMode: string, audioVoice: string, audioFormat: string, audioSpeed: string, audioInstructions: string,
};

function defineTool(name: string, description: string, properties: Record<string, unknown> = {}, required: string[] = []): SiteAgentTool {
    return { type: "function", function: { name, description, parameters: { type: "object", properties, required } } };
}

/** The browser exposes canvas operations, never shell or filesystem commands. */
export const SITE_AGENT_TOOLS: SiteAgentTool[] = [
    defineTool("canvas_get_state", "Read the current canvas, selection, viewport and registered node types."),
    defineTool("canvas_get_node", "Read a complete node by ID, including its text and generation metadata.", { id: string }, ["id"]),
    defineTool("canvas_get_selection", "Read the selected canvas nodes."),
    defineTool("canvas_export_snapshot", "Read a snapshot of the active canvas."),
    defineTool("canvas_apply_ops", "Atomically add/update/delete nodes, connect or delete connections, select nodes, set the viewport or start generation. Node IDs created earlier in the batch can be referenced by later operations. update_node.patch may contain title, position, width, height, metadata; node identity is immutable.", {
        ops: { type: "array", minItems: 1, items: { oneOf: [
            { type: "object", properties: { type: { const: "add_node" }, id: string, nodeType: string, title: string, position, x: number, y: number, width: number, height: number, metadata: record }, required: ["type", "nodeType"] },
            { type: "object", properties: { type: { const: "update_node" }, id: string, patch: record, metadata: record }, required: ["type", "id"] },
            { type: "object", properties: { type: { const: "delete_node" }, id: string, ids: strings, nodeType: string }, required: ["type"] },
            { type: "object", properties: { type: { const: "delete_connections" }, id: string, ids: strings, all: { type: "boolean" } }, required: ["type"] },
            { type: "object", properties: { type: { const: "connect_nodes" }, id: string, fromNodeId: string, toNodeId: string }, required: ["type", "fromNodeId", "toNodeId"] },
            { type: "object", properties: { type: { const: "select_nodes" }, ids: strings }, required: ["type", "ids"] },
            { type: "object", properties: { type: { const: "set_viewport" }, viewport }, required: ["type", "viewport"] },
            { type: "object", properties: { type: { const: "run_generation" }, nodeId: string, mode, prompt: string }, required: ["type", "nodeId"] },
        ] } },
    }, ["ops"]),
    defineTool("canvas_undo_ops", "Undo the last Agent canvas operation. This does not cancel a submitted generation task."),
    defineTool("canvas_create_node", "Create a built-in or registered plugin node.", { nodeType: string, id: string, title: string, x: number, y: number, width: number, height: number, metadata: record }, ["nodeType"]),
    defineTool("canvas_create_text_node", "Create a text node.", { text: string, title: string, x: number, y: number, width: number, height: number }, ["text"]),
    defineTool("canvas_create_attachment_nodes", "Import uploaded chat images into the canvas using the attachment IDs given in this conversation.", { attachmentIds: strings, x: number, y: number }, ["attachmentIds"]),
    defineTool("canvas_create_config_node", "Create a generation configuration node. autoRun starts generation; otherwise it only prepares the node.", flowProperties),
    defineTool("canvas_create_generation_flow", "Create connected prompt/config/reference nodes for text, image, video or audio. autoRun defaults to false. Use the canvas models listed in the system context, not the Agent model.", flowProperties, ["prompt"]),
    ...(["text", "image", "video", "audio"] as const).map((kind) => defineTool(`canvas_generate_${kind}`, `Create a connected ${kind} generation flow and submit it immediately. Submission is asynchronous; query generation_get_status for completion.`, flowProperties, ["prompt"])),
    defineTool("canvas_run_generation", "Start generation on an existing node. It returns submission status, not the finished media.", { nodeId: string, mode, prompt: string }, ["nodeId"]),
    defineTool("generation_get_status", "Read generation task status without starting another task.", { scope: { type: "string", enum: ["all", "canvas", "image", "video"] }, taskId: string, nodeIds: strings, limit: number }),
    defineTool("site_navigate", "Open an internal page: /canvas, /canvas/:id, /image, /video, /prompts or /assets. Navigating to a different canvas ends this turn; continue there with a new message.", { path: string }, ["path"]),
    defineTool("canvas_list_projects", "List locally stored canvases with IDs and titles.", { keyword: string, page: number, pageSize: number }),
    defineTool("workbench_image_get_config", "Read image workbench parameters and available models."),
    defineTool("workbench_image_generate", "Fill image workbench parameters and optionally run generation. run defaults to true.", { prompt: string, model: string, quality: string, size: string, count: number, run: { type: "boolean" } }, ["prompt"]),
    defineTool("workbench_video_get_config", "Read video workbench parameters and available models."),
    defineTool("workbench_video_generate", "Fill video workbench parameters and optionally run generation. run defaults to true.", { prompt: string, model: string, size: string, seconds: string, resolution: string, generateAudio: { type: "boolean" }, watermark: { type: "boolean" }, mode: { type: "string", enum: ["frames", "reference"] }, run: { type: "boolean" } }, ["prompt"]),
    defineTool("prompts_search", "Search the prompt library.", { keyword: string, category: string, tags: strings, page: number, pageSize: number }),
    defineTool("assets_list", "List saved assets.", { kind: { type: "string", enum: ["all", "text", "image", "video"] }, keyword: string, page: number, pageSize: number }),
    defineTool("assets_add", "Save text or an image to My Assets.", { kind: { type: "string", enum: ["text", "image"] }, title: string, content: string, imageUrl: string, tags: strings, source: string, note: string }, ["kind", "title"]),
];

export function siteAgentRecord(value: unknown): Record<string, unknown> {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Expected a JSON object");
    return value as Record<string, unknown>;
}

function requiredString(value: unknown, field: string): string {
    if (typeof value !== "string" || !value.trim()) throw new Error(`${field} is required`);
    return value;
}

function finiteNumber(value: unknown, field: string, positive = false): number {
    if (typeof value !== "number" || !Number.isFinite(value) || (positive && value <= 0)) throw new Error(`${field} must be ${positive ? "positive and " : ""}finite`);
    return value;
}

function stringIds(value: unknown): string[] {
    if (!Array.isArray(value) || value.some((id) => typeof id !== "string" || !id)) throw new Error("Expected node IDs");
    return value as string[];
}

function generationMode(value: unknown): CanvasGenerationMode {
    if (value === undefined) return "image";
    if (value === "text" || value === "image" || value === "video" || value === "audio") return value;
    throw new Error("Invalid generation mode");
}

function validateNodeFields(value: Record<string, unknown>) {
    if (value.title !== undefined && typeof value.title !== "string") throw new Error("title must be text");
    if (value.width !== undefined) finiteNumber(value.width, "width", true);
    if (value.height !== undefined) finiteNumber(value.height, "height", true);
    if (value.position !== undefined) {
        const point = siteAgentRecord(value.position);
        finiteNumber(point.x, "position.x");
        finiteNumber(point.y, "position.y");
    }
    if (value.metadata !== undefined) {
        const metadata = siteAgentRecord(value.metadata);
        if (metadata.generationMode !== undefined) generationMode(metadata.generationMode);
        if (metadata.count !== undefined && (!Number.isSafeInteger(metadata.count) || Number(metadata.count) < 1)) throw new Error("count must be a positive integer");
        for (const key of ["content", "prompt", "composerContent", "model", "size", "quality", "seconds", "vquality"] as const) {
            if (metadata[key] !== undefined && typeof metadata[key] !== "string") throw new Error(`${key} must be text`);
        }
    }
}

/** Validate an entire batch before touching the live canvas, including intra-batch references. */
export function prepareSiteCanvasOps(snapshot: CanvasAgentSnapshot, input: unknown): CanvasAgentOp[] {
    if (!Array.isArray(input) || !input.length) throw new Error("ops must be a non-empty array");
    const ops: CanvasAgentOp[] = [];
    let next = snapshot;
    for (const value of input) {
        const op = { ...siteAgentRecord(value) };
        if (op.type === "add_node") {
            const nodeType = requiredString(op.nodeType, "nodeType");
            if (!isRegisteredNodeType(nodeType)) throw new Error(`Unknown node type: ${nodeType}`);
            op.id = op.id === undefined ? `${nodeType}-${nanoid()}` : requiredString(op.id, "id");
            if (next.nodes.some((node) => node.id === op.id)) throw new Error(`Duplicate node: ${String(op.id)}`);
            for (const key of ["x", "y"] as const) if (op[key] !== undefined) finiteNumber(op[key], key);
            validateNodeFields(op);
        } else if (op.type === "update_node") {
            const id = requiredString(op.id, "id");
            if (!next.nodes.some((node) => node.id === id)) throw new Error(`Node not found: ${id}`);
            const patch = op.patch === undefined ? {} : siteAgentRecord(op.patch);
            if (Object.keys(patch).some((key) => !["title", "position", "width", "height", "metadata"].includes(key))) throw new Error("Unsupported node patch field");
            validateNodeFields(patch);
            validateNodeFields({ metadata: op.metadata });
        } else if (op.type === "delete_node" || op.type === "select_nodes") {
            let ids: string[] = [];
            if (op.ids !== undefined) ids = stringIds(op.ids);
            else if (op.id) ids = [requiredString(op.id, "id")];
            if (!ids.length && !(op.type === "delete_node" && typeof op.nodeType === "string")) throw new Error("Node IDs are required");
            if (ids.some((id) => !next.nodes.some((node) => node.id === id))) throw new Error("Node not found");
        } else if (op.type === "delete_connections") {
            if (op.all !== undefined && typeof op.all !== "boolean") throw new Error("all must be boolean");
            let ids: string[] = [];
            if (op.ids !== undefined) ids = stringIds(op.ids);
            else if (op.id) ids = [requiredString(op.id, "id")];
            if (!op.all && !ids.length) throw new Error("Connection IDs are required");
        } else if (op.type === "connect_nodes") {
            const from = requiredString(op.fromNodeId, "fromNodeId");
            const to = requiredString(op.toNodeId, "toNodeId");
            if (from === to || !next.nodes.some((node) => node.id === from) || !next.nodes.some((node) => node.id === to)) throw new Error("Invalid connection endpoints");
            if (op.id !== undefined) requiredString(op.id, "id");
            if (next.connections.some((connection) => connection.id === op.id)) throw new Error("Duplicate connection ID");
        } else if (op.type === "set_viewport") {
            const point = siteAgentRecord(op.viewport);
            finiteNumber(point.x, "viewport.x");
            finiteNumber(point.y, "viewport.y");
            finiteNumber(point.k, "viewport.k", true);
        } else if (op.type === "run_generation") {
            const id = requiredString(op.nodeId, "nodeId");
            const node = next.nodes.find((item) => item.id === id);
            if (!node || !["text", "image", "config", "video", "audio"].includes(node.type)) throw new Error("Generation node not found");
            if (op.mode !== undefined) generationMode(op.mode);
            if (op.prompt !== undefined && typeof op.prompt !== "string") throw new Error("prompt must be text");
        } else {
            throw new Error(`Unknown operation: ${String(op.type)}`);
        }
        const parsed = op as CanvasAgentOp;
        ops.push(parsed);
        next = applyCanvasAgentOps(next, [parsed]);
    }
    return ops;
}

function nextCanvasX(snapshot: CanvasAgentSnapshot): number {
    return snapshot.nodes.length ? Math.max(...snapshot.nodes.map((node) => node.position.x + node.width)) + 80 : 0;
}

function configNodeOp(input: Record<string, unknown>, x: number, y: number): CanvasAgentOp {
    const kind = generationMode(input.mode);
    const metadata: CanvasNodeMetadata = { generationMode: kind, composerContent: String(input.prompt || ""), prompt: String(input.prompt || ""), status: "idle" };
    for (const key of Object.keys(flowProperties)) {
        if (["mode", "prompt", "title", "x", "y", "referenceNodeIds", "autoRun"].includes(key) || input[key] === undefined) continue;
        Object.assign(metadata, { [key]: input[key] });
    }
    return { type: "add_node", id: `config-${nanoid()}`, nodeType: "config", title: typeof input.title === "string" ? input.title : getNodeSpec("config").title, position: { x, y }, metadata };
}

export function buildSiteCanvasOps(name: string, input: Record<string, unknown>, snapshot: CanvasAgentSnapshot): unknown[] {
    if (name === "canvas_apply_ops") return input.ops as unknown[];
    const x = input.x === undefined ? nextCanvasX(snapshot) : finiteNumber(input.x, "x");
    const y = input.y === undefined ? 0 : finiteNumber(input.y, "y");
    if (name === "canvas_create_node") return [{ ...input, type: "add_node", position: { x, y } }];
    if (name === "canvas_create_text_node") return [{ type: "add_node", nodeType: "text", title: input.title, width: input.width, height: input.height, position: { x, y }, metadata: { content: requiredString(input.text, "text"), status: "success" } }];
    if (name === "canvas_run_generation") return [{ ...input, type: "run_generation" }];
    if (name === "canvas_create_config_node") {
        const op = configNodeOp(input, x, y);
        return [op, ...(input.autoRun === true && op.type === "add_node" ? [{ type: "run_generation", nodeId: op.id, mode: generationMode(input.mode) }] : [])];
    }
    if (name === "canvas_create_generation_flow" || /^canvas_generate_(text|image|video|audio)$/.test(name)) {
        const prompt = requiredString(input.prompt, "prompt");
        const references = input.referenceNodeIds === undefined ? [] : stringIds(input.referenceNodeIds);
        const kind = name === "canvas_create_generation_flow" ? generationMode(input.mode) : generationMode(name.replace("canvas_generate_", ""));
        const textId = `text-${nanoid()}`;
        const tokens = [`@[node:${textId}]`, ...references.map((id) => `@[node:${id}]`)].join("\n");
        const config = configNodeOp({ ...input, mode: kind, prompt: tokens }, x + 420, y);
        if (config.type !== "add_node") throw new Error("Invalid generation config");
        return [
            { type: "add_node", id: textId, nodeType: "text", position: { x, y }, metadata: { content: prompt, status: "success" } },
            config,
            ...[textId, ...references].map((fromNodeId) => ({ type: "connect_nodes", fromNodeId, toNodeId: config.id })),
            { type: "select_nodes", ids: [config.id] },
            ...(input.autoRun === true || name.startsWith("canvas_generate_") ? [{ type: "run_generation", nodeId: config.id, mode: kind, prompt: tokens }] : []),
        ];
    }
    throw new Error(i18n.t("agent.siteTools.unknownTool", { name }));
}

export function compactSiteCanvas(snapshot: CanvasAgentSnapshot) {
    return {
        ...snapshot,
        nodes: snapshot.nodes.map((node) => ({ ...node, metadata: compactMediaMetadata(node.metadata || {}) })),
        nodeTypes: listNodeDefinitions().map((definition) => ({ type: definition.type, title: definition.title })),
    };
}

/** Blob URLs and binary strings are not useful model context; images travel as image_url parts. */
export function compactMediaMetadata(value: Record<string, unknown>): Record<string, unknown> {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => {
        if (typeof item === "string" && /^(data:|blob:)/.test(item)) return [key, "[media stored in browser]"];
        if (Array.isArray(item)) {
            return [key, item.map((entry) => {
                if (entry && typeof entry === "object") return compactMediaMetadata(entry as Record<string, unknown>);
                if (typeof entry === "string" && /^(data:|blob:)/.test(entry)) return "[media stored in browser]";
                return entry;
            })];
        }
        if (item && typeof item === "object") return [key, compactMediaMetadata(item as Record<string, unknown>)];
        return [key, item];
    }));
}

export async function executeSiteAgentTool(name: string, input: Record<string, unknown>, options: {
    getCanvas: () => AgentCanvasContext | null;
    navigate: NavigateFunction;
    attachments: AgentAttachment[];
    signal: AbortSignal;
    confirm: (name: string, input: Record<string, unknown>) => Promise<boolean>;
}) {
    options.signal.throwIfAborted();
    const context = options.getCanvas();
    if (name === "site_navigate") {
        const path = requiredString(input.path, "path");
        if (!/^\/(?:canvas(?:\/[\w-]+)?|image|video|prompts|assets|config)?$/.test(path)) throw new Error("Invalid internal route");
        options.navigate(path);
        return { ok: true, path };
    }
    if (isSiteTool(name)) {
        if (["assets_add", "workbench_image_generate", "workbench_video_generate"].includes(name) && !await options.confirm(name, input)) return { canceled: true };
        options.signal.throwIfAborted();
        return runSiteTool(name, input, options.navigate, { canvasSnapshot: context?.snapshot });
    }
    if (!context) throw new Error(i18n.t("agent.runtime.openCanvasFirst"));
    if (name === "canvas_get_state" || name === "canvas_export_snapshot") return compactSiteCanvas(context.snapshot);
    if (name === "canvas_get_selection") return compactSiteCanvas({ ...context.snapshot, nodes: context.snapshot.nodes.filter((node) => context.snapshot.selectedNodeIds.includes(node.id)) });
    if (name === "canvas_get_node") {
        const id = requiredString(input.id, "id");
        const node = context.snapshot.nodes.find((item) => item.id === id);
        if (!node) throw new Error(`Node not found: ${id}`);
        return { ...node, metadata: compactMediaMetadata(node.metadata || {}) };
    }
    if (name === "canvas_undo_ops") {
        if (!await options.confirm(name, input)) return { canceled: true };
        options.signal.throwIfAborted();
        const latest = options.getCanvas();
        const snapshot = latest?.undoOps();
        if (!snapshot) throw new Error(i18n.t("agent.site.noUndo", { defaultValue: "当前没有可撤销的 Agent 操作" }));
        return compactSiteCanvas(snapshot);
    }
    let rawOps: unknown[];
    if (name === "canvas_create_attachment_nodes") {
        rawOps = await Promise.all(stringIds(input.attachmentIds).map(async (id, index) => {
            const attachment = options.attachments.find((item) => item.id === id);
            if (!attachment) throw new Error(i18n.t("agent.runtime.attachmentReadFailed"));
            const image = await uploadImage(attachment.dataUrl, { signal: options.signal });
            const size = fitNodeSize(image.width, image.height);
            return { type: "add_node", nodeType: "image", title: attachment.name, position: { x: input.x === undefined ? nextCanvasX(context.snapshot) + index * (size.width + 40) : finiteNumber(input.x, "x") + index * (size.width + 40), y: input.y === undefined ? 0 : finiteNumber(input.y, "y") }, ...size, metadata: imageMetadata(image) };
        }));
    } else {
        rawOps = buildSiteCanvasOps(name, input, context.snapshot);
    }
    const ops = prepareSiteCanvasOps(context.snapshot, rawOps);
    if (!await options.confirm(name, { ...input, ops })) return { canceled: true };
    options.signal.throwIfAborted();
    const latest = options.getCanvas();
    if (!latest || latest.snapshot.projectId !== context.snapshot.projectId) throw new Error("Canvas changed during this tool call");
    // Revalidate after approval, since the user may have edited nodes while the card was open.
    const snapshot = latest.applyOps(prepareSiteCanvasOps(latest.snapshot, ops));
    return { ok: true, submittedNodeIds: ops.filter((op) => op.type === "run_generation").map((op) => op.nodeId), snapshot: compactSiteCanvas(snapshot) };
}
