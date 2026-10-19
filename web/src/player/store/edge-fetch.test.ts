import { test } from "node:test"
import assert from "node:assert/strict"
import { edgeTokenFor, installEdgeFetch, setEdgeToken } from "./internals.ts"

test("edge fetch wrapper: Authorization only for registered edge URLs, refreshed tokens, no cookies", async () => {
  const calls: { url: string; auth: string | null; credentials?: RequestInit["credentials"]; cache?: RequestInit["cache"]; range: string | null }[] = []
  const g = {
    fetch: (async (input: Request | string | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url
      const h = new Headers(init?.headers)
      calls.push({ url, auth: h.get("authorization"), credentials: init?.credentials, cache: init?.cache, range: h.get("range") })
      return new Response("")
    }) as typeof fetch,
  }
  installEdgeFetch(g)
  const wrapped = g.fetch
  installEdgeFetch(g) // idempotent
  assert.equal(g.fetch, wrapped)

  const E = "https://edge.example/webseed/hash/ab/"
  setEdgeToken(E, "t1")
  // what webtorrent's webconn sends: plain init object with headers
  await g.fetch(E + "T/a.mkv", { cache: "no-store", method: "GET", headers: { "Cache-Control": "no-store", range: "bytes=0-9" } })
  await g.fetch("https://www.example/webseed/hash/ab/T/a.mkv", { headers: { range: "bytes=0-9" } })
  setEdgeToken(E, "t2") // refreshed over the SSE: same URL, new token
  await g.fetch(new URL(E + "T/b.srt"))
  setEdgeToken(E, null)
  await g.fetch(E + "T/a.mkv")
  assert.deepEqual(
    calls.map((c) => [c.url.replace("https://", ""), c.auth, c.range]),
    [
      ["edge.example/webseed/hash/ab/T/a.mkv", "Bearer t1", "bytes=0-9"],
      ["www.example/webseed/hash/ab/T/a.mkv", null, "bytes=0-9"],
      ["edge.example/webseed/hash/ab/T/b.srt", "Bearer t2", null],
      ["edge.example/webseed/hash/ab/T/a.mkv", null, null],
    ],
  )
  // credentials stay default (never 'include': no cookies cross-origin)
  assert.ok(calls.every((c) => c.credentials === undefined))
  // edge requests drop cache: "no-store" (Chromium then skips the preflight cache); others untouched
  assert.equal(calls[0].cache, "default")
  await g.fetch("https://www.example/x", { cache: "no-store" })
  assert.equal(calls.at(-1)?.cache, "no-store")
  // longest prefix wins; another torrent on the same edge has its own token
  setEdgeToken("https://edge.example/webseed/hash/ab/", "a")
  setEdgeToken("https://edge.example/webseed/hash/cd/", "c")
  assert.equal(edgeTokenFor("https://edge.example/webseed/hash/cd/x"), "c")
  assert.equal(edgeTokenFor("https://edge.example/webseed/hash/ef/x"), null)
  setEdgeToken("https://edge.example/webseed/hash/ab/", null)
  setEdgeToken("https://edge.example/webseed/hash/cd/", null)
})
