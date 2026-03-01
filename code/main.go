package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/net/publicsuffix"
)

const (
	arcCookiePath = "Library/Application Support/Arc/User Data/Default/Cookies"
	keychainSvc   = "Arc Safe Storage"
)

var verbose bool

type lecture struct {
	ID    string
	Title string
	URL   string
	Order int
}

type section struct {
	Title    string
	Lectures []lecture
}

// Metadata types for course-info.json export
type courseInfo struct {
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Author      string            `json:"author,omitempty"`
	ImageURL    string            `json:"image_url,omitempty"`
	URL         string            `json:"url"`
	CourseID    string            `json:"course_id"`
	Sections    []sectionInfo     `json:"sections"`
}

type sectionInfo struct {
	Title    string       `json:"title"`
	Position int          `json:"position"`
	Lectures []lectureInfo `json:"lectures"`
}

type lectureInfo struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Position    int              `json:"position"`
	URL         string           `json:"url"`
	Attachments []attachmentInfo `json:"attachments"`
}

type attachmentInfo struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	ContentType string `json:"content_type,omitempty"`
	URL         string `json:"url,omitempty"`
	CDNURL      string `json:"cdn_url,omitempty"`
	FileSize    int64  `json:"file_size,omitempty"`
}

func logv(format string, args ...interface{}) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
	}
}

func main() {
	// Parse flags manually to keep positional args simple
	args := []string{}
	for _, a := range os.Args[1:] {
		if a == "-v" || a == "--verbose" {
			verbose = true
		} else {
			args = append(args, a)
		}
	}

	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: teachable-dl [-v] <course-url> [output-dir]\n")
		fmt.Fprintf(os.Stderr, "\nFlags:\n")
		fmt.Fprintf(os.Stderr, "  -v, --verbose   Show detailed debug output\n")
		fmt.Fprintf(os.Stderr, "\nExample:\n")
		fmt.Fprintf(os.Stderr, "  teachable-dl https://school.teachable.com/courses/enrolled/12345\n")
		fmt.Fprintf(os.Stderr, "  teachable-dl -v https://school.teachable.com/courses/enrolled/12345 ~/Videos/MyCourse\n")
		os.Exit(1)
	}

	courseURL := args[0]
	outputDir := "."
	if len(args) > 1 {
		outputDir = args[1]
	}

	// Parse school domain from URL
	u, err := url.Parse(courseURL)
	if err != nil {
		fatal("Invalid URL: %v", err)
	}
	school := u.Hostname()

	fmt.Printf("🎓 Teachable Downloader\n")
	fmt.Printf("   School: %s\n", school)
	fmt.Printf("   Output: %s\n\n", outputDir)

	// Step 1: Extract cookies from Arc
	fmt.Println("🔑 Extracting cookies from Arc browser...")
	cookies, err := extractArcCookies(school)
	if err != nil {
		fatal("Cookie extraction failed: %v\n\nMake sure you're logged into Teachable in Arc.", err)
	}
	fmt.Printf("   Found %d cookies for %s\n", len(cookies), school)
	if verbose {
		for _, c := range cookies {
			logv("  cookie: %s=%s... (domain=%s)", c.Name, truncate(c.Value, 20), c.Domain)
		}
	}
	fmt.Println()

	// Build HTTP client with cookies
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	jar.SetCookies(u, cookies)
	client := &http.Client{Jar: jar}

	// Step 2: Fetch course page and extract lecture URLs
	fmt.Println("📋 Fetching course curriculum...")
	sections, courseID, err := fetchCurriculum(client, courseURL)
	if err != nil {
		fatal("Failed to fetch curriculum: %v", err)
	}

	totalLectures := 0
	for _, s := range sections {
		totalLectures += len(s.Lectures)
	}
	fmt.Printf("   Found %d sections, %d lectures\n\n", len(sections), totalLectures)

	// Fetch course-level metadata
	base := u.Scheme + "://" + u.Host
	courseMeta := fetchCourseMeta(client, base, courseID)

	// Build metadata structure — sections and lectures populated from curriculum
	meta := courseInfo{
		Title:       courseMeta.Title,
		Description: courseMeta.Description,
		Author:      courseMeta.Author,
		ImageURL:    courseMeta.ImageURL,
		URL:         courseURL,
		CourseID:    courseID,
	}

	// Step 3: For each lecture, find the video and download
	downloaded := 0
	for si, sec := range sections {
		sectionDir := filepath.Join(outputDir, fmt.Sprintf("%02d - %s", si+1, sanitise(sec.Title)))
		os.MkdirAll(sectionDir, 0755)

		secMeta := sectionInfo{
			Title:    sec.Title,
			Position: si + 1,
		}

		for li, lec := range sec.Lectures {
			fmt.Printf("📹 [%d/%d] %s\n", downloaded+1, totalLectures, lec.Title)

			// Fetch lecture attachments (returns video URL + all attachment metadata)
			videoURL, atts, err := extractVideoURL(client, lec.URL)

			lecMeta := lectureInfo{
				ID:          lec.ID,
				Title:       lec.Title,
				Position:    lec.Order,
				URL:         lec.URL,
				Attachments: atts,
			}
			secMeta.Lectures = append(secMeta.Lectures, lecMeta)

			if err != nil {
				fmt.Printf("   ⚠️  Skipped (no video found): %v\n", err)
				continue
			}

			filename := fmt.Sprintf("%02d - %s", li+1, sanitise(lec.Title))
			outPath := filepath.Join(sectionDir, filename+".mp4")

			if _, err := os.Stat(outPath); err == nil {
				fmt.Printf("   ⏭️  Already exists, skipping\n")
				downloaded++
				continue
			}

			// Download using yt-dlp (handles HLS, Wistia, etc.)
			err = downloadVideo(videoURL, outPath, cookies, school)
			if err != nil {
				fmt.Printf("   ❌ Download failed: %v\n", err)
				continue
			}

			// Embed metadata into MP4
			embedMetadata(outPath, lec.Title, courseMeta.Author, courseMeta.Title, li+1)

			downloaded++
			fmt.Printf("   ✅ Saved\n")
		}

		meta.Sections = append(meta.Sections, secMeta)
	}

	// Write course-info.json
	os.MkdirAll(outputDir, 0755)
	infoPath := filepath.Join(outputDir, "course-info.json")
	infoJSON, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(infoPath, infoJSON, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Failed to write course-info.json: %v\n", err)
	} else {
		fmt.Printf("📄 Wrote %s\n", infoPath)
	}

	fmt.Printf("\n🎉 Done! Downloaded %d/%d lectures to %s\n", downloaded, totalLectures, outputDir)
}

