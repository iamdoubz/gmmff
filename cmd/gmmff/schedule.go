package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iamdoubz/gmmff/v2/internal/schedule"
	"github.com/mdp/qrterminal/v3"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// ─────────────────────────────────────────────────────────────────────────────
// Root schedule command (shows help when called with no subcommand)
// ─────────────────────────────────────────────────────────────────────────────

var scheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Encrypted server-side file transfers (async dead-drop)",
	Long: `Schedule transfers let you upload an AES-256-GCM encrypted file to the server
and share a download link. The recipient decrypts it in their browser or via
the CLI — no simultaneous connection required.

The server never sees plaintext. The decryption key lives only in the URL
fragment (#key=...), which is never transmitted to the server.

Subcommands:
  upload    Encrypt and upload a file to the server
  download  Download and decrypt a file using a share URL`,
	// No RunE — shows help automatically when called with no subcommand.
}

// ─────────────────────────────────────────────────────────────────────────────
// gmmff schedule upload
// ─────────────────────────────────────────────────────────────────────────────

type scheduleUploadFlags struct {
	server       string
	ttl          string
	maxDownloads int
	password     string
	out          string
	qr           bool
	jsonOut      bool
	chunkSize    int
}

var schedUploadFlags scheduleUploadFlags

var scheduleUploadCmd = &cobra.Command{
	Use:   "upload <file> [file|dir ...]",
	Short: "Encrypt and upload file(s) to the server",
	Long: `Encrypt one or more files with AES-256-GCM and upload them to the server.
Multiple files or directories are zipped automatically before encryption.

The decryption key is printed separately from the share URL so you can
distribute them via different channels for improved security.

Examples:
  gmmff schedule upload report.pdf
  gmmff schedule upload *.csv --ttl 8h --max-downloads 3
  gmmff schedule upload ./project/ --ttl 7d --qr
  gmmff schedule upload file.zip --json`,
	Args: cobra.MinimumNArgs(1),
	RunE: runScheduleUpload,
}

func init() {
	f := scheduleUploadCmd.Flags()
	f.StringVar(&schedUploadFlags.server, "server", "", "Signaling server URL (wss://host/ws or https://host); uses $GMMFF_SERVER if unset")
	f.StringVar(&schedUploadFlags.ttl, "ttl", "24h", "File expiry duration (e.g. 1h, 8h, 1 day, 7d, 30 days)")
	f.IntVar(&schedUploadFlags.maxDownloads, "max-downloads", 1, "Maximum downloads (0 = unlimited)")
	f.StringVar(&schedUploadFlags.password, "password", "", "Upload password (prompted if required and not set)")
	f.StringVar(&schedUploadFlags.out, "out", "", "Write share URL to this file instead of stdout")
	f.BoolVar(&schedUploadFlags.qr, "qr", false, "Print a QR code for the share URL")
	f.BoolVar(&schedUploadFlags.jsonOut, "json", false, "Output result as JSON")
	f.IntVar(&schedUploadFlags.chunkSize, "chunk-size", 0, "Override chunk size in bytes (0 = auto)")

	scheduleCmd.AddCommand(scheduleUploadCmd)
	rootCmd.AddCommand(scheduleCmd)
}

func runScheduleUpload(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// ── 1. Resolve server URL ─────────────────────────────────────────────────
	rawServer := schedUploadFlags.server
	if rawServer == "" {
		rawServer = os.Getenv("GMMFF_SERVER")
	}
	if rawServer == "" {
		rawServer = "http://localhost:8080"
	}
	client, err := schedule.NewClient(rawServer)
	if err != nil {
		return err
	}

	// ── 2. Auth check ─────────────────────────────────────────────────────────
	auth, err := client.CheckAuth(ctx)
	if err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}
	if !auth.Allowed && !auth.NeedsPassword {
		return fmt.Errorf("upload not permitted from your IP address")
	}
	if auth.NeedsPassword {
		pw := schedUploadFlags.password
		if pw == "" {
			pw, err = promptPassword("Upload password: ")
			if err != nil {
				return err
			}
		}
		client.Password = pw
	}

	// ── 3. Parse TTL ──────────────────────────────────────────────────────────
	ttl, err := parseTTLFlag(schedUploadFlags.ttl)
	if err != nil {
		return fmt.Errorf("invalid --ttl %q: %w", schedUploadFlags.ttl, err)
	}

	// ── 4. Collect files ──────────────────────────────────────────────────────
	reader, filename, size, cleanup, err := prepareUploadFiles(args)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	// ── 5. Upload with progress bar ───────────────────────────────────────────
	if !schedUploadFlags.jsonOut {
		fmt.Fprintf(os.Stderr, "Uploading %s (%s)...\n", filename, formatBytes(size))
	}

	bar := progressbar.NewOptions64(size,
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(40),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionSetDescription(""),
		progressbar.OptionClearOnFinish(),
		progressbar.OptionSetVisibility(!schedUploadFlags.jsonOut),
	)

	var lastUploaded int64
	startTime := time.Now()

	result, err := client.Upload(ctx, reader, filename, size, schedule.UploadOptions{
		TTL:          ttl,
		MaxDownloads: schedUploadFlags.maxDownloads,
		ChunkSize:    schedUploadFlags.chunkSize,
		Progress: func(uploaded, total int64, _ float64) {
			delta := uploaded - atomic.SwapInt64(&lastUploaded, uploaded)
			bar.Add64(delta)
		},
	})
	bar.Finish()
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}

	elapsed := time.Since(startTime)
	avgSpeed := float64(size) / elapsed.Seconds()

	// ── 6. Output result ──────────────────────────────────────────────────────
	if schedUploadFlags.jsonOut {
		return printUploadResultJSON(result)
	}
	return printUploadResult(result, avgSpeed, schedUploadFlags.out, schedUploadFlags.qr)
}

