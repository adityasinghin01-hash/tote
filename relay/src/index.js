// tote hosted mailbox — a Cloudflare Worker in front of one R2 bucket.
//
// It only ever sees locked boxes (age-encrypted on the sender's machine), so
// it cannot read anything. Its jobs: accept a box in ≤90 MB parts (the free
// plan caps request bodies at 100 MB), hand back a read link, stream the box
// to the receiver, and forget it after 24 h or a few downloads.
//
//   POST   /v1/boxes                  {size}           → {id, put_token, read_token, part_size, parts, expires}
//   PUT    /v1/boxes/:id/:n           Bearer put_token → 204
//   POST   /v1/boxes/:id/seal         Bearer put_token → {get, del, expires}
//   GET    /v1/boxes/:id?t=read_token                  → the box bytes
//   DELETE /v1/boxes/:id?t=read_token                  → 204

const PART = 90 * 1024 * 1024;
const MAX_SIZE = 4 * PART; // 360 MB — bigger boxes belong in the user's own bucket
const TTL_MS = 24 * 60 * 60 * 1000;
const MAX_DOWNLOADS = 5;

export default {
  async fetch(req, env, ctx) {
    try {
      return await route(req, env, ctx);
    } catch (e) {
      return json({ error: "server error" }, 500);
    }
  },
  // Cron: sweep expired boxes (R2 lifecycle rules are the backstop).
  async scheduled(_event, env) {
    let cursor;
    do {
      const page = await env.BOX.list({ prefix: "meta/", cursor });
      for (const o of page.objects) {
        const meta = await readMeta(env, o.key.slice(5));
        if (!meta || Date.now() > meta.expires) await wipe(env, o.key.slice(5), meta);
      }
      cursor = page.truncated ? page.cursor : undefined;
    } while (cursor);
  },
};

async function route(req, env, ctx) {
  const url = new URL(req.url);
  const p = url.pathname.split("/").filter(Boolean); // ["v1","boxes",id,n]
  if (p[0] !== "v1" || p[1] !== "boxes") return json({ error: "not found" }, 404);

  if (p.length === 2 && req.method === "POST") return create(req, env);
  const id = p[2];
  if (!/^[a-f0-9]{24}$/.test(id || "")) return json({ error: "not found" }, 404);
  const meta = await readMeta(env, id);
  if (!meta || Date.now() > meta.expires) {
    if (meta) ctx.waitUntil(wipe(env, id, meta));
    return json({ error: "gone" }, 404);
  }

  if (p.length === 4 && p[3] === "seal" && req.method === "POST") {
    if (!(await authed(req.headers.get("authorization"), meta.put))) return json({ error: "forbidden" }, 403);
    return seal(url, env, id, meta);
  }
  if (p.length === 4 && req.method === "PUT") {
    if (!(await authed(req.headers.get("authorization"), meta.put))) return json({ error: "forbidden" }, 403);
    return putPart(req, env, id, meta, p[3]);
  }
  if (p.length === 3 && (req.method === "GET" || req.method === "DELETE")) {
    if (!(await authed("Bearer " + (url.searchParams.get("t") || ""), meta.read))) return json({ error: "forbidden" }, 403);
    if (!meta.sealed) return json({ error: "gone" }, 404);
    if (req.method === "DELETE") {
      await wipe(env, id, meta);
      return new Response(null, { status: 204 });
    }
    return download(env, ctx, id, meta);
  }
  return json({ error: "not found" }, 404);
}

async function create(req, env) {
  let body;
  try {
    body = await req.json();
  } catch {
    return json({ error: "bad request" }, 400);
  }
  const size = Number(body && body.size);
  if (!Number.isSafeInteger(size) || size <= 0) return json({ error: "bad size" }, 400);
  if (size > MAX_SIZE)
    return json({ error: `box is over ${MAX_SIZE / 1024 / 1024} MB — use your own bucket (tote mailbox use s3)` }, 413);
  const id = hex(12);
  const putToken = hex(32);
  const readToken = hex(32);
  const meta = {
    size,
    parts: Math.ceil(size / PART),
    put: await sha(putToken),
    read: await sha(readToken),
    expires: Date.now() + TTL_MS,
    sealed: false,
    downloads: 0,
  };
  await writeMeta(env, id, meta);
  return json({ id, put_token: putToken, read_token: readToken, part_size: PART, parts: meta.parts, expires: meta.expires }, 201);
}

async function putPart(req, env, id, meta, nStr) {
  if (meta.sealed) return json({ error: "already sealed" }, 409);
  const n = Number(nStr);
  if (!Number.isInteger(n) || n < 0 || n >= meta.parts) return json({ error: "bad part" }, 400);
  const want = n === meta.parts - 1 ? meta.size - PART * (meta.parts - 1) : PART;
  if (Number(req.headers.get("content-length")) !== want) return json({ error: `part ${n} must be ${want} bytes` }, 400);
  const obj = await env.BOX.put(`data/${id}/${n}`, req.body);
  if (!obj || obj.size !== want) {
    await env.BOX.delete(`data/${id}/${n}`);
    return json({ error: "part arrived incomplete" }, 400);
  }
  return new Response(null, { status: 204 });
}

async function seal(url, env, id, meta) {
  for (let n = 0; n < meta.parts; n++) {
    if (!(await env.BOX.head(`data/${id}/${n}`))) return json({ error: `part ${n} missing` }, 409);
  }
  meta.sealed = true;
  await writeMeta(env, id, meta);
  // The read token is not stored in clear, so the client fills it in.
  const link = `${url.origin}/v1/boxes/${id}?t=`;
  return json({ get: link, del: link, expires: meta.expires });
}

async function download(env, ctx, id, meta) {
  if (meta.downloads >= MAX_DOWNLOADS) return json({ error: "gone" }, 404);
  meta.downloads++;
  await writeMeta(env, id, meta);
  const { readable, writable } = new FixedLengthStream(meta.size);
  ctx.waitUntil(
    (async () => {
      for (let n = 0; n < meta.parts; n++) {
        const o = await env.BOX.get(`data/${id}/${n}`);
        if (!o) {
          await writable.abort("part missing");
          return;
        }
        await o.body.pipeTo(writable, { preventClose: true });
      }
      await writable.close();
    })(),
  );
  return new Response(readable, {
    headers: { "content-type": "application/octet-stream", "cache-control": "no-store" },
  });
}

async function wipe(env, id, meta) {
  const parts = meta ? meta.parts : 4;
  const keys = [`meta/${id}`];
  for (let n = 0; n < parts; n++) keys.push(`data/${id}/${n}`);
  await env.BOX.delete(keys);
}

async function readMeta(env, id) {
  const o = await env.BOX.get(`meta/${id}`);
  return o ? o.json() : null;
}

function writeMeta(env, id, meta) {
  return env.BOX.put(`meta/${id}`, JSON.stringify(meta));
}

async function authed(header, wantHash) {
  const m = /^Bearer ([a-f0-9]{64})$/.exec(header || "");
  if (!m) return false;
  return timingSafe(await sha(m[1]), wantHash);
}

function timingSafe(a, b) {
  if (a.length !== b.length) return false;
  let d = 0;
  for (let i = 0; i < a.length; i++) d |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return d === 0;
}

async function sha(s) {
  const d = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function hex(nBytes) {
  const b = crypto.getRandomValues(new Uint8Array(nBytes));
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}

function json(obj, status = 200) {
  return new Response(JSON.stringify(obj), {
    status,
    headers: { "content-type": "application/json", "cache-control": "no-store" },
  });
}