// extractArcCookies reads cookies from Arc's SQLite database
func extractArcCookies(domain string) ([]*http.Cookie, error) {
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, arcCookiePath)

	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("Arc cookie database not found at %s", dbPath)
	}

	// Get decryption key from macOS Keychain
	key, err := getKeychainKey()
	if err != nil {
		return nil, fmt.Errorf("keychain access failed: %v", err)
	}

	// Derive the AES key using PBKDF2 (Chromium standard)
	derivedKey := pbkdf2.Key(key, []byte("saltysalt"), 1003, 16, sha1.New)

	// Copy DB to temp (Arc may have it locked)
	tmpDB, err := copyToTemp(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to copy cookie DB: %v", err)
	}
	defer os.Remove(tmpDB)

	db, err := sql.Open("sqlite3", tmpDB+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Query cookies for the domain (and parent domains)
	rows, err := db.Query(
		`SELECT name, encrypted_value, host_key, path, is_secure, is_httponly
		 FROM cookies WHERE host_key LIKE ?`,
		"%"+domain,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cookies []*http.Cookie
	for rows.Next() {
		var name string
		var encValue []byte
		var host, path string
		var secure, httponly int

		if err := rows.Scan(&name, &encValue, &host, &path, &secure, &httponly); err != nil {
			continue
		}

		value, err := decryptCookie(encValue, derivedKey)
		if err != nil {
			continue
		}

		cookies = append(cookies, &http.Cookie{
			Name:     name,
			Value:    value,
			Domain:   host,
			Path:     path,
			Secure:   secure == 1,
			HttpOnly: httponly == 1,
		})
	}

	return cookies, nil
}

// getKeychainKey retrieves Arc's encryption key from macOS Keychain
func getKeychainKey() ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-s", keychainSvc, "-w").Output()
	if err != nil {
		return nil, fmt.Errorf("could not read '%s' from Keychain: %v", keychainSvc, err)
	}
	return []byte(strings.TrimSpace(string(out))), nil
}

