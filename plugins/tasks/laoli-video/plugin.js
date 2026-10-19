export const meta = {
  apiVersion: 1,
  key: "laoli-video",
  name: "Laoli Video",
  icon: "text:H3",
  version: "1.0.0",
  author: { name: "QuantumNous" },
  description: { en: "H3-KS video generation via Laoli", zh: "通过老李生成 H3-KS 视频" },
  baseUrl: "https://video.laoliimage2.win",
  website: "https://video.laoliimage2.win/docs/video-api/",
  models: ["H3-KS"],
  fetchMode: "per_task",
  protocols: ["openai_video"],
  // H3-KS is priced per completed video, independently of its duration.
  usageSchema: {
    video_count: {
      type: "number",
      unit: "count",
      unitLabel: { en: "video", zh: "条" },
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
  },
};

function referenceURL(value) {
  if (typeof value !== "string" || !value.trim()) throw new Error("Reference media must have a public HTTP or HTTPS URL");
  const source = value.trim();
  if (/^(data|blob):/i.test(source)) throw new Error("H3-KS reference media requires a public HTTP or HTTPS URL; data and blob URLs are not supported");
  const match = /^https?:\/\/([^\s/?#]+)(?:[/?#][^\s]*)?$/i.exec(source);
  if (!match || match[1].includes("@")) throw new Error("Reference media must have a public HTTP or HTTPS URL without embedded credentials");
  const host = match[1].replace(/:\d+$/, "").toLowerCase();
  if (host === "localhost" || host.endsWith(".localhost") || host === "[::1]" || /^(127\.|10\.|192\.168\.|169\.254\.|0\.)/.test(host) || /^172\.(1[6-9]|2\d|3[01])\./.test(host)) {
    throw new Error("Reference media must have a public HTTP or HTTPS URL");
  }
  return source;
}

function h3Request(req) {
  if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("A JSON object is required");
  if (typeof req.prompt !== "string" || !req.prompt.trim()) throw new Error("prompt is required");
  const metadata = req.metadata === undefined ? {} : req.metadata;
  if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) throw new Error("metadata must be an object");

  let rawSeconds = 6;
  if (req.seconds !== undefined) rawSeconds = req.seconds;
  else if (req.duration !== undefined) rawSeconds = req.duration;
  if ((typeof rawSeconds !== "number" && typeof rawSeconds !== "string") || String(rawSeconds).trim() === "") throw new Error("H3-KS seconds must be an integer from 5 to 15");
  const seconds = Number(rawSeconds);
  if (!Number.isInteger(seconds) || seconds < 5 || seconds > 15) throw new Error("H3-KS seconds must be an integer from 5 to 15");
  if (req.seconds !== undefined && req.duration !== undefined && Number(req.duration) !== seconds) throw new Error("seconds and duration must match");
  if (req.resolution !== undefined && req.resolution !== "1080p") throw new Error("H3-KS supports only resolution 1080p");
  if (req.n !== undefined && req.n !== 1 && req.n !== "1") throw new Error("H3-KS creates one video per request; n must be 1");

  const ratioFields = req.aspect_ratio !== undefined || req.ratio !== undefined ? req : metadata;
  if (ratioFields.aspect_ratio !== undefined && ratioFields.ratio !== undefined && ratioFields.aspect_ratio !== ratioFields.ratio) throw new Error("aspect_ratio and ratio must match");
  let ratio = ratioFields.aspect_ratio === undefined ? ratioFields.ratio : ratioFields.aspect_ratio;
  if (ratio === undefined) {
    const size = req.size === undefined ? metadata.size : req.size;
    if (size === undefined || size === "") ratio = "16:9";
    else {
      const dimensions = /^(\d+)x(\d+)$/i.exec(String(size));
      if (dimensions && Number(dimensions[1]) > 0 && Number(dimensions[2]) > 0) {
        if (Number(dimensions[1]) * 9 === Number(dimensions[2]) * 16) ratio = "16:9";
        else if (Number(dimensions[1]) * 16 === Number(dimensions[2]) * 9) ratio = "9:16";
      }
    }
  }
  if (ratio !== "16:9" && ratio !== "9:16") throw new Error("H3-KS supports only aspect_ratio 16:9 or 9:16");

  for (const values of [req, metadata]) {
    for (const key of ["video", "videos", "referenceVideos", "reference_videos", "last_frame", "last_frame_image"]) {
      const value = values[key];
      if (value !== undefined && value !== null && value !== "" && (!Array.isArray(value) || value.length)) throw new Error("H3-KS does not support reference videos or first/last-frame mode");
    }
    for (const key of ["task_mode", "mode"]) {
      if (["frames", "first_frame", "first_last_frame"].includes(values[key])) throw new Error("H3-KS does not support first/last-frame mode; use image references");
    }
  }
  if (req.first_frame || req.first_frame_image) throw new Error("H3-KS does not support first/last-frame mode; use image references");

  const references = [];
  let imageCount = 0;
  let audioCount = 0;
  const supplied = req.references === undefined ? metadata.references : req.references;
  if (supplied !== undefined && !Array.isArray(supplied)) throw new Error("references must be an array");
  const candidates = (supplied || []).slice();
  // Explicit references and each selected media list preserve repeated URLs and
  // their order. Canvas mirror fields are aliases, not additional materials.
  if (supplied === undefined) {
    for (const type of ["image", "audio"]) {
      const keys = type === "image" ? ["images", "referenceImages", "reference_images"] : ["audios", "referenceAudios", "reference_audios"];
      let sources;
      for (const key of keys) {
        for (const values of [req, metadata]) {
          if (values[key] !== undefined) {
            if (!Array.isArray(values[key])) throw new Error(key + " must be an array of public URLs");
            sources = values[key];
            break;
          }
        }
        if (sources !== undefined) break;
      }
      if (sources === undefined) {
        const aliases = type === "image" ? [req.image, req.input_reference, metadata.image, metadata.input_reference, metadata.first_frame, metadata.first_frame_image] : [req.audio, metadata.audio];
        const source = aliases.find(function (value) { return value !== undefined && value !== ""; });
        sources = source === undefined ? [] : [source];
      }
      for (const source of sources) candidates.push({ type, source });
    }
  }
  for (const candidate of candidates) {
    if (!candidate || typeof candidate !== "object" || Array.isArray(candidate)) throw new Error("Each reference must be an object with type and source");
    if (candidate.type !== "image" && candidate.type !== "audio") throw new Error("H3-KS supports only image and audio references");
    if (candidate.role !== undefined && candidate.role !== "reference") throw new Error("H3-KS supports only role reference, without first/last-frame mode");
    const source = referenceURL(candidate.source);
    if (candidate.type === "image") imageCount++;
    else audioCount++;
    if (imageCount > 6 || audioCount > 2) throw new Error("H3-KS supports at most 6 image references and 2 audio references");
    references.push({ type: candidate.type, role: "reference", source });
  }
  const body = { model: req.model, prompt: req.prompt, seconds, resolution: "1080p", aspect_ratio: ratio };
  if (references.length) body.references = references;
  return body;
}

export const protocols = {
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
      let req = ctx.body.value;
      if (ctx.body.kind === "multipart") {
        if ((ctx.body.files || []).length) throw new Error("H3-KS reference media requires public URLs; upload files before creating the video");
        req = {};
        const fields = ctx.body.fields || {};
        for (const name of Object.keys(fields)) {
          if (fields[name].length !== 1) throw new Error(name + " must be provided once");
          let value = fields[name][0];
          if (["metadata", "references", "images", "referenceImages", "reference_images", "audios", "referenceAudios", "reference_audios", "videos", "referenceVideos", "reference_videos"].includes(name)) {
            try { value = JSON.parse(value); } catch (e) { throw new Error(name + " must contain valid JSON"); }
          }
          req[name] = value;
        }
      }
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("A JSON object is required");
      const body = h3Request(Object.assign({}, req, { model: ctx.model }));
      return { kind: "submit", model: ctx.model, action: body.references ? "reference_to_video" : "text_to_video", requestBody: body };
    },
    render: function (_ctx, task) {
      return task.data && typeof task.data === "object" && !Array.isArray(task.data) ? task.data : {};
    },
  },
};

