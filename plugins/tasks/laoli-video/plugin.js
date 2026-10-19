const H3_MODEL = "H3-KS";
const CLASSIC_20 = "特价2.0-需要过人脸技术-可参考过人脸素材库";
const CLASSIC_25 = "特价2.5需要过人脸技术-可参考过人脸素材库";
const FULL_20 = "原生不卡人脸-全参2.0";
const FULL_25 = "原生不卡人脸-全参2.5";
const WAN_VIDEO = "WAN3.0-可参考视频";
const WAN_NO_VIDEO = "WAN3.0-不可参考视频";

const COUNT_SCHEMA = {
  video_count: {
    type: "number",
    unit: "count",
    unitLabel: { en: "video", zh: "条" },
    description: { en: "Video generation unit price", zh: "视频生成单价" },
  },
};

function meteredVideoSchema(resolutions) {
  return {
    seconds: { type: "number", unit: "second", description: { en: "Video generation unit price", zh: "视频生成单价" } },
    resolution: { enum: resolutions, description: { en: "Output video resolution", zh: "输出视频分辨率" } },
  };
}

const MODEL_SPECS = {
  [H3_MODEL]: { min: 5, max: 15, defaultSeconds: 6, resolutions: ["1080p"], defaultResolution: "1080p", images: 6, videos: 0, audios: 2, total: 8, prompt: 6000, h3: true },
  [CLASSIC_20]: { min: 5, max: 15, durations: [5, 10, 15], defaultSeconds: 5, resolutions: ["720p"], defaultResolution: "720p", images: 9, videos: 0, audios: 0, total: 9, prompt: 15000, classic: true },
  [CLASSIC_25]: { min: 16, max: 30, defaultSeconds: 16, resolutions: ["720p"], defaultResolution: "720p", images: 30, videos: 0, audios: 0, total: 30, prompt: 15000, classic: true },
  [FULL_20]: { min: 4, max: 15, defaultSeconds: 6, resolutions: ["480p", "720p", "1080p"], defaultResolution: "720p", images: 9, videos: 3, audios: 3, total: 12, prompt: 4000, mediaMin: 2, mediaMax: 15, frames: true, metered: true },
  [FULL_25]: { min: 4, max: 30, defaultSeconds: 6, resolutions: ["480p", "720p"], defaultResolution: "720p", images: 30, videos: 10, audios: 10, total: 50, prompt: 4000, mediaMin: 2, mediaMax: 30, metered: true },
  [WAN_VIDEO]: { min: 4, max: 15, defaultSeconds: 6, resolutions: ["720p"], defaultResolution: "720p", images: 10, videos: 5, audios: 5, total: 20, metered: true },
  [WAN_NO_VIDEO]: { min: 4, max: 30, defaultSeconds: 6, resolutions: ["720p"], defaultResolution: "720p", images: 10, videos: 0, audios: 5, total: 10, metered: true },
};

export const meta = {
  apiVersion: 1,
  key: "laoli-video",
  name: "Laoli Video",
  icon: "text:H3",
  version: "1.1.0",
  author: { name: "QuantumNous" },
  description: { en: "Video generation via Laoli", zh: "通过老李生成视频" },
  baseUrl: "https://video.laoliimage2.win",
  website: "https://video.laoliimage2.win/docs/video-api/",
  models: Object.keys(MODEL_SPECS),
  fetchMode: "per_task",
  protocols: ["openai_video"],
  usageSchema: COUNT_SCHEMA,
  usageProfiles: [
    { models: [H3_MODEL, CLASSIC_20, CLASSIC_25], schema: COUNT_SCHEMA },
    { models: [FULL_20], schema: meteredVideoSchema(MODEL_SPECS[FULL_20].resolutions) },
    { models: [FULL_25], schema: meteredVideoSchema(MODEL_SPECS[FULL_25].resolutions) },
    { models: [WAN_VIDEO, WAN_NO_VIDEO], schema: meteredVideoSchema(["720p"]) },
  ],
};

