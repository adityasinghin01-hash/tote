# tote box format — v1

A **box** is everything one AI needs to pick up where another left off, packed
into a single locked file. This document is the contract: any AI and any
version of tote must read and write boxes exactly this way.

## 1. The file

```
box file = age( gzip( tar( payload ) ) )
```

- **tar** — POSIX/PAX tar. Paths use `/`, are relative, never contain `..`,
  never start with `/`. Regular files and directories only (symlinks are
  skipped when packing and rejected when opening).
- **gzip** — standard gzip.
- **age** — [age v1](https://age-encryption.org) with a passphrase (scrypt)
  recipient. The passphrase is `<key>:<pin>` (see §4).

File extension: `.tote`.

## 2. Payload layout

```
manifest.json        always the LAST tar entry
START-HERE.md        read first, always (~2K tokens)
INDEX.md             one line per item in the box (~5K tokens)
DONE.md              things already done — do NOT redo them
digests/             one squeezed summary per chat (read on demand)
tools/<tool>/...     the tool's real files, word for word (memory, skills, settings)
raw/<tool>/...       raw chat logs, for native resume only — rarely read
quiz.lock            proof-test answer hashes (see §5); never readable as answers
```

Only `manifest.json` is required. Everything else is written by the sending
AI following `TASK.md` (produced by `tote prepare`), so a box made by Claude
reads the same as one made by Codex or Gemini.

**Reading order for the receiving AI:** `START-HERE.md` → `INDEX.md` →
`DONE.md` → anything else only when the work needs it.

## 3. manifest.json

```json
{
  "format": "tote-box/1",
  "created": "2026-10-02T16:30:00Z",
  "tote_version": "0.1.0",
  "source": { "os": "darwin", "home": "/Users/sam", "host_label": "mac" },
  "tools": ["claude"],
  "files": [
    { "path": "START-HERE.md", "size": 1834, "sha256": "…" }
  ]
}
```

- `files` lists every payload entry except `manifest.json` itself.
- `source.home` lets the receiver rewrite paths (e.g. `/Users/sam/...` →
  `C:\Users\lab\...`). `host_label` is a name the user chose, never the real
  hostname.
- On open, every file is hashed while extracting. If any size or hash differs
  from the manifest, or a file is missing or extra, the open fails and nothing
  is left behind.

## 4. Code and PIN

- **key** — 128 random bits, written as 26 lowercase base32 characters. It
  goes in the pasted command, so it travels over WhatsApp etc.
- **pin** — 6 random digits. Shown only to the sender and **must be sent a
  different way** (said aloud, typed by hand).
- The box is locked with passphrase `key:pin`. Having the box and the key but
  not the PIN is not enough to open it.

## 5. Proof test (quiz.json → quiz.lock)

The sending AI writes `quiz.json`:

```json
{ "questions": [ { "q": "…", "answers": ["Lisbon", "Lisbon, Portugal"], "source": "path/in/box.md" } ] }
```

3–30 questions; each answer ≤ 8 words; a question may not contain its own
answer. When packing, tote replaces `quiz.json` with `quiz.lock`:

```json
{ "format": "tote-quiz/1", "salt": "…", "questions": [ { "id": 1, "q": "…", "hashes": ["…"], "max_words": 2, "source": "…" } ] }
```

Answers are normalised (lowercase; digits' thousand-separators dropped;
punctuation, currency signs and quotes → spaces; trailing dots trimmed) and
fingerprinted as `sha256(salt + "\0" + answer)`. A reply is right if any run
of up to `max_words` consecutive words in its first 40 words matches. The
receiving AI answers through `tote quiz`; tote grades. Two attempts; the
first is the score ("Context received: 9/10").

Threat model: catches an AI that skipped or skimmed the box. It does not stop
an AI deliberately brute-forcing short answers against the salted hashes.

## 6. Versioning

`format` changes only when a v1 reader would misread a box. New optional files
do not change the version.

## 7. Privacy rules (every tote, every mailbox)

1. **Only your own files.** Pack refuses a folder owned by another user and
   leaves out any file or shortcut target owned by another user (reported as
   "not yours"). On Windows the OS already blocks other profiles; guest mode
   adds an ACL check.
2. **Guest mode packs only tote's private folder**, never the machine owner's
   AI folders.
3. **Locked before it leaves.** Mailboxes (hosted, your bucket, a shared
   folder) only ever hold locked boxes. The receiver needs the ticket *and*
   the PIN; storage keys never travel in tickets.
