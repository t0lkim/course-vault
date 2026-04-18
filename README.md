# CourseVault

Download and archive course content from Teachable platforms. Authenticates via Arc browser cookies, enumerates curriculum via JSON API, downloads videos at full CDN quality.

## Usage

```bash
coursevault https://school.teachable.com/courses/enrolled/12345 ~/Videos/MyCourse
```

## Features

- Arc browser cookie extraction (AES-128-CBC decryption of Chromium v10 format)
- Teachable JSON API curriculum enumeration
- Video download via yt-dlp (handles HLS, Wistia, direct MP4)
- MP4 metadata embedding via ffmpeg (title, artist, album, track number)
- Structured manifest (`course-info.json`) for resume and inventory
- Resume support — skips already-downloaded files on re-run

## Language

Go

## License

MIT