func printUploadResult(r *schedule.UploadResult, speed float64, outFile string, qr bool) error {
	expLocal := r.ExpiresAt.Local()
	timeStr := expLocal.Format("2006-01-02 15:04 MST")
	inStr := formatDuration(time.Until(r.ExpiresAt))

	lines := []string{
		"",
		"  Upload complete!",
		"",
		fmt.Sprintf("  File ID:         %s", r.FileID),
		fmt.Sprintf("  Decryption key:  %s", r.KeyHex),
		"",
		fmt.Sprintf("  Share URL:       %s", r.ShareURL),
		fmt.Sprintf("  Full URL:        %s", r.FullURL),
		"",
		fmt.Sprintf("  Auto-download:   %s&dl=1#key=%s", r.ShareURL, r.KeyHex),
		"",
		fmt.Sprintf("  Delete URL:      %s", r.DeleteURL),
		"",
		fmt.Sprintf("  Expires:         %s (in %s)", timeStr, inStr),
		fmt.Sprintf("  Avg speed:       %s/s", formatBytes(int64(speed))),
		"",
	}
	output := strings.Join(lines, "\n")
	fmt.Fprint(os.Stdout, output)

	if outFile != "" {
		if err := os.WriteFile(outFile, []byte(r.FullURL+"\n"), 0o640); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Full URL written to %s\n", outFile)
	}

	if qr {
		printQR(r.FullURL)
	}
	return nil
}

type uploadResultJSON struct {
	FileID        string `json:"file_id"`
	Key           string `json:"key"`
	ShareURL      string `json:"share_url"`
	FullURL       string `json:"full_url"`
	AutoDownload  string `json:"auto_download_url"`
	DeleteURL     string `json:"delete_url"`
	ExpiresAt     string `json:"expires_at"`
	DownloadsLeft int    `json:"downloads_left"`
}