export function buildSubmitRequest(ctx) {
  if (typeof ctx.publicTaskId !== "string" || !ctx.publicTaskId) throw new Error("A stable public task ID is required for video submission");
  const body = h3Request(Object.assign({}, ctx.requestBody, { model: ctx.upstreamModel || ctx.model }));
  return {
    url: ctx.baseUrl + "/v1/videos",
    method: "POST",
    headers: { Authorization: "Bearer " + ctx.apiKey, "Content-Type": "application/json", "Idempotency-Key": ctx.publicTaskId },
    body,
  };
}

export function parseSubmitResponse(_ctx, response) {
  const body = response.body || {};
  const taskId = body.task_id || body.id;
  if (typeof taskId !== "string" || !taskId.trim()) throw new Error("Video submission returned no task ID");
  return { taskId, taskData: body };
}

export function buildQueryRequest(ctx) {
  return { url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId), method: "GET", headers: { Authorization: "Bearer " + ctx.apiKey } };
}

export function parseTaskResult(_ctx, body) {
  if (!body || typeof body !== "object" || Array.isArray(body)) body = {};
  const states = { queued: "QUEUED", in_progress: "IN_PROGRESS", completed: "SUCCESS", failed: "FAILURE" };
  const status = states[body.status];
  const result = { status: status || "UNKNOWN" };
  if (!status) result.reason = "Unrecognized video task status";
  if (typeof body.progress === "number" && Number.isFinite(body.progress) && body.progress >= 0 && body.progress <= 100) result.progress = body.progress + "%";
  if (status === "FAILURE") result.reason = body.error && body.error.message ? body.error.message : "Video generation failed";
  return result;
}

export function extractUsage(_ctx) {
  return { video_count: 1 };
}

export function extractUsageOnComplete(_ctx, _result, _body) {
  return { video_count: 1 };
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" ? [{ key: "video", type: "video", mimeType: "video/mp4" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.upstreamTaskId) + "/content?download=1",
    method: ctx.clientRequest.method,
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}