function declaredVideoModel(ctx) {
  for (const name of [ctx.upstreamModel, ctx.model]) {
    if (Object.prototype.hasOwnProperty.call(MODEL_SPECS, name)) return name;
  }
  throw new Error("The selected model is not supported by Laoli Video");
}

function validSeconds(value, spec) {
  if ((typeof value !== "number" && typeof value !== "string") || String(value).trim() === "") return false;
  const seconds = Number(value);
  return Number.isInteger(seconds) && seconds >= spec.min && seconds <= spec.max && (!spec.durations || spec.durations.includes(seconds));
}

function referenceURL(value) {
  if (typeof value !== "string" || !value.trim()) throw new Error("Reference media must have a public HTTP or HTTPS URL");
  const source = value.trim();
  if (/^(data|blob):/i.test(source)) throw new Error("This model requires public HTTP or HTTPS reference URLs; data and blob URLs are not supported");
  const match = /^https?:\/\/([^\s/?#]+)(?:[/?#][^\s]*)?$/i.exec(source);
  if (!match || match[1].includes("@")) throw new Error("Reference media must have a public HTTP or HTTPS URL without embedded credentials");
  const host = match[1].replace(/:\d+$/, "").toLowerCase();
  if (host === "localhost" || host.endsWith(".localhost") || host === "[::1]" || /^(127\.|10\.|192\.168\.|169\.254\.|0\.)/.test(host) || /^172\.(1[6-9]|2\d|3[01])\./.test(host)) throw new Error("Reference media must have a public HTTP or HTTPS URL");
  return source;
}

function mediaSources(options, type) {
  const arrayKeys = type === "image" ? ["images", "referenceImages", "reference_images"] : type === "video" ? ["videos", "referenceVideos", "reference_videos"] : ["audios", "referenceAudios", "reference_audios"];
  for (const key of arrayKeys) {
    if (options[key] !== undefined) {
      if (!Array.isArray(options[key])) throw new Error(key + " must be an array");
      return options[key];
    }
  }
  const aliases = type === "image" ? [options.image, options.input_reference, options.first_frame, options.first_frame_image] : [options[type]];
  const source = aliases.find(function (value) { return value !== undefined && value !== ""; });
  return source === undefined ? [] : [source];
}

function videoReferences(options, spec, modelName) {
  if (options.materials !== undefined) throw new Error("Use references with public URLs for this integration");
  if (!spec.videos && mediaSources(options, "video").length) throw new Error(modelName + " does not support reference videos");
  if (!spec.audios && mediaSources(options, "audio").length) throw new Error(modelName + " does not support reference audio");
  let candidates = options.references;
  if (candidates !== undefined && !Array.isArray(candidates)) throw new Error("references must be an array");
  if (candidates === undefined) {
    candidates = [];
    for (const type of ["image", "video", "audio"]) {
      for (const source of mediaSources(options, type)) candidates.push({ type, source });
    }
  }
  const references = [];
  const counts = { image: 0, video: 0, audio: 0 };
  let firstFrames = 0;
  let lastFrames = 0;
  for (const candidate of candidates) {
    if (!candidate || typeof candidate !== "object" || Array.isArray(candidate)) throw new Error("Each reference must be an object with type and source");
    if (!["image", "video", "audio"].includes(candidate.type) || (candidate.type === "video" && !spec.videos) || (candidate.type === "audio" && !spec.audios)) {
      if (spec.h3) throw new Error("H3-KS supports only image and audio references");
      throw new Error(modelName + " does not support this reference media type");
    }
    const role = candidate.role === undefined ? "reference" : candidate.role;
    if (role !== "reference") {
      if (!spec.frames || candidate.type !== "image" || (role !== "first_frame" && role !== "last_frame")) throw new Error(modelName + " supports only role reference, without first/last-frame mode");
      if (role === "first_frame") firstFrames++;
      else lastFrames++;
    }
    const reference = { type: candidate.type, role, source: referenceURL(candidate.source) };
    if (candidate.type !== "image" && (candidate.duration_seconds !== undefined || spec.mediaMin)) {
      const duration = candidate.duration_seconds;
      if ((typeof duration !== "number" && typeof duration !== "string") || String(duration).trim() === "" || !Number.isFinite(Number(duration)) || Number(duration) < (spec.mediaMin || 0) || Number(duration) <= 0 || Number(duration) > (spec.mediaMax || 3600)) throw new Error("Reference audio/video duration_seconds must match the model's supported media duration");
      reference.duration_seconds = Number(duration);
    }
    counts[candidate.type]++;
    references.push(reference);
  }
  if (firstFrames || lastFrames) {
    if (firstFrames !== 1 || lastFrames > 1 || references.length !== firstFrames + lastFrames) throw new Error("First/last-frame mode requires one first frame and at most one last frame, without other reference media");
  }
  if (counts.image > spec.images || counts.video > spec.videos || counts.audio > spec.audios || references.length > spec.total) {
    if (spec.h3) throw new Error("H3-KS supports at most 6 image references and 2 audio references");
    throw new Error(modelName + " reference limits: " + spec.images + " images, " + spec.videos + " videos, " + spec.audios + " audio clips, " + spec.total + " materials in total");
  }
  return references;
}

function classicImages(options, spec, modelName) {
  if (options.references !== undefined || options.materials !== undefined) throw new Error("Special-price models require Base64 or Data URL images, rather than references");
  if (mediaSources(options, "video").length || mediaSources(options, "audio").length) throw new Error("Special-price models support only image references");
  for (const key of ["face_direct", "native_face"]) {
    if (options[key] !== undefined && options[key] !== false) throw new Error(key + " is disabled for this model");
  }
  const images = mediaSources(options, "image");
  if (images.length > spec.images) throw new Error(modelName + " supports at most " + spec.images + " images");
  let totalBytes = 0;
  for (const image of images) {
    if (typeof image !== "string") throw new Error("Special-price images must be Base64 strings or image Data URLs");
    const data = /^data:image\/(?:png|jpeg|webp);base64,([A-Za-z0-9+/]+={0,2})$/i.exec(image);
    const encoded = data ? data[1] : image;
    if (!/^[A-Za-z0-9+/]+={0,2}$/.test(encoded) || encoded.length % 4 !== 0) throw new Error("Special-price images must be Base64 strings or image Data URLs; public URLs require conversion before submission");
    const bytes = encoded.length / 4 * 3 - (encoded.endsWith("==") ? 2 : encoded.endsWith("=") ? 1 : 0);
    if (bytes > 50 * 1024 * 1024) throw new Error("Each special-price image must be at most 50 MiB");
    totalBytes += bytes;
  }
  if (totalBytes > 70 * 1024 * 1024) throw new Error("Special-price images must total at most 70 MiB");
  return images;
}

function videoRequest(req, modelName) {
  const spec = MODEL_SPECS[modelName];
  if (!spec) throw new Error("The selected model is not supported by Laoli Video");
  if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("A JSON object is required");
  if (typeof req.prompt !== "string" || !req.prompt.trim()) throw new Error("prompt is required");
  if (spec.prompt && Array.from(req.prompt).length > spec.prompt) throw new Error(modelName + " prompt must be at most " + spec.prompt + " characters");
  const metadata = req.metadata === undefined ? {} : req.metadata;
  const params = req.params === undefined ? {} : req.params;
  if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) throw new Error("metadata must be an object");
  if (!params || typeof params !== "object" || Array.isArray(params)) throw new Error("params must be an object");
  if ((modelName === WAN_VIDEO || modelName === WAN_NO_VIDEO) && metadata.payload !== undefined) throw new Error("Use canonical references instead of WAN metadata.payload for this integration");
  const options = Object.assign({}, metadata, params, req);

  let rawSeconds = spec.defaultSeconds;
  if (options.seconds !== undefined) rawSeconds = options.seconds;
  else if (options.duration !== undefined) rawSeconds = options.duration;
  if (!validSeconds(rawSeconds, spec)) throw new Error(modelName + " seconds must be " + (spec.durations ? "one of " + spec.durations.join(", ") : "an integer from " + spec.min + " to " + spec.max));
  const seconds = Number(rawSeconds);
  if (options.seconds !== undefined && options.duration !== undefined && Number(options.duration) !== seconds) throw new Error("seconds and duration must match");
  if (options.n !== undefined && options.n !== 1 && options.n !== "1") throw new Error("This model creates one video per request; n must be 1");
  let resolution = spec.defaultResolution;
  // H3's old canvas metadata carries a generic 720p default, not an explicit choice.
  if (req.resolution !== undefined) resolution = req.resolution;
  else if (params.resolution !== undefined) resolution = params.resolution;
  else if (!spec.h3 && metadata.resolution !== undefined) resolution = metadata.resolution;
  if (typeof resolution === "string") resolution = resolution.toLowerCase();
  if (!spec.resolutions.includes(resolution)) throw new Error(modelName + " supports only resolution " + spec.resolutions.join(", "));

  let ratioFields = metadata;
  if (params.aspect_ratio !== undefined || params.ratio !== undefined) ratioFields = params;
  if (req.aspect_ratio !== undefined || req.ratio !== undefined) ratioFields = req;
  if (ratioFields.aspect_ratio !== undefined && ratioFields.ratio !== undefined && ratioFields.aspect_ratio !== ratioFields.ratio) throw new Error("aspect_ratio and ratio must match");
  let ratio = ratioFields.aspect_ratio === undefined ? ratioFields.ratio : ratioFields.aspect_ratio;
  if (ratio === undefined) {
    const size = options.size;
    if (size === undefined || size === "") ratio = "16:9";
    else {
      const dimensions = /^(\d+)x(\d+)$/i.exec(String(size));
      if (dimensions) {
        const width = Number(dimensions[1]), height = Number(dimensions[2]);
        let divisor = width, remainder = height;
        while (remainder) { const next = divisor % remainder; divisor = remainder; remainder = next; }
        if (Number.isSafeInteger(width) && Number.isSafeInteger(height) && width > 0 && height > 0) ratio = width / divisor + ":" + height / divisor;
      }
    }
  }
  if (spec.h3 && ratio !== "16:9" && ratio !== "9:16") throw new Error("H3-KS supports only aspect_ratio 16:9 or 9:16");
  if (typeof ratio !== "string" || !ratio.trim() || ratio.length > 64 || /[\s\x00-\x1f]/.test(ratio)) throw new Error("aspect_ratio must be a non-empty model-supported ratio");

  const mode = options.task_mode === undefined ? options.mode : options.task_mode;
  const frameMode = ["frames", "first_frame", "first_last_frame"].includes(mode) || !!(spec.frames && (req.first_frame || req.first_frame_image || req.last_frame || req.last_frame_image));
  if (frameMode && !spec.frames) throw new Error(modelName + " does not support first/last-frame mode; use image references");
  if (!spec.frames && (req.first_frame || req.first_frame_image || options.last_frame || options.last_frame_image)) throw new Error(modelName + " does not support reference videos or first/last-frame mode");
  if (spec.classic) {
    const body = { model: req.model, prompt: req.prompt, duration: seconds, resolution, ratio, images: classicImages(options, spec, modelName) };
    for (const key of ["face_direct", "native_face"]) { if (options[key] !== undefined) body[key] = options[key]; }
    return body;
  }

  if (spec.frames && frameMode && options.references === undefined) {
    const first = options.first_frame || options.first_frame_image || options.input_reference;
    const last = options.last_frame || options.last_frame_image;
    const references = [];
    if (first) references.push({ type: "image", role: "first_frame", source: first });
    if (last) references.push({ type: "image", role: "last_frame", source: last });
    if (mediaSources(options, "audio").length || mediaSources(options, "video").length || (options.images && options.images.length)) throw new Error("First/last-frame mode cannot mix other reference media");
    options.references = references;
  }
  const references = videoReferences(options, spec, modelName);
  const hasFrames = references.some(function (reference) { return reference.role !== "reference"; });
  if (frameMode && !hasFrames) throw new Error("First/last-frame mode requires explicit frame references");
  if ((mode === "first_frame" && references.length !== 1) || (mode === "first_last_frame" && references.length !== 2)) throw new Error("task_mode does not match the frame references");
  if ((mode === "text" && references.length) || (mode === "references" && hasFrames)) throw new Error("task_mode does not match the references");
  const body = { model: req.model, prompt: req.prompt, seconds, resolution, aspect_ratio: ratio };
  if (references.length) body.references = references;
  if (mode !== undefined && ["text", "references", "first_frame", "first_last_frame"].includes(mode)) body.task_mode = mode;
  else if (frameMode) body.task_mode = references.length === 2 ? "first_last_frame" : "first_frame";
  return body;
}

export const protocols = {
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
      let req = ctx.body.value;
      if (ctx.body.kind === "multipart") {
        if ((ctx.body.files || []).length) throw new Error("This integration requires public URLs or inline Base64 images; upload files before creating the video");
        req = {};
        for (const name of Object.keys(ctx.body.fields || {})) {
          const fields = ctx.body.fields[name];
          if (fields.length !== 1) throw new Error(name + " must be provided once");
          let value = fields[0];
          if (["metadata", "params", "references", "materials", "images", "referenceImages", "reference_images", "audios", "referenceAudios", "reference_audios", "videos", "referenceVideos", "reference_videos", "face_direct", "native_face"].includes(name)) {
            try { value = JSON.parse(value); } catch (e) { throw new Error(name + " must contain valid JSON"); }
          }
          req[name] = value;
        }
      }
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("A JSON object is required");
      const body = videoRequest(Object.assign({}, req, { model: ctx.model }), declaredVideoModel(ctx));
      const hasReferences = (body.references && body.references.length) || (body.images && body.images.length);
      return { kind: "submit", model: ctx.model, action: hasReferences ? "reference_to_video" : "text_to_video", requestBody: body };
    },
    render: function (_ctx, task) { return task.data && typeof task.data === "object" && !Array.isArray(task.data) ? task.data : {}; },
  },
};