func printUploadResultJSON(r *schedule.UploadResult) error {
	out := uploadResultJSON{
		FileID:        r.FileID,
		Key:           r.KeyHex,
		ShareURL:      r.ShareURL,
		FullURL:       r.FullURL,
		AutoDownload:  fmt.Sprintf("%s&dl=1#key=%s", r.ShareURL, r.KeyHex),
		DeleteURL:     r.DeleteURL,
		ExpiresAt:     r.ExpiresAt.UTC().Format(time.RFC3339),
		DownloadsLeft: -1, // filled from server opts not easily available here
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// ─────────────────────────────────────────────────────────────────────────────
// gmmff schedule download
// ─────────────────────────────────────────────────────────────────────────────

type scheduleDownloadFlags struct {
	out     string
	confirm bool
	jsonOut bool
}

var schedDlFlags scheduleDownloadFlags

var scheduleDownloadCmd = &cobra.Command{
	Use:   "download <share-url>",
	Short: "Download and decrypt a scheduled file",
	Long: `Fetch and decrypt a file uploaded via gmmff schedule upload.

Pass the full share URL including the #key= fragment as a single quoted argument:

  gmmff schedule download "https://host/?type=schedule&id=X#key=Y"

Pipe to stdout with --out -:

  gmmff schedule download "https://host/...#key=..." --out - | tar xz
  gmmff schedule download "https://host/...#key=..." --out - > file.zip

The decryption happens locally — the server never receives the key.`,
	Args: cobra.ExactArgs(1),
	RunE: runScheduleDownload,
}

func init() {
	f := scheduleDownloadCmd.Flags()
	f.StringVarP(&schedDlFlags.out, "out", "o", ".", "Output directory or filename; use - for stdout")
	f.BoolVar(&schedDlFlags.confirm, "confirm", false, "Prompt for confirmation before downloading")
	f.BoolVar(&schedDlFlags.jsonOut, "json", false, "Output result as JSON")
	scheduleCmd.AddCommand(scheduleDownloadCmd)
}

func runScheduleDownload(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	rawURL := args[0]

	// ── 1. Parse share URL ────────────────────────────────────────────────────
	fileID, keyHex, err := schedule.ParseShareURL(rawURL)
	if err != nil {
		return err
	}

	// ── 2. Derive server base URL from share URL ───────────────────────────────
	client, err := schedule.NewClient(rawURL)
	if err != nil {
		return err
	}

	// ── 3. Fetch metadata ─────────────────────────────────────────────────────
	meta, err := client.FetchMeta(ctx, fileID)
	if err != nil {
		return err
	}

	if !schedDlFlags.jsonOut {
		fmt.Fprintf(os.Stderr, "\n  File:       (encrypted — name revealed after decryption)\n")
		fmt.Fprintf(os.Stderr, "  Size:       ~%s (plaintext)\n", formatBytes(meta.TotalSize))
		fmt.Fprintf(os.Stderr, "  Expires:    %s\n", meta.ExpiresAt.Local().Format("2006-01-02 15:04 MST"))
		if meta.DownloadsLeft == 1 {
			fmt.Fprintf(os.Stderr, "  ⚠  This is the last download — the link will be invalidated.\n")
		} else if meta.DownloadsLeft > 0 {
			fmt.Fprintf(os.Stderr, "  Downloads:  %d remaining\n", meta.DownloadsLeft)
		}
		fmt.Fprintln(os.Stderr)
	}

	// ── 4. Optional confirmation ──────────────────────────────────────────────
	if schedDlFlags.confirm {
		if meta.DownloadsLeft == 1 {
			fmt.Fprint(os.Stderr, "This will exhaust the last remaining download. Continue? [y/N] ")
		} else {
			fmt.Fprint(os.Stderr, "Continue with download? [y/N] ")
		}
		var answer string
		fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return nil
		}
	}

	// ── 5. Resolve output destination ─────────────────────────────────────────
	toStdout := schedDlFlags.out == "-"
	var outWriter io.Writer
	var outPath string
	var outFile *os.File

	// outWriter is assigned below after the progress bar is created.

	// ── 6. Progress bar (stderr so it doesn't corrupt stdout pipe) ────────────
	bar := progressbar.NewOptions64(meta.TotalSize,
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(40),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionClearOnFinish(),
		progressbar.OptionSetVisibility(!toStdout && !schedDlFlags.jsonOut),
	)

	var tempPath string
	if !toStdout {
		// Write to a temp file; rename after decryption reveals the filename.
		f, err := os.CreateTemp("", "gmmff-schedule-*.tmp")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		tempPath = f.Name()
		outFile = f
		outWriter = io.MultiWriter(f, bar)
	} else {
		outWriter = os.Stdout
	}

	startTime := time.Now()

	result, err := client.Download(ctx, fileID, keyHex, meta, outWriter, schedule.DownloadOptions{
		Progress: func(written, total int64, _ float64) {
			bar.Set64(written)
		},
	})
	bar.Finish()

	if err != nil {
		if tempPath != "" {
			os.Remove(tempPath)
		}
		return fmt.Errorf("download failed: %w", err)
	}

	elapsed := time.Since(startTime)
	avgSpeed := float64(result.BytesRead) / elapsed.Seconds()

	// ── 7. Rename temp file to final filename ─────────────────────────────────
	if !toStdout && tempPath != "" {
		outFile.Close()
		// Determine final output path.
		dest := schedDlFlags.out
		info, statErr := os.Stat(dest)
		if statErr == nil && info.IsDir() {
			dest = filepath.Join(dest, result.Filename)
		} else if dest == "." {
			dest = result.Filename
		}
		if err := os.Rename(tempPath, dest); err != nil {
			// Rename may fail across filesystems — fall back to copy.
			if err2 := copyFile(tempPath, dest); err2 != nil {
				return fmt.Errorf("save file: %w", err2)
			}
			os.Remove(tempPath)
		}
		outPath = dest
	}

	// ── 8. Output result ──────────────────────────────────────────────────────
	if schedDlFlags.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"filename":  result.Filename,
			"saved_to":  outPath,
			"bytes":     result.BytesRead,
			"speed_bps": int64(avgSpeed),
		})
	}

	if !toStdout {
		fmt.Fprintf(os.Stderr, "\n  Saved to:   %s\n", outPath)
		fmt.Fprintf(os.Stderr, "  Size:       %s\n", formatBytes(result.BytesRead))
		fmt.Fprintf(os.Stderr, "  Avg speed:  %s/s\n\n", formatBytes(int64(avgSpeed)))
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// gmmff schedule delete
// ─────────────────────────────────────────────────────────────────────────────

var scheduleDeleteCmd = &cobra.Command{
	Use:   "delete <delete-url>",
	Short: "Delete an uploaded file from the server",
	Long: `Delete a scheduled file using the delete URL shown after upload.

  gmmff schedule delete "https://host/?type=schedule&id=X&action=delete&dk=Y"

You can also pass --id and --delete-key explicitly:

  gmmff schedule delete --id abc123 --delete-key def456 --server https://host`,
	Args: cobra.MaximumNArgs(1),
	RunE: runScheduleDelete,
}

type scheduleDeleteFlags struct {
	id        string
	deleteKey string
	server    string
}

var schedDelFlags scheduleDeleteFlags

func init() {
	f := scheduleDeleteCmd.Flags()
	f.StringVar(&schedDelFlags.id, "id", "", "File ID to delete")
	f.StringVar(&schedDelFlags.deleteKey, "delete-key", "", "Delete key shown after upload")
	f.StringVar(&schedDelFlags.server, "server", "", "Server URL (derived from delete URL if not set)")
	scheduleCmd.AddCommand(scheduleDeleteCmd)
}

func runScheduleDelete(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	var fileID, deleteKey, serverURL string

	if len(args) == 1 {
		// Parse from delete URL.
		var err error
		fileID, deleteKey, err = schedule.ParseDeleteURL(args[0])
		if err != nil {
			return err
		}
		serverURL = args[0]
	} else {
		// Explicit flags.
		fileID = schedDelFlags.id
		deleteKey = schedDelFlags.deleteKey
		serverURL = schedDelFlags.server
		if fileID == "" || deleteKey == "" {
			return fmt.Errorf("provide a delete URL or both --id and --delete-key")
		}
		if serverURL == "" {
			serverURL = os.Getenv("GMMFF_SERVER")
		}
		if serverURL == "" {
			return fmt.Errorf("provide --server or set GMMFF_SERVER")
		}
	}

	client, err := schedule.NewClient(serverURL)
	if err != nil {
		return err
	}

	if err := client.Delete(ctx, fileID, deleteKey); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "File %s deleted.\n", fileID)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// prepareUploadFiles reads one or more files/dirs into a stream.
// Multiple files are zipped on the fly using the existing archive package.
func prepareUploadFiles(paths []string) (r io.Reader, filename string, size int64, cleanup func(), err error) {
	if len(paths) == 1 {
		// Single file — read directly.
		info, err := os.Stat(paths[0])
		if err != nil {
			return nil, "", 0, nil, fmt.Errorf("stat %q: %w", paths[0], err)
		}
		if !info.IsDir() {
			f, err := os.Open(paths[0])
			if err != nil {
				return nil, "", 0, nil, fmt.Errorf("open %q: %w", paths[0], err)
			}
			return f, info.Name(), info.Size(), func() { f.Close() }, nil
		}
	}

	// Multiple files or a directory — zip to a temp file first.
	tmp, err := os.CreateTemp("", "gmmff-zip-*.zip")
	if err != nil {
		return nil, "", 0, nil, fmt.Errorf("create temp zip: %w", err)
	}
	tmpName := tmp.Name()

	if err := zipPaths(tmp, paths); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return nil, "", 0, nil, fmt.Errorf("zip: %w", err)
	}
	size, _ = tmp.Seek(0, io.SeekCurrent)
	tmp.Seek(0, io.SeekStart)

	// Determine a sensible filename for the zip.
	base := filepath.Base(paths[0])
	zipName := strings.TrimSuffix(base, filepath.Ext(base)) + ".zip"

	return tmp, zipName, size, func() {
		tmp.Close()
		os.Remove(tmpName)
	}, nil
}

