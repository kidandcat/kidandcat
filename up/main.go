package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed page.html
var pageFS embed.FS

const (
	maxFileSize   = 1 << 30  // 1 GiB of file content
	maxStoreBytes = 20 << 30 // 20 GiB total on disk
	maxNameLen    = 180
	uploadsHour   = 5
)

func main() {
	dir := env("KIDANDCAT_UP_DIR", "/var/lib/kidandcat-up")
	listen := env("KIDANDCAT_UP_LISTEN", "127.0.0.1:8098")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		log.Fatalf("create store: %v", err)
	}

	page, err := fs.ReadFile(pageFS, "page.html")
	if err != nil {
		log.Fatalf("embed page: %v", err)
	}

	s := &server{
		dir:   dir,
		page:  page,
		limit: newLimiter(uploadsHour, time.Hour),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/up/health", s.health)
	mux.HandleFunc("/up", s.route)
	mux.HandleFunc("/up/", s.route)

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Minute,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("kidandcat-up listening on %s, storing in %s", listen, dir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type server struct {
	dir   string
	page  []byte
	limit *limiter
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, "ok\n")
	}
}

func (s *server) route(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/up" && r.URL.Path != "/up/" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.form(w, r)
	case http.MethodPost:
		s.upload(w, r)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) form(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(s.page)
	}
}

type uploadOK struct {
	OK   bool   `json:"ok"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type uploadErr struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	ip := clientIP(r)
	if !s.limit.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "Demasiados envíos. Prueba dentro de un rato.")
		return
	}

	used, err := dirSize(s.dir)
	if err != nil {
		log.Printf("dir size: %v", err)
		writeErr(w, http.StatusInternalServerError, "No pude comprobar el espacio. Inténtalo otra vez.")
		return
	}
	if used >= maxStoreBytes {
		writeErr(w, http.StatusInsufficientStorage, "No queda sitio ahora mismo. Avísame y lo vacío.")
		return
	}

	// Bound the whole request (file + multipart wrapper).
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize+2<<20)

	reader, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "El envío no es un archivo. Elige un fichero e inténtalo otra vez.")
		return
	}

	var (
		gotFile bool
		saved   string
		size    int64
	)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "No pude leer el archivo. Inténtalo otra vez.")
			return
		}
		if part.FormName() != "file" {
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 1<<20))
			_ = part.Close()
			continue
		}
		gotFile = true
		saved, size, err = s.savePart(part)
		_ = part.Close()
		if err != nil {
			status, msg := uploadFail(err)
			writeErr(w, status, msg)
			return
		}
		break
	}
	if !gotFile {
		writeErr(w, http.StatusBadRequest, "No ha llegado ningún archivo.")
		return
	}

	log.Printf("stored %s (%d bytes) from %s", saved, size, ip)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(uploadOK{OK: true, Name: saved, Size: size})
}

func (s *server) savePart(part interface {
	io.Reader
	FileName() string
}) (string, int64, error) {
	name := sanitizeName(part.FileName())
	final := time.Now().UTC().Format("20060102-150405") + "_" + name
	dest := uniquePath(s.dir, final)

	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return "", 0, fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(part, maxFileSize+1))
	if err != nil {
		return "", 0, fmt.Errorf("write: %w", err)
	}
	if n == 0 {
		return "", 0, errEmpty
	}
	if n > maxFileSize {
		return "", 0, errTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", 0, fmt.Errorf("rename: %w", err)
	}
	cleanup = false
	if err := os.Chmod(dest, 0o640); err != nil {
		log.Printf("chmod %s: %v", dest, err)
	}
	return filepath.Base(dest), n, nil
}

var (
	errTooLarge = errors.New("too large")
	errEmpty    = errors.New("empty")
)

func uploadFail(err error) (int, string) {
	switch {
	case errors.Is(err, errTooLarge):
		return http.StatusRequestEntityTooLarge, "El archivo supera 1 GB."
	case errors.Is(err, errEmpty):
		return http.StatusBadRequest, "El archivo está vacío."
	case errors.Is(err, os.ErrDeadlineExceeded):
		return http.StatusRequestTimeout, "Se cortó la subida. Prueba de nuevo."
	default:
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return http.StatusRequestEntityTooLarge, "El archivo supera 1 GB."
		}
		log.Printf("upload error: %v", err)
		return http.StatusInternalServerError, "No pude guardar el archivo. Inténtalo otra vez."
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(uploadErr{OK: false, Error: msg})
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N}._+\-()\[\] ]+`)

func sanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	name = unsafeName.ReplaceAllString(name, "_")
	name = strings.Join(strings.Fields(name), " ")
	name = strings.Trim(name, "._ ")
	if name == "" {
		return "file"
	}
	if len(name) > maxNameLen {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ext[:16]
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		keep := maxNameLen - len(ext)
		if keep < 8 {
			keep = 8
		}
		if len(base) > keep {
			base = base[:keep]
		}
		name = strings.TrimRight(base, "._ ") + ext
	}
	if name == "" {
		return "file"
	}
	return name
}

func uniquePath(dir, name string) string {
	dest := filepath.Join(dir, name)
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		return dest
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; i < 8; i++ {
		dest = filepath.Join(dir, fmt.Sprintf("%s-%s%s", base, randHex(3), ext))
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			return dest
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext))
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type limiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(n int, window time.Duration) *limiter {
	return &limiter{n: n, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-l.window)
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.n {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	return true
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