// decryptCookie decrypts a Chromium-encrypted cookie value.
// Arc (and newer Chromium on macOS) stores: "v10" + 16-byte nonce + 16-byte IV + AES-128-CBC ciphertext.
func decryptCookie(encrypted []byte, key []byte) (string, error) {
	if len(encrypted) == 0 {
		return "", nil
	}

	// Chromium v10 encryption: "v10" prefix + AES-128-CBC
	if len(encrypted) < 3 || string(encrypted[:3]) != "v10" {
		// Not encrypted or different version
		return string(encrypted), nil
	}

	data := encrypted[3:] // strip "v10" prefix

	// Arc/newer Chromium: 16-byte nonce + 16-byte IV + ciphertext (minimum 48 bytes)
	if len(data) < 48 || (len(data)-32)%16 != 0 {
		return "", fmt.Errorf("ciphertext too short or misaligned (len=%d)", len(data))
	}

	iv := data[16:32]
	ciphertext := data[32:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	decrypted := make([]byte, len(ciphertext))
	mode.CryptBlocks(decrypted, ciphertext)

	// Remove PKCS#7 padding
	if len(decrypted) > 0 {
		padding := int(decrypted[len(decrypted)-1])
		if padding > 0 && padding <= aes.BlockSize && padding <= len(decrypted) {
			// Verify all padding bytes are consistent
			valid := true
			for i := len(decrypted) - padding; i < len(decrypted); i++ {
				if decrypted[i] != byte(padding) {
					valid = false
					break
				}
			}
			if valid {
				decrypted = decrypted[:len(decrypted)-padding]
			}
		}
	}

	return string(decrypted), nil
}

// copyToTemp copies a file to a temp location
func copyToTemp(src string) (string, error) {
	tmp, err := os.CreateTemp("", "teachable-cookies-*.db")
	if err != nil {
		return "", err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		tmp.Close()
		return "", err
	}
	defer srcFile.Close()

	_, err = io.Copy(tmp, srcFile)
	tmp.Close()
	return tmp.Name(), err
}

// fetchCourseMeta fetches course-level metadata from the Teachable API.
func fetchCourseMeta(client *http.Client, base, courseID string) struct {
	Title       string
	Description string
	Author      string
	ImageURL    string
} {
	type result struct {
		Title       string
		Description string
		Author      string
		ImageURL    string
	}

	apiURL := fmt.Sprintf("%s/api/v1/courses/%s", base, courseID)
	logv("Fetching course metadata: %s", apiURL)
	resp, err := client.Get(apiURL)
	if err != nil {
		logv("Course metadata fetch failed: %v", err)
		return result{}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	logv("Course metadata API: status=%d, len=%d", resp.StatusCode, len(body))

	var data struct {
		Name        string `json:"name"`
		Heading     string `json:"heading"`
		Description string `json:"description"`
		ImageURL    string `json:"image_url"`
		AuthorBio   struct {
			Name string `json:"name"`
		} `json:"author_bio"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		logv("Course metadata parse failed: %v", err)
		return result{}
	}

	title := data.Name
	if title == "" {
		title = data.Heading
	}

	return result{
		Title:       title,
		Description: stripTags(data.Description),
		Author:      data.AuthorBio.Name,
		ImageURL:    data.ImageURL,
	}
}

// fetchCurriculum retrieves the course curriculum via Teachable's JSON API.
func fetchCurriculum(client *http.Client, courseURL string) ([]section, string, error) {
	u, _ := url.Parse(courseURL)
	base := u.Scheme + "://" + u.Host

	// Extract course ID from URL (last path segment that's numeric)
	courseID := ""
	for _, seg := range strings.Split(u.Path, "/") {
		if len(seg) > 0 && seg[0] >= '0' && seg[0] <= '9' {
			courseID = seg
		}
	}
	if courseID == "" {
		return nil, "", fmt.Errorf("could not extract course ID from URL")
	}
	logv("Course ID: %s", courseID)

	// Fetch lecture sections
	type apiSection struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		Position int    `json:"position"`
	}
	type apiLecture struct {
		ID               int    `json:"id"`
		Name             string `json:"name"`
		Position         int    `json:"position"`
		LectureSectionID int    `json:"lecture_section_id"`
		IsPublished      bool   `json:"is_published"`
	}

	// Get sections
	logv("Fetching /api/v1/courses/%s/lecture_sections", courseID)
	resp, err := client.Get(fmt.Sprintf("%s/api/v1/courses/%s/lecture_sections", base, courseID))
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch sections: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	logv("Sections API: status=%d, len=%d", resp.StatusCode, len(body))

	var secResp struct {
		LectureSections []apiSection `json:"lecture_sections"`
	}
	if err := json.Unmarshal(body, &secResp); err != nil {
		return nil, "", fmt.Errorf("failed to parse sections JSON: %v", err)
	}

	// Get lectures
	logv("Fetching /api/v1/courses/%s/lectures", courseID)
	resp2, err := client.Get(fmt.Sprintf("%s/api/v1/courses/%s/lectures", base, courseID))
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch lectures: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	logv("Lectures API: status=%d, len=%d", resp2.StatusCode, len(body2))

	var lecResp struct {
		Lectures []apiLecture `json:"lectures"`
	}
	if err := json.Unmarshal(body2, &lecResp); err != nil {
		return nil, "", fmt.Errorf("failed to parse lectures JSON: %v", err)
	}

	// Build section index map (ID → index into ordered list)
	sectionIndex := map[int]int{} // section API ID → index
	sort.Slice(secResp.LectureSections, func(i, j int) bool {
		return secResp.LectureSections[i].Position < secResp.LectureSections[j].Position
	})
	sections := make([]section, len(secResp.LectureSections))
	for i, s := range secResp.LectureSections {
		sections[i] = section{Title: s.Name}
		sectionIndex[s.ID] = i
	}

	// Sort lectures by position and assign to sections
	sort.Slice(lecResp.Lectures, func(i, j int) bool {
		return lecResp.Lectures[i].Position < lecResp.Lectures[j].Position
	})
	for _, l := range lecResp.Lectures {
		if !l.IsPublished {
			logv("Skipping unpublished lecture: %s", l.Name)
			continue
		}
		idx, ok := sectionIndex[l.LectureSectionID]
		if !ok {
			logv("Lecture %d (%s) has unknown section %d, adding to last section", l.ID, l.Name, l.LectureSectionID)
			idx = len(sections) - 1
			if idx < 0 {
				sections = append(sections, section{Title: "Other"})
				idx = 0
			}
		}
		sections[idx].Lectures = append(sections[idx].Lectures, lecture{
			ID:    fmt.Sprintf("%d", l.ID),
			Title: l.Name,
			URL:   fmt.Sprintf("%s/courses/%s/lectures/%d", base, courseID, l.ID),
			Order: l.Position,
		})
	}

	// Remove empty sections
	var result []section
	for _, s := range sections {
		if len(s.Lectures) > 0 {
			result = append(result, s)
		}
	}

	return result, courseID, nil
}

// extractVideoURL fetches the lecture's attachments via the JSON API and returns the video CDN URL
// plus metadata for all attachments (for course-info.json).
func extractVideoURL(client *http.Client, lectureURL string) (string, []attachmentInfo, error) {
	// Parse course ID and lecture ID from the URL
	re := regexp.MustCompile(`/courses/(\d+)/lectures/(\d+)`)
	m := re.FindStringSubmatch(lectureURL)
	if m == nil {
		return "", nil, fmt.Errorf("cannot parse lecture URL: %s", lectureURL)
	}
	courseID, lectureID := m[1], m[2]

	u, _ := url.Parse(lectureURL)
	apiURL := fmt.Sprintf("%s://%s/api/v1/courses/%s/lectures/%s/attachments", u.Scheme, u.Host, courseID, lectureID)
	logv("Fetching attachments: %s", apiURL)

	resp, err := client.Get(apiURL)
	if err != nil {
		return "", nil, fmt.Errorf("attachments API request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("attachments API returned %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	logv("Attachments API: %d bytes", len(body))

	var data struct {
		Attachments []struct {
			Kind        string `json:"kind"`
			Name        string `json:"name"`
			ContentType string `json:"content_type"`
			CDNURL      string `json:"cdn_url"`
			URL         string `json:"url"`
			FileSize    int64  `json:"file_size"`
		} `json:"attachments"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		return "", nil, fmt.Errorf("failed to parse attachments JSON: %v", err)
	}

	logv("Found %d attachments", len(data.Attachments))

	// Collect all attachment metadata
	var atts []attachmentInfo
	for _, att := range data.Attachments {
		atts = append(atts, attachmentInfo{
			Name:        att.Name,
			Kind:        att.Kind,
			ContentType: att.ContentType,
			URL:         att.URL,
			CDNURL:      att.CDNURL,
			FileSize:    att.FileSize,
		})
	}

	// Find and return video URL
	for _, att := range data.Attachments {
		logv("  Attachment: kind=%s, name=%s, type=%s", att.Kind, att.Name, att.ContentType)
		if att.Kind == "video" || strings.HasPrefix(att.ContentType, "video/") {
			videoURL := att.CDNURL
			if videoURL == "" {
				videoURL = att.URL
			}
			if videoURL != "" {
				logv("  Using CDN URL: %s", truncate(videoURL, 80))
				return videoURL, atts, nil
			}
		}
	}

	return "", atts, fmt.Errorf("no video attachment found (got %d non-video attachments)", len(data.Attachments))
}

// embedMetadata writes title, artist, and album tags into the MP4 container via ffmpeg.
func embedMetadata(filePath, title, artist, album string, track int) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		logv("ffmpeg not found, skipping metadata embed")
		return
	}

	tmp := filePath + ".meta.mp4"
	args := []string{
		"-i", filePath,
		"-c", "copy",
		"-metadata", fmt.Sprintf("title=%s", title),
		"-metadata", fmt.Sprintf("artist=%s", artist),
		"-metadata", fmt.Sprintf("album=%s", album),
		"-metadata", fmt.Sprintf("track=%d", track),
		"-y", tmp,
	}

	logv("Embedding metadata: title=%q artist=%q album=%q track=%d", title, artist, album, track)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		logv("ffmpeg metadata embed failed: %v", err)
		os.Remove(tmp)
		return
	}

	if err := os.Rename(tmp, filePath); err != nil {
		logv("Failed to replace file with tagged version: %v", err)
		os.Remove(tmp)
		return
	}
	logv("Metadata embedded successfully")
}

