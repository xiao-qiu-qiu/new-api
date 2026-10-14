import { expect, test } from "vitest";

import { parseLocalAgentConnectionLink } from "../local-agent-connection";

test.each([
    "https://canvas.example/#agentUrl=http%3A%2F%2F127.0.0.1%3A17371%2F&agentToken=LOCAL_FIXTURE",
    "https://canvas.example/canvas?agentUrl=http%3A%2F%2F127.0.0.1%3A17371%2F&agentToken=LOCAL_FIXTURE",
    "https://canvas.example/canvas-app/#/canvas?agentUrl=http%3A%2F%2F127.0.0.1%3A17371%2F&agentToken=LOCAL_FIXTURE",
])("pasting a complete connection link fills endpoint and token without navigation: %s", (link) => {
    expect(parseLocalAgentConnectionLink(link)).toEqual({ url: "http://127.0.0.1:17371", token: "LOCAL_FIXTURE" });
});

test.each(["http://127.0.0.1:17371", "not a URL", "https://canvas.example/#agentToken=LOCAL_FIXTURE", "https://canvas.example/#agentUrl=javascript%3Avoid(0)&agentToken=LOCAL_FIXTURE"])("ordinary addresses and incomplete or invalid links remain available for manual editing: %s", (link) => {
    expect(parseLocalAgentConnectionLink(link)).toBeNull();
});
