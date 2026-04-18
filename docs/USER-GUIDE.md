# teachable-dl User Guide

**Version:** 1.0.0
**Last updated:** 2026-03-01

---

## Table of Contents

1. [Introduction](#1-introduction)
2. [Prerequisites](#2-prerequisites)
3. [Installation](#3-installation)
4. [Usage](#4-usage)
5. [Output Structure](#5-output-structure)
6. [Course Metadata](#6-course-metadata)
7. [Troubleshooting](#7-troubleshooting)

---

## 1. Introduction

teachable-dl downloads video courses from the Teachable platform to your local machine. It automatically extracts authentication from your Arc browser session, so there's no need for API keys or manual login -- just visit the course page in Arc once, and teachable-dl handles the rest.

### What It Does

- Downloads all video lessons from a Teachable course
- Organises videos into section folders (e.g. `01 - Section Name/01 - Lesson.mp4`)
- Embeds metadata into each MP4 (title, artist, album, track number)
- Writes a `course-info.json` with the full course structure and attachment details
- Skips already-downloaded files (safe to re-run)

### What It Doesn't Do

- Download non-video attachments (PDFs, slides, etc.) -- these are listed in `course-info.json` but not downloaded
- Work with browsers other than Arc (Chrome/Brave cookie format differs slightly)
- Work on non-macOS systems (requires Keychain access)

## 2. Prerequisites

| Requirement | How to check | Install |
|-------------|-------------|---------|
| **Go 1.25+** | `go version` | `brew install go` |
| **Arc browser** | Must be installed | [arc.net](https://arc.net) |
| **yt-dlp** | `yt-dlp --version` | `brew install yt-dlp` |
| **ffmpeg** (optional) | `ffmpeg -version` | `brew install ffmpeg` |

You must be **logged into the Teachable course** in Arc. Visit the course page at least once so the authentication cookies are stored.

## 3. Installation

### Build from source

```bash
cd ~/course-vault
CGO_ENABLED=1 go build -o teachable-dl .
```

### Add to PATH

```bash
ln -sf ~/course-vault/teachable-dl ~/bin/teachable-dl
```

### Verify

```bash
teachable-dl
# Should print usage information
```

## 4. Usage

### Basic usage

```bash
teachable-dl <course-url> [output-dir]
```

### Examples

Download a course to a specific directory:

```bash
teachable-dl "https://school.teachable.com/courses/enrolled/12345" ~/Videos/MyCourse
```

Download to the current directory:

```bash
teachable-dl "https://school.teachable.com/courses/enrolled/12345"
```

With verbose output (shows cookie decryption, API calls, attachment details):

```bash
teachable-dl -v "https://school.teachable.com/courses/enrolled/12345" ~/Videos/MyCourse
```

### Finding the course URL

1. Open your Teachable course in Arc
2. Navigate to the course curriculum/syllabus page
3. The URL will look like: `https://your-school.teachable.com/courses/enrolled/1234567`
4. Copy the full URL -- that's what you pass to teachable-dl

### Re-running / resuming

teachable-dl checks for existing files before downloading. If a download is interrupted, just run the same command again -- it will skip completed files and continue from where it left off.

## 5. Output Structure

```
output-dir/
├── course-info.json
├── 01 - Section Name/
│   ├── 01 - Lesson Title.mp4
│   ├── 02 - Another Lesson.mp4
│   └── 03 - Third Lesson.mp4
└── 02 - Another Section/
    └── 01 - Webinar Recording.mp4
```

- Sections are numbered and named from the course structure
- Lessons are numbered by their position within each section
- Filenames are sanitised (special characters removed, max 80 chars)
- Each MP4 has embedded metadata tags (title, artist, album, track)

### MP4 Metadata

Each downloaded video has the following tags embedded in the MP4 container:

| Tag | Value |
|-----|-------|
| `title` | Lesson name |
| `artist` | Course author |
| `album` | Course title |
| `track` | Lesson number within section |

These tags are visible in Finder's Get Info, VLC, IINA, and other media players.

## 6. Course Metadata

A `course-info.json` file is written to the output directory containing the full course structure:

```json
{
  "title": "Course Title",
  "description": "Course description text",
  "author": "Author Name",
  "image_url": "https://...",
  "url": "https://school.teachable.com/courses/enrolled/12345",
  "course_id": "12345",
  "sections": [
    {
      "title": "Section Name",
      "position": 1,
      "lectures": [
        {
          "id": "44111275",
          "title": "Lesson Title",
          "position": 1,
          "url": "https://...",
          "attachments": [
            {
              "name": "Video 1.mp4",
              "kind": "video",
              "content_type": "video/mp4",
              "cdn_url": "https://uploads.teachablecdn.com/...",
              "file_size": 262930432
            }
          ]
        }
      ]
    }
  ]
}
```

This file can be used for scripting, cataloguing, or downloading non-video attachments separately.

## 7. Troubleshooting

### "Cookie extraction failed"

- **Cause:** Arc browser cookies not found or not readable
- **Fix:** Make sure Arc is installed and you've visited the Teachable course page while logged in. The cookie database is at `~/Library/Application Support/Arc/User Data/Default/Cookies`

### "Found 0 sections, 0 lectures"

- **Cause:** Authentication cookies expired or course ID not found
- **Fix:** Open the course in Arc again (this refreshes the session cookies), then re-run

### "attachments API returned 401"

- **Cause:** Session expired
- **Fix:** Visit the course page in Arc to refresh your session, then re-run

### "no video attachment found"

- **Cause:** The lecture has no video content (text-only lesson, quiz, etc.)
- **Action:** This is normal -- the lesson is listed in `course-info.json` but skipped for download

### "ffmpeg not found, skipping metadata embed"

- **Cause:** ffmpeg is not installed
- **Fix:** `brew install ffmpeg` (optional -- videos download fine without it, just without embedded tags)

### Downloads are slow

- Teachable CDN speeds vary. Typical rates are 10-55 MB/s depending on time of day and region
- yt-dlp handles retries automatically
- Re-run the command to resume any failed downloads

### "ciphertext too short or misaligned"

- **Cause:** Cookie encryption format not recognised
- **Fix:** This may happen with non-Arc Chromium browsers. Currently only Arc is supported
