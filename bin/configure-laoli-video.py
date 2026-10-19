#!/usr/bin/env python3
"""Print the Laoli video import plan; use --apply after deploying new-api.

Authentication uses NEW_API_ADMIN_TOKEN (a root management token).
Example: python bin/configure-laoli-video.py --apply --base-url https://www.codeq.top
         --channel-id 44 --backup-dir /private/backups/laoli-video
"""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import sys
from datetime import datetime
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


ROOT = Path(__file__).resolve().parents[1]
PLUGIN_DIR = ROOT / "plugins" / "tasks" / "laoli-video"
PLUGIN_PRICES = "billing_setting.plugin_billing_expr"
METADATA_FIELDS = (
    "id", "model_name", "description", "icon", "tags", "vendor_id",
    "endpoints", "status", "sync_official", "name_rule",
)
CHANNEL_FIELDS = ("id", "name", "type", "base_url", "models", "group", "setting")


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Management requests must keep credentials on the selected origin.
        return None


class ManagementAPI:
    def __init__(self, base_url, token):
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.opener = build_opener(NoRedirect)

    def request(self, method, path, body=None):
        payload = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
        req = Request(self.base_url + path, data=payload, method=method, headers={
            "Authorization": "Bearer " + self.token,
            "Content-Type": "application/json",
            "Accept": "application/json",
        })
        try:
            with self.opener.open(req, timeout=60) as response:
                result = json.load(response)
        except HTTPError as error:
            if error.code == 409:
                raise RuntimeError("Pricing changed concurrently; inspect the backup and rerun after review") from None
            raise RuntimeError(f"{method} {path.split('?')[0]} returned HTTP {error.code}") from None
        except (URLError, ValueError):
            raise RuntimeError(f"{method} {path.split('?')[0]} failed or returned invalid JSON") from None
        if not isinstance(result, dict) or result.get("success") is not True:
            message = result.get("message", "invalid response") if isinstance(result, dict) else "invalid response"
            raise RuntimeError(f"{method} {path.split('?')[0]}: {message}")
        return result.get("data")


def pricing_draft(configured, item, plugin_key):
    # The pricing API replaces a complete draft, so retain all other overrides.
    draft = copy.deepcopy(configured)
    draft["billing_setting.billing_mode"] = "tiered_expr"
    draft["billing_setting.billing_expr"] = item["model_expression"]
    draft.setdefault(PLUGIN_PRICES, {})[plugin_key] = item["provider_expression"]
    if item["unit"] == "request":
        draft["ModelPrice"] = item["model_price"]
    return draft


def metadata_draft(current, item):
    draft = {key: current[key] for key in METADATA_FIELDS if key in current}
    endpoints = json.loads(current.get("endpoints") or "{}")
    endpoints.update(item["metadata"]["endpoints"])
    draft.update(item["metadata"])
    draft.update({"endpoints": json.dumps(endpoints, ensure_ascii=False),
                  "status": 1, "sync_official": 0, "name_rule": 0})
    return draft


def find_metadata(api, name):
    page = 1
    matches = []
    while True:
        query = urlencode({"keyword": name, "p": page, "page_size": 100})
        result = api.request("GET", "/api/models/search?" + query)
        for row in result["items"]:
            if row["model_name"] == name and row.get("id", 0) > 0:
                if row.get("name_rule", 0) != 0:
                    raise RuntimeError(f"Existing metadata for {name} is a name rule; review it first")
                matches.append(row)
        if page * 100 >= result["total"]:
            break
        page += 1
    if len(matches) > 1:
        raise RuntimeError(f"Duplicate exact metadata for {name}")
    return matches[0] if matches else {}