// downloadVideo uses yt-dlp to download a video URL
func downloadVideo(videoURL, outPath string, cookies []*http.Cookie, domain string) error {
	// Write a temp cookies.txt for yt-dlp
	cookieFile, err := writeCookiesTxt(cookies, domain)
	if err != nil {
		return fmt.Errorf("failed to write cookies: %v", err)
	}
	defer os.Remove(cookieFile)

	args := []string{
		"--cookies", cookieFile,
		"-o", outPath,
		"--no-warnings",
		"--no-playlist",
		"--merge-output-format", "mp4",
		videoURL,
	}

	cmd := exec.Command("yt-dlp", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// writeCookiesTxt writes cookies in Netscape format for yt-dlp
func writeCookiesTxt(cookies []*http.Cookie, domain string) (string, error) {
	f, err := os.CreateTemp("", "teachable-cookies-*.txt")
	if err != nil {
		return "", err
	}

	fmt.Fprintln(f, "# Netscape HTTP Cookie File")
	for _, c := range cookies {
		httpOnly := "FALSE"
		if c.HttpOnly {
			httpOnly = "TRUE"
		}
		secure := "FALSE"
		if c.Secure {
			secure = "TRUE"
		}
		dom := c.Domain
		if !strings.HasPrefix(dom, ".") {
			dom = "." + dom
		}
		fmt.Fprintf(f, "%s\tTRUE\t%s\t%s\t0\t%s\t%s\n",
			dom, c.Path, secure, c.Name, c.Value)
		_ = httpOnly // Netscape format doesn't have httpOnly column
	}

	f.Close()
	return f.Name(), nil
}

func stripTags(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	return re.ReplaceAllString(s, "")
}

func sanitise(s string) string {
	// Remove characters invalid in filenames
	re := regexp.MustCompile(`[<>:"/\\|?*]`)
	s = re.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", args...)
	os.Exit(1)
}