// zipPaths writes all paths into a ZIP archive on w.
func zipPaths(w io.Writer, paths []string) error {
	// Collect all files.
	type entry struct {
		name string
		path string
	}
	var entries []entry

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := filepath.Walk(p, func(path string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(filepath.Dir(p), path)
				entries = append(entries, entry{rel, path})
				return nil
			}); err != nil {
				return err
			}
		} else {
			entries = append(entries, entry{info.Name(), p})
		}
	}

	// Write minimal ZIP (STORE, no compression) — same format as browser JS.
	type localEntry struct {
		name   []byte
		data   []byte
		crc    uint32
		offset uint32
	}
	var locals []localEntry
	var offset uint32

	for _, e := range entries {
		data, err := os.ReadFile(e.path)
		if err != nil {
			return err
		}
		nameBytes := []byte(filepath.ToSlash(e.name))
		crc := crc32Compute(data)
		locals = append(locals, localEntry{nameBytes, data, crc, offset})

		lf := buildLocalFileHeaderGo(nameBytes, uint32(len(data)), crc)
		offset += uint32(len(lf)) + uint32(len(data))
		if _, err := w.Write(lf); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}

	// Central directory.
	cdStart := offset
	var cdSize uint32
	for _, e := range locals {
		cd := buildCentralDirEntryGo(e.name, uint32(len(e.data)), e.crc, e.offset)
		cdSize += uint32(len(cd))
		if _, err := w.Write(cd); err != nil {
			return err
		}
	}
	eocd := buildEOCDGo(uint16(len(locals)), cdSize, cdStart)
	_, err := w.Write(eocd)
	return err
}

