package readapi

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/vzd3v/agent_session_collector/sessionapi"
)

var (
	errPageComplete  = errors.New("event page ready")
	errInvalidRecord = errors.New("transcript contains an invalid JSON record")
)

func (s *Server) events(w http.ResponseWriter, r *http.Request, p row, values url.Values) {
	limit := pageLimit(values.Get("limit"), 40, 100)
	scope := sessionapi.Ref{SourceID: p.SourceID, ArtifactID: p.ArtifactID}
	pos := eventPosition{SHA256: p.SHA256}
	if limit == 0 {
		http.Error(w, "invalid limit", http.StatusBadRequest)
		return
	}
	if err := s.parseCursor(values.Get("cursor"), scope, &pos); err != nil {
		s.respondError(w, err)
		return
	}
	if pos.SHA256 != p.SHA256 || pos.Offset < 0 || pos.Offset > p.SizeBytes {
		s.respondError(w, errGeneration)
		return
	}
	file, err := s.openCapture(r.Context(), p)
	if err != nil {
		s.respondError(w, err)
		return
	}
	defer func() { _ = file.file.Close() }()
	page := sessionapi.Events{Ref: p.Ref, Records: []json.RawMessage{}}
	err = file.read(r.Context(), pos.Offset, func(reader io.Reader) error {
		return readEvents(reader, &page, &pos, p.SizeBytes, limit)
	})
	if errors.Is(err, errPageComplete) {
		page.NextCursor = s.signCursor(scope, pos)
	} else if err != nil {
		s.respondError(w, err)
		return
	}
	s.respondJSON(w, r, page)
}

func readEvents(reader io.Reader, page *sessionapi.Events, pos *eventPosition, size int64, limit int) error {
	const maxPageBytes = 1 << 20
	input := bufio.NewReaderSize(reader, maxPageBytes)
	total := 0
	for {
		line, err := input.ReadSlice('\n')
		eof := errors.Is(err, io.EOF)
		if errors.Is(err, bufio.ErrBufferFull) {
			return errLimit
		}
		if err != nil && !eof {
			return err
		}
		raw := bytes.TrimSpace(line)
		if len(raw) != 0 {
			if !json.Valid(raw) {
				if !eof {
					return errInvalidRecord
				}
				page.IncompleteTail = true
			} else {
				if total+len(raw) > maxPageBytes {
					return errPageComplete
				}
				page.Records = append(page.Records, bytes.Clone(raw))
				total += len(raw)
			}
		}
		pos.Offset += int64(len(line))
		if pos.Offset > size || (eof && pos.Offset != size) {
			return errGeneration
		}
		if eof {
			return nil
		}
		if len(page.Records) == limit && pos.Offset < size {
			return errPageComplete
		}
	}
}

func (s *Server) transcript(w http.ResponseWriter, r *http.Request, p row, _ url.Values) {
	file, err := s.openCapture(r.Context(), p)
	if err != nil {
		s.respondError(w, err)
		return
	}
	defer func() { _ = file.file.Close() }()
	w.Header().Set("X-Content-SHA256", p.SHA256)
	s.writeEncoded(w, r, "application/x-ndjson", func(dst io.Writer) error {
		return file.write(r.Context(), dst)
	})
}

type manifest struct {
	Version   int                  `json:"version"`
	Anchor    sessionapi.Ref       `json:"anchor"`
	Artifacts []sessionapi.Session `json:"artifacts"`
}

func (s *Server) bundle(w http.ResponseWriter, r *http.Request, p row, _ url.Values) {
	members, err := s.members(r.Context(), p)
	if err != nil {
		s.respondError(w, err)
		return
	}
	files := make([]capture, 0, len(members))
	defer func() {
		for _, p := range files {
			_ = p.file.Close()
		}
	}()
	var total int64
	for _, member := range members {
		if member.SizeBytes < 0 || member.SizeBytes > s.maxBytes-total {
			s.respondError(w, errLimit)
			return
		}
		total += member.SizeBytes
		file, err := s.openCapture(r.Context(), member)
		if err != nil {
			s.respondError(w, err)
			return
		}
		files = append(files, file)
	}
	header := manifest{Version: 1, Anchor: p.Ref, Artifacts: make([]sessionapi.Session, 0, len(files))}
	for _, file := range files {
		header.Artifacts = append(header.Artifacts, file.Session)
	}
	body, err := json.Marshal(header)
	if err != nil {
		s.respondError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote("session-"+p.ArtifactID+".tar.gz"))
	z, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		s.respondError(w, err)
		return
	}
	t := tar.NewWriter(z)
	if err = writeEntry(t, "manifest.json", int64(len(body))); err == nil {
		_, err = t.Write(body)
	}
	if err != nil {
		s.abort(err)
	}
	for _, file := range files {
		if err := writeEntry(t, file.ArtifactID+".jsonl", file.SizeBytes); err != nil {
			s.abort(err)
		}
		if err := file.write(r.Context(), t); err != nil {
			s.abort(err)
		}
	}
	if err := t.Close(); err != nil {
		s.abort(err)
	}
	if err := z.Close(); err != nil {
		s.abort(err)
	}
}

func writeEntry(t *tar.Writer, name string, size int64) error {
	return t.WriteHeader(&tar.Header{Name: name, Size: size, Mode: 0o600})
}
