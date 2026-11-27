package readapi

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/vzd3v/agent_session_collector/internal/schema"
	"github.com/vzd3v/agent_session_collector/sessionapi"
)

const downloadSlotsRetry = "1"

type Server struct {
	db         *sql.DB
	mirror     *os.File
	archive    *os.File
	tokensFile string
	log        *slog.Logger
	cursorKey  [32]byte
	slots      chan struct{}
	maxBytes   int64
}

func New(ctx context.Context, c Config, log *slog.Logger) (*Server, error) {
	server := &Server{log: log, tokensFile: c.TokensFile, maxBytes: c.MaxBytes, slots: make(chan struct{}, c.MaxTransfers)}
	if _, err := rand.Read(server.cursorKey[:]); err != nil {
		return nil, err
	}
	db, err := schema.OpenReadOnly(c.Catalog)
	if err != nil {
		return nil, fmt.Errorf("open read-only catalog: %w", err)
	}
	server.db = db
	if err = server.ready(ctx); err == nil {
		server.mirror, err = openDirectory(c.Mirror)
	}
	if err == nil {
		server.archive, err = openDirectory(c.Archive)
	}
	if err == nil {
		_, err = readTokens(c.TokensFile)
	}
	if err != nil {
		return nil, errors.Join(err, server.Close())
	}
	return server, nil
}

func openDirectory(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		return nil, errors.Join(fmt.Errorf("storage root must be a directory: %s", path), err, f.Close())
	}
	return f, nil
}

func (s *Server) Close() error {
	var errs []error
	for _, f := range []*os.File{s.mirror, s.archive} {
		if f != nil {
			errs = append(errs, f.Close())
		}
	}
	return errors.Join(append(errs, s.db.Close())...)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.ready(r.Context()); err != nil {
			s.respondError(w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/v1/sessions/query", s.list)
	const base = "GET /api/v1/sources/{source}/sessions/{artifact}"
	mux.HandleFunc(base, s.item("", false, s.get))
	mux.HandleFunc(base+"/events", s.item("events", false, s.events))
	mux.HandleFunc(base+"/transcript", s.item(sessionapi.Transcript, true, s.transcript))
	mux.HandleFunc(base+"/archive", s.item(sessionapi.Archive, true, s.bundle))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	credential, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var q *sessionapi.Query
	body := http.MaxBytesReader(w, r.Body, 160<<10)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil || q == nil || r.URL.RawQuery != "" || !errors.Is(decoder.Decode(new(any)), io.EOF) ||
		len(q.SourceIDs) > sessionapi.MaxSources || len(q.NativeID) > 512 {
		http.Error(w, "invalid session query", http.StatusBadRequest)
		return
	}
	for _, id := range q.SourceIDs {
		if !sessionapi.ValidSourceID(id) {
			http.Error(w, "invalid source ID", http.StatusBadRequest)
			return
		}
	}
	q.SourceIDs = intersect(credential.ReadSources, q.SourceIDs)
	cursor := q.Cursor
	q.Cursor = ""
	if q.Limit == 0 {
		q.Limit = 50
	}
	var after listPosition
	if q.Limit < 1 || q.Limit > 100 || s.parseCursor(cursor, *q, &after) != nil {
		http.Error(w, "invalid limit or cursor", http.StatusBadRequest)
		return
	}
	page, err := s.listRows(r.Context(), *q, after)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if len(page.Sessions) > q.Limit {
		page.Sessions = page.Sessions[:q.Limit]
		last := page.Sessions[len(page.Sessions)-1]
		page.NextCursor = s.signCursor(*q, listPosition{last.SourceID, last.ArtifactID})
	}
	s.respondJSON(w, r, page)
}

func (s *Server) item(kind string, export bool, handle func(http.ResponseWriter, *http.Request, row, url.Values)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if kind != "" && r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		credential, ok := s.authenticate(w, r)
		if !ok {
			return
		}
		ref := sessionapi.Ref{SourceID: r.PathValue("source"), ArtifactID: r.PathValue("artifact")}
		ids := credential.ReadSources
		if export {
			ids = credential.ExportSources
		}
		if !sessionapi.ValidSourceID(ref.SourceID) || !sessionapi.ValidArtifactID(ref.ArtifactID) || !slices.Contains(ids, ref.SourceID) {
			s.respondError(w, errMissing)
			return
		}
		keys := []string{"sha256"}
		if kind == "events" {
			keys = append(keys, "cursor", "limit")
		}
		values, err := parseQuery(r.URL.RawQuery, keys...)
		ref.SHA256 = values.Get("sha256")
		if err != nil || (ref.SHA256 != "" && !sessionapi.ValidSHA256(ref.SHA256)) {
			http.Error(w, "invalid session query", http.StatusBadRequest)
			return
		}
		if kind != "" {
			select {
			case s.slots <- struct{}{}:
				defer func() { <-s.slots }()
			default:
				w.Header().Set("Retry-After", downloadSlotsRetry)
				http.Error(w, "read API busy", http.StatusServiceUnavailable)
				return
			}
		}
		p, err := s.lookup(r.Context(), ref)
		if err != nil {
			s.respondError(w, err)
			return
		}
		handle(w, r, p, values)
	}
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, p row, _ url.Values) {
	s.respondJSON(w, r, p.Session)
}

func (s *Server) respondError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMissing):
		http.Error(w, errMissing.Error(), http.StatusNotFound)
	case errors.Is(err, errGeneration):
		http.Error(w, errGeneration.Error(), http.StatusConflict)
	case errors.Is(err, errLimit):
		http.Error(w, errLimit.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, errCursor):
		http.Error(w, errCursor.Error(), http.StatusBadRequest)
	case errors.Is(err, errInvalidRecord):
		http.Error(w, errInvalidRecord.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, errNotReady):
		http.Error(w, errNotReady.Error(), http.StatusServiceUnavailable)
	default:
		s.log.Error("session API operation failed", "error", err)
		http.Error(w, "session API operation failed", http.StatusServiceUnavailable)
	}
}