// Minimal ZIP helpers (STORE, no compression).
func buildLocalFileHeaderGo(name []byte, size, crc uint32) []byte {
	b := make([]byte, 30+len(name))
	putUint32LE(b, 0, 0x04034b50)
	putUint16LE(b, 4, 20)
	putUint16LE(b, 6, 0)
	putUint16LE(b, 8, 0) // STORE
	putUint32LE(b, 14, crc)
	putUint32LE(b, 18, size)
	putUint32LE(b, 22, size)
	putUint16LE(b, 26, uint16(len(name)))
	copy(b[30:], name)
	return b
}

func buildCentralDirEntryGo(name []byte, size, crc, off uint32) []byte {
	b := make([]byte, 46+len(name))
	putUint32LE(b, 0, 0x02014b50)
	putUint16LE(b, 4, 20)
	putUint16LE(b, 6, 20)
	putUint16LE(b, 8, 0)
	putUint16LE(b, 10, 0)
	putUint32LE(b, 16, crc)
	putUint32LE(b, 20, size)
	putUint32LE(b, 24, size)
	putUint16LE(b, 28, uint16(len(name)))
	putUint32LE(b, 42, off)
	copy(b[46:], name)
	return b
}

func buildEOCDGo(count uint16, cdSize, cdOffset uint32) []byte {
	b := make([]byte, 22)
	putUint32LE(b, 0, 0x06054b50)
	putUint16LE(b, 8, count)
	putUint16LE(b, 10, count)
	putUint32LE(b, 12, cdSize)
	putUint32LE(b, 16, cdOffset)
	return b
}

func putUint32LE(b []byte, off int, v uint32) {
	b[off], b[off+1], b[off+2], b[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func putUint16LE(b []byte, off int, v uint16) {
	b[off], b[off+1] = byte(v), byte(v>>8)
}

func crc32Compute(data []byte) uint32 {
	var crc uint32 = 0xFFFFFFFF
	for _, b := range data {
		crc ^= uint32(b)
		for k := 0; k < 8; k++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xEDB88320
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFFFFFF
}

// copyFile copies src to dst when os.Rename crosses filesystems.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// promptPassword reads a masked password from the terminal.
func promptPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr) // newline after masked input
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

// parseTTLFlag parses a CLI --ttl value using the same fuzzy parser as the
// server-side GMMFF_TTL_SETTINGS env var.
func parseTTLFlag(s string) (time.Duration, error) {
	d, _, err := schedule.ParseFuzzyDuration(s)
	return d, err
}

func formatDuration(d time.Duration) string {
	switch {
	case d >= 7*24*time.Hour:
		return fmt.Sprintf("%.0f weeks", d.Hours()/168)
	case d >= 24*time.Hour:
		return fmt.Sprintf("%.0f days", d.Hours()/24)
	case d >= time.Hour:
		return fmt.Sprintf("%.0f hours", d.Hours())
	default:
		return fmt.Sprintf("%.0f minutes", d.Minutes())
	}
}

// printQR prints a QR code for url to the terminal.
func printQR(url string) {
	fmt.Fprintln(os.Stderr, "\nQR code:")
	qrterminal.GenerateWithConfig(url, qrterminal.Config{
		Level:          qrterminal.M,
		Writer:         os.Stderr,
		BlackChar:      qrterminal.BLACK,
		WhiteChar:      qrterminal.WHITE,
		BlackWhiteChar: qrterminal.BLACK,
		HalfBlocks:     false,
	})
}
