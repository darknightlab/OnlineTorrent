import { test } from "node:test"
import assert from "node:assert/strict"
import { releaseTags } from "./release-tags.ts"

test("audio codec tags next to the video codec", () => {
  const cases: [string, string[]][] = [
    ["Cyberpunk Edgerunners S02 MULTi AD 1080p NF WEB-DL DDP5.1 H.265-Tsundere-Raws", ["HEVC", "DDP"]],
    ["[LoliHouse] Frieren - 03 [WebRip 1080p HEVC-10bit AAC ASSx2]", ["HEVC", "AAC"]],
    ["X.1080p.WEB-DL.AAC2.0.H.264", ["AVC", "AAC"]],
    ["X [BDRip 1080p HEVC FLAC]", ["HEVC", "FLAC"]],
    ["X 1080p BluRay DTS-HD MA 5.1 AVC", ["AVC", "DTS"]],
    ["X.2160p.BluRay.TrueHD.7.1.Atmos.x265", ["HEVC", "TrueHD"]],
    ["X.1080p.WEB-DL.DD5.1.H264", ["AVC", "AC3"]],
    ["[DDD] Show 01 [1080p]", []],
  ]
  for (const [text, want] of cases) {
    const got = releaseTags(text).filter((t) => ["HEVC", "AVC", "AAC", "FLAC", "Opus", "DDP", "AC3", "DTS", "TrueHD"].includes(t))
    assert.deepEqual(got, want, text)
  }
})
