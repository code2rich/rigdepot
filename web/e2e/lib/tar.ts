// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// A tar archive of a few small text files, for uploading a resource's directory: the ustar format is a
// 512-byte header per file, the content padded to 512 bytes, and two empty blocks at the end.

const enc = new TextEncoder()

function header(name: string, size: number): Uint8Array {
  const h = new Uint8Array(512)
  const put = (at: number, text: string) => h.set(enc.encode(text), at)
  const octal = (n: number, width: number) => n.toString(8).padStart(width - 1, '0') + '\0'
  put(0, name)
  put(100, octal(0o644, 8))
  put(108, octal(0, 8))
  put(116, octal(0, 8))
  put(124, octal(size, 12))
  put(136, octal(Math.floor(Date.UTC(2026, 0, 1) / 1000), 12))
  put(148, '        ') // the checksum counts its own field as spaces
  put(156, '0')
  put(257, 'ustar\0')
  put(263, '00')
  const sum = h.reduce((a, b) => a + b, 0)
  put(148, sum.toString(8).padStart(6, '0') + '\0 ')
  return h
}

/** tar packs files (path -> text) into a tar archive. */
export function tar(files: Readonly<Record<string, string>>): Uint8Array {
  const parts: Uint8Array[] = []
  for (const [name, text] of Object.entries(files)) {
    const body = enc.encode(text)
    parts.push(header(name, body.length), body, new Uint8Array((512 - (body.length % 512)) % 512))
  }
  parts.push(new Uint8Array(1024))
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0))
  let at = 0
  for (const p of parts) {
    out.set(p, at)
    at += p.length
  }
  return out
}
