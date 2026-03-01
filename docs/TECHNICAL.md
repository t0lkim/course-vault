# teachable-dl Technical Reference

**Version:** 1.0.0
**Language:** Go 1.25
**Last updated:** 2026-03-01

---

## Table of Contents

1. [Overview](#1-overview)
2. [Architecture](#2-architecture)
3. [Cookie Decryption](#3-cookie-decryption)
4. [Teachable API](#4-teachable-api)
5. [Video Download Pipeline](#5-video-download-pipeline)
6. [Metadata Embedding](#6-metadata-embedding)
7. [Dependencies](#7-dependencies)
8. [Build](#8-build)

---

## 1. Overview

teachable-dl is a Go CLI tool that downloads video courses from the Teachable platform. It extracts authentication cookies from the Arc browser's encrypted SQLite database, uses Teachable's JSON API to enumerate course structure, downloads video attachments via direct CDN URLs, embeds MP4 metadata tags, and writes a `course-info.json` manifest.

The tool requires no API keys or manual authentication -- it piggybacks on existing Arc browser sessions.

## 2. Architecture

```
Arc Cookie DB ──► Cookie Extraction ──► PBKDF2 Key Derivation
                                              │
                                              ▼
                                     AES-128-CBC Decryption
                                              │
                                              ▼
                                     HTTP Client (with cookies)
                                              │
                        ┌─────────────────────┼──────────────────┐
                        ▼                     ▼                  ▼
               /api/v1/courses/{id}  /api/v1/.../lectures  /api/v1/.../attachments
                        │                     │                  │
                        ▼                     ▼                  ▼
                   Course Meta          Section + Lecture     Video CDN URLs
                        │                   Structure              │
                        ▼                     │                    ▼
                  course-info.json            │              yt-dlp download
                                              │                    │
                                              └────────┬───────────┘
                                                       ▼
                                              ffmpeg metadata embed
                                                       │
                                                       ▼
                                              01 - Section/01 - Lesson.mp4
```

### Execution Flow

1. **Cookie extraction** -- copies Arc's SQLite cookie DB to a temp file (avoids lock contention), queries cookies matching the school domain.
2. **Cookie decryption** -- retrieves the Arc keychain password via `security find-generic-password`, derives an AES-128 key with PBKDF2, decrypts each cookie value.
3. **Curriculum fetch** -- calls two JSON API endpoints to get sections and lectures, builds ordered section→lecture tree.
4. **Course metadata** -- calls the course API for title, description, author.
5. **Video extraction** -- for each lecture, calls the attachments API, finds video attachments, returns CDN URL.
6. **Download** -- passes CDN URL to yt-dlp with Netscape-format cookie file.
7. **Metadata embed** -- runs ffmpeg to write title/artist/album/track tags into the MP4 container (copy codec, no re-encoding).
8. **Manifest** -- writes `course-info.json` with full course structure and attachment details.

## 3. Cookie Decryption

### Keychain Access

Arc stores its cookie encryption password in the macOS Keychain under the service name `Arc Safe Storage`. Retrieved via:

```
security find-generic-password -s "Arc Safe Storage" -w
```

### Key Derivation

Chromium-standard PBKDF2:
- **Password:** Keychain value (typically 24 bytes)
- **Salt:** `saltysalt` (literal)
- **Iterations:** 1003
- **Key length:** 16 bytes (AES-128)
- **Hash:** SHA-1

### Encrypted Cookie Format

Arc/newer Chromium on macOS uses a different format to older Chromium:

```
"v10" (3 bytes) + nonce (16 bytes) + IV (16 bytes) + AES-128-CBC ciphertext
```

- **Bytes [0:3]:** `v10` prefix (version identifier)
- **Bytes [3:19]:** 16-byte nonce (not used for CBC decryption, possibly for future AEAD)
- **Bytes [19:35]:** 16-byte IV for AES-128-CBC
- **Bytes [35:]:** Ciphertext (must be aligned to 16-byte blocks)
- **Padding:** PKCS#7

This differs from the commonly documented Chromium cookie encryption which uses a hardcoded 16-space IV. The nonce+IV format is specific to Arc and possibly newer Chromium builds.

### Cookie Database

- **Path:** `~/Library/Application Support/Arc/User Data/Default/Cookies`
- **Format:** SQLite3
- **Table:** `cookies`
- **Key columns:** `name`, `encrypted_value`, `host_key`, `path`, `is_secure`, `is_httponly`
- **Query:** `SELECT ... FROM cookies WHERE host_key LIKE '%{domain}'`

The database file is copied to a temp location before querying to avoid SQLite lock contention with the running browser.

## 4. Teachable API

All endpoints are unauthenticated JSON APIs that rely on session cookies for authorisation.

### Course Metadata

```
GET {base}/api/v1/courses/{courseID}
```

Returns: course name, heading, description (HTML), author bio, image URL.

### Lecture Sections

```
GET {base}/api/v1/courses/{courseID}/lecture_sections
```

Returns: `{ "lecture_sections": [{ "id", "name", "position" }] }`

### Lectures

```
GET {base}/api/v1/courses/{courseID}/lectures
```

Returns: `{ "lectures": [{ "id", "name", "position", "lecture_section_id", "is_published" }] }`

### Lecture Attachments

```
GET {base}/api/v1/courses/{courseID}/lectures/{lectureID}/attachments
```

Returns: `{ "attachments": [{ "kind", "name", "content_type", "cdn_url", "url", "file_size" }] }`

- Video attachments have `kind: "video"` and `content_type: "video/mp4"`
- `cdn_url` points to `uploads.teachablecdn.com` -- direct MP4 download, no streaming protocol
- Response can be large (~200KB) due to base64-encoded thumbnail data in non-video attachments

### Course URL Format

```
https://{school}.teachable.com/courses/enrolled/{courseID}
```

The course ID is extracted as the last numeric path segment.

## 5. Video Download Pipeline

Videos are downloaded using `yt-dlp` with a Netscape-format cookie file for authentication. Although the CDN URLs are direct MP4 links (no HLS/DASH), yt-dlp provides:

- Automatic retry on network errors
- Resume support for interrupted downloads
- Progress display
- Proper output file naming via `-o` flag

### Cookie File Format

Netscape HTTP Cookie File format, one line per cookie:

```
.domain.com    TRUE    /    TRUE    0    cookie_name    cookie_value
```

## 6. Metadata Embedding

After each successful download, ffmpeg embeds metadata tags into the MP4 container:

```
ffmpeg -i input.mp4 -c copy \
  -metadata title="Lesson Title" \
  -metadata artist="Course Author" \
  -metadata album="Course Title" \
  -metadata track=N \
  -y output.mp4
```

- **`-c copy`** -- stream copy, no re-encoding (sub-second operation)
- Tags are written to the MP4 container's metadata atoms
- If ffmpeg is not installed, metadata embedding is silently skipped

## 7. Dependencies

### Go Modules

| Module | Purpose |
|--------|---------|
| `github.com/mattn/go-sqlite3` | SQLite3 driver (CGO) for reading Arc cookie database |
| `golang.org/x/crypto` | PBKDF2 key derivation |
| `golang.org/x/net` | Public suffix list for cookie jar |

### External Tools

| Tool | Required | Purpose |
|------|----------|---------|
| `yt-dlp` | Yes | Video download with retry/resume |
| `ffmpeg` | Optional | MP4 metadata embedding |

### System Requirements

- **macOS** -- required for Keychain access (`security` command)
- **Arc browser** -- must be installed with active Teachable session
- **CGO** -- required for sqlite3 driver compilation

## 8. Build

```bash
cd code/
CGO_ENABLED=1 go build -o teachable-dl .
```

The `CGO_ENABLED=1` flag is mandatory because `go-sqlite3` is a CGO wrapper around the C SQLite library. Without it, the build will fail with missing symbol errors.

### Install

```bash
# Build
cd ~/MyAI/USER/PROJECTS/teachable-dl/code
CGO_ENABLED=1 go build -o teachable-dl .

# Symlink to PATH
ln -sf "$(pwd)/teachable-dl" ~/bin/teachable-dl
```

### Cross-compilation

Not supported due to CGO dependency. Must be built on the target platform.