def apply_catalog(args, catalog, source):
    token = os.environ.get("NEW_API_ADMIN_TOKEN", "").strip()
    if not token:
        raise RuntimeError("Set NEW_API_ADMIN_TOKEN to a root management token")
    target = urlsplit(args.base_url)
    if (target.scheme not in ("https", "http") or not target.netloc or target.username
            or target.password or target.query or target.fragment or target.path not in ("", "/")):
        raise RuntimeError("--base-url must be the new-api origin, without a path or credentials")
    if target.scheme == "http" and target.hostname not in ("localhost", "127.0.0.1", "::1"):
        raise RuntimeError("Use HTTPS for a remote management API")
    api = ManagementAPI(args.base_url, token)
    status = api.request("GET", "/api/status")
    # This catalog follows this station's existing 1 accounting unit = 1 RMB convention.
    display = status.get("quota_display_type")
    rate = status.get("custom_currency_exchange_rate") if display == "CUSTOM" else status.get("usd_exchange_rate")
    if display not in ("USD", "CUSTOM") or rate != catalog["currency"]["target_accounting_units_per_rmb"]:
        raise RuntimeError("Target currency convention changed; review the RMB coefficients before importing")

    channel = api.request("GET", f"/api/channel/{args.channel_id}")
    if channel.get("base_url", "").rstrip("/") != catalog["upstream_base_url"]:
        raise RuntimeError("The selected channel does not point at the catalog's Laoli upstream")
    if channel["type"] not in (1, 61):
        raise RuntimeError("Expected an OpenAI or task-plugin channel")
    plugin_key = catalog["plugin_key"]
    plugin = api.request("GET", "/api/plugin/task/" + plugin_key)
    if plugin.get("meta", {}).get("version") != catalog["plugin_version"] and plugin.get("meta", {}).get("version") != "1.0.0":
        raise RuntimeError("Unexpected installed plugin version; review before replacing it")

    models = catalog["models"]
    query = urlencode([("model", item["name"]) for item in models])
    snapshot = api.request("GET", "/api/option/model_pricing?" + query)
    entries = {entry["model_name"]: entry for entry in snapshot["entries"]}
    metadata = {item["name"]: find_metadata(api, item["name"]) for item in models}
    changes = []
    desired_pricing = {}
    desired_metadata = {}
    for item in models:
        name = item["name"]
        current = entries.get(name, {})
        configured = current.get("configured", {})
        draft = pricing_draft(configured, item, plugin_key)
        desired_pricing[name] = draft
        desired_metadata[name] = metadata_draft(metadata[name], item)
        if draft != configured:
            changes.append({"model_name": name, "expected_version": current.get("version", snapshot["empty_version"]),
                            "pricing": draft})

    channel_draft = {key: channel[key] for key in CHANNEL_FIELDS if key in channel}
    setting = json.loads(channel.get("setting") or "{}")
    existing_binding = setting.get("task_plugin_key", "")
    if existing_binding and existing_binding != plugin_key:
        raise RuntimeError("Selected channel is bound to another task plugin")
    setting["task_plugin_key"] = plugin_key
    names = list(dict.fromkeys(filter(None, channel["models"].split(","))))
    names.extend(item["name"] for item in models if item["name"] not in names)
    groups = list(dict.fromkeys(filter(None, channel["group"].split(","))))
    if catalog["target_group"] not in groups:
        groups.append(catalog["target_group"])
    channel_draft.update({"type": 61, "models": ",".join(names), "group": ",".join(groups),
                          "setting": json.dumps(setting, ensure_ascii=False, separators=(",", ":"))})

    backup_dir = Path(args.backup_dir).resolve()
    backup_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    backup_path = backup_dir / ("laoli-video-" + datetime.now().strftime("%Y%m%d-%H%M%S-%f") + ".json")
    before = {"base_url": api.base_url, "channel": {key: channel[key] for key in CHANNEL_FIELDS if key in channel},
              "plugin": plugin, "metadata": metadata, "pricing": snapshot}
    descriptor = os.open(backup_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as stream:
        json.dump(before, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(f"Backup: {backup_path}")

    source_hash = hashlib.sha256(source.encode("utf-8")).hexdigest()
    enabled = plugin.get("plugin", {}).get("enabled", True)
    if plugin["source"] != source or not enabled or plugin.get("layer") == "factory":
        api.request("POST", "/api/plugin/task", {"source": source, "sourceSha256": source_hash,
                    "enabled": True, "remark": "Laoli video catalog " + catalog["captured_at"]})
        # Uploading another version keeps the existing active override in place.
        plugin = api.request("GET", "/api/plugin/task/" + plugin_key)
    if plugin["source"] != source or plugin["meta"]["version"] != catalog["plugin_version"]:
        api.request("POST", "/api/plugin/task/" + plugin_key + "/activate", {"version": catalog["plugin_version"]})
        plugin = api.request("GET", "/api/plugin/task/" + plugin_key)
    if (plugin["source"] != source or plugin["meta"]["version"] != catalog["plugin_version"]
            or not plugin.get("plugin", {}).get("enabled", True)
            or not {item["name"] for item in models}.issubset(plugin["meta"].get("models", []))):
        raise RuntimeError("Plugin activation did not expose the expected catalog; no model settings were published")
    print("Plugin active: " + plugin_key + " " + catalog["plugin_version"])
    for name, draft in desired_metadata.items():
        current = {key: metadata[name][key] for key in METADATA_FIELDS if key in metadata[name]}
        if current != draft:
            api.request("PUT" if current else "POST", "/api/models/", draft)
    if changes:
        api.request("PATCH", "/api/option/model_pricing", {"changes": changes})
    # Publishing the channel's model list comes after plugin and price installation.
    current_channel = {key: channel[key] for key in CHANNEL_FIELDS if key in channel}
    if channel_draft != current_channel:
        api.request("PUT", "/api/channel/", channel_draft)

    after = api.request("GET", "/api/option/model_pricing?" + query)
    actual = {entry["model_name"]: entry["configured"] for entry in after["entries"]}
    if any(actual.get(name) != draft for name, draft in desired_pricing.items()):
        raise RuntimeError("Saved pricing differs from the plan; inspect the backup and server state")
    saved_channel = api.request("GET", f"/api/channel/{args.channel_id}")
    if (saved_channel["type"] != 61 or saved_channel["models"] != channel_draft["models"]
            or saved_channel["group"] != channel_draft["group"]
            or json.loads(saved_channel.get("setting") or "{}").get("task_plugin_key") != plugin_key):
        raise RuntimeError("Channel verification failed; inspect the backup and server state")
    print(f"Imported {len(models)} models into channel {args.channel_id}; no generation requests were sent")


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--apply", action="store_true", help="Apply the catalog through management APIs")
    parser.add_argument("--base-url", help="Target new-api origin (required with --apply)")
    parser.add_argument("--channel-id", type=int, default=44, help="Existing Laoli channel ID (default: 44)")
    parser.add_argument("--backup-dir", help="Private backup directory (required with --apply)")
    args = parser.parse_args()
    if args.apply and (not args.base_url or not args.backup_dir or args.channel_id <= 0):
        parser.error("--apply requires --base-url, --backup-dir and a positive --channel-id")
    catalog = json.loads((PLUGIN_DIR / "catalog.json").read_text(encoding="utf-8"))
    if args.apply:
        source = (PLUGIN_DIR / "plugin.js").read_text(encoding="utf-8")
        apply_catalog(args, catalog, source)
    else:
        plan = {"plugin": catalog["plugin_key"], "version": catalog["plugin_version"],
                "group": catalog["target_group"], "currency": catalog["currency"],
                "expression_basis": catalog["expression_basis"],
                "models": [{key: item[key] for key in ("name", "unit", "prices_rmb", "model_expression", "provider_expression")}
                           for item in catalog["models"]]}
        print(json.dumps(plan, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, RuntimeError) as error:
        print(f"Import stopped: {error}", file=sys.stderr)
        sys.exit(1)