func parseQuery(raw string, keys ...string) (url.Values, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, err
	}
	for key, vals := range values {
		if !slices.Contains(keys, key) || len(vals) != 1 {
			return nil, errors.New("invalid session query parameter")
		}
	}
	return values, nil
}

func pageLimit(value string, fallback, maximum int) int {
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > maximum {
		return 0
	}
	return n
}

func (s *Server) respondJSON(w http.ResponseWriter, r *http.Request, body any) {
	s.writeEncoded(w, r, "application/json", func(dst io.Writer) error {
		return json.NewEncoder(dst).Encode(body)
	})
}

func (s *Server) writeEncoded(w http.ResponseWriter, r *http.Request, mime string, send func(io.Writer) error) {
	encoding := negotiateEncoding(r.Header.Values("Accept-Encoding"))
	w.Header().Add("Vary", "Accept-Encoding")
	if encoding == "" {
		http.Error(w, "no supported encoding accepted", http.StatusNotAcceptable)
		return
	}
	w.Header().Set("Content-Type", mime)
	if encoding == "gzip" {
		w.Header().Set("Content-Encoding", "gzip")
	}
	w.WriteHeader(http.StatusOK)
	if encoding == "gzip" {
		z, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
		if err != nil {
			s.abort(err)
		}
		if err := send(z); err != nil {
			s.abort(err)
		}
		if err := z.Close(); err != nil {
			s.abort(err)
		}
		return
	}
	if err := send(w); err != nil {
		s.abort(err)
	}
}

func negotiateEncoding(values []string) string {
	gz, identity, wildcard := -1.0, -1.0, -1.0
	for _, item := range strings.Split(strings.Join(values, ","), ",") {
		name, option, _ := strings.Cut(item, ";")
		q := 1.0
		if option != "" {
			prefix, value, ok := strings.Cut(strings.TrimSpace(option), "=")
			var err error
			q, err = strconv.ParseFloat(value, 64)
			if !ok || !strings.EqualFold(prefix, "q") || err != nil || !(q >= 0 && q <= 1) {
				q = 0
			}
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "gzip":
			gz = q
		case "identity":
			identity = q
		case "*":
			wildcard = q
		}
	}
	if gz < 0 {
		gz = wildcard
	}
	if identity < 0 {
		identity = 1
		if wildcard == 0 {
			identity = 0
		}
	}
	if gz > 0 && gz >= identity {
		return "gzip"
	}
	if identity > 0 {
		return "identity"
	}
	return ""
}

func (s *Server) abort(err error) {
	s.log.Warn("session API response interrupted", "error", err)
	panic(http.ErrAbortHandler)
}
