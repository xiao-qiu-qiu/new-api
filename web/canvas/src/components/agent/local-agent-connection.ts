/** Accept upstream share links and new-api hash routes without navigating the iframe. */
export function parseLocalAgentConnectionLink(value: string): { url: string; token: string } | null {
    let link: URL;
    try {
        link = new URL(value.trim());
    } catch {
        return null;
    }
    const fragment = link.hash.slice(1);
    const queryStart = fragment.indexOf("?");
    const fragmentParams = new URLSearchParams(queryStart >= 0 ? fragment.slice(queryStart + 1) : fragment);
    const url = (fragmentParams.get("agentUrl") || link.searchParams.get("agentUrl") || "").trim();
    const token = (fragmentParams.get("agentToken") || link.searchParams.get("agentToken") || "").trim();
    if (!url || !token) return null;
    try {
        if (!["http:", "https:"].includes(new URL(url).protocol)) return null;
    } catch {
        return null;
    }
    return { url: url.replace(/\/$/, ""), token };
}
