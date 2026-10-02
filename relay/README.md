# tote hosted mailbox (Cloudflare Worker + R2)

The free public mailbox `tote send` uses when you haven't set up your own.
It only ever stores locked boxes — it cannot read them.

Limits: 360 MB per box (sent in 90 MB parts), deleted 24 h after creation or
after 5 downloads, deleted right after a successful `tote get`.

## Run locally (no account needed)

```sh
npm install
npx wrangler dev --local          # serves http://localhost:8787
# in another terminal, from the repo root:
TOTE_RELAY_URL=http://localhost:8787 go test ./internal/mailbox/
```

## Deploy your own

1. Free Cloudflare account → `npx wrangler login`
2. `npx wrangler r2 bucket create tote-relay`
3. Backstop expiry: `npx wrangler r2 bucket lifecycle add tote-relay expire-2d --expire-days 2`
4. `npx wrangler deploy` → note the `https://tote-relay.<you>.workers.dev` URL
5. Point tote at it: `tote mailbox use hosted https://tote-relay.<you>.workers.dev`
6. Abuse guard (dashboard → Security → WAF → Rate limiting rules): e.g. 20
   `POST /v1/boxes` per IP per 10 minutes.

## Protocol

| Call | Auth | Result |
|---|---|---|
| `POST /v1/boxes` `{size}` | — | `{id, put_token, read_token, part_size, parts, expires}` |
| `PUT /v1/boxes/:id/:n` | `Bearer put_token` | 204; each part exactly `part_size` bytes except the last |
| `POST /v1/boxes/:id/seal` | `Bearer put_token` | `{get, del, expires}` — client appends `read_token` |
| `GET /v1/boxes/:id?t=read_token` | read token | the box bytes |
| `DELETE /v1/boxes/:id?t=read_token` | read token | 204 |

Tokens are stored only as SHA-256 hashes.