export function buildSubmitRequest(ctx) {
  if (typeof ctx.publicTaskId !== "string" || !ctx.publicTaskId) throw new Error("A stable public task ID is required for video submission");
  const body = videoRequest(Object.assign({}, ctx.requestBody, { model: ctx.upstreamModel || ctx.model }), declaredVideoModel(ctx));
  return { url: ctx.baseUrl + "/v1/videos", method: "POST", headers: { Authorization: "Bearer " + ctx.apiKey, "Content-Type": "application/json", "Idempotency-Key": ctx.publicTaskId }, body };
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

export function extractUsage(ctx) {
  const modelName = declaredVideoModel(ctx);
  const spec = MODEL_SPECS[modelName];
  if (!spec.metered) return { video_count: 1 };
  const request = videoRequest(Object.assign({}, ctx.requestBody, { model: ctx.model }), modelName);
  return { seconds: request.seconds, resolution: request.resolution };
}

export function extractUsageOnComplete(ctx, _result, body) {
  const spec = MODEL_SPECS[declaredVideoModel(ctx)];
  if (!spec.metered) return { video_count: 1 };
  if (!body || typeof body !== "object" || Array.isArray(body)) return {};
  const seconds = body.seconds === undefined ? body.duration : body.seconds;
  // Missing/invalid upstream facts do not overwrite the frozen reservation.
  // Resolution stays at the requested tier, even when the provider echoes another one.
  return validSeconds(seconds, spec) ? { seconds: Number(seconds) } : {};
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" ? [{ key: "video", type: "video", mimeType: "video/mp4" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  return { url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.upstreamTaskId) + "/content?download=1", method: ctx.clientRequest.method, headers: { Authorization: "Bearer " + ctx.apiKey } };
}
