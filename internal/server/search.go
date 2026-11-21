package server

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/session"
	"github.com/whyrusleeping/ycc/internal/sessionview"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// searchWait bounds how long SearchSessions waits for the index refresh it
// started before answering from what is indexed so far.
var searchWait = 1500 * time.Millisecond

const (
	defaultSearchHits = 20
	maxSearchHits     = 100
)

// projectSearch is one project's search index and its refresh passes. A pass
// follows every session in the project's history; a request arriving during
// a pass waits on the next one, so its results include appends made before it.
type projectSearch struct {
	root    string
	mu      sync.Mutex
	index   *sessionview.SearchIndex // nil until a pass opens it
	running bool
	next    chan struct{} // closed when the queued pass ends
	indexed int
	total   int
}

func (s *Server) projectSearch(root string) *projectSearch {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if p := s.searches[root]; p != nil {
		return p
	}
	if s.searches == nil {
		s.searches = make(map[string]*projectSearch)
	}
	p := &projectSearch{root: root}
	s.searches[root] = p
	return p
}

// openIndex (re)opens the database when it is not open yet or its file was
// deleted or replaced, which rebuilds it from the logs. Only passes call it,
// and they never overlap.
func (p *projectSearch) openIndex() (*sessionview.SearchIndex, error) {
	p.mu.Lock()
	index := p.index
	p.mu.Unlock()
	if index != nil && !index.Stale() {
		return index, nil
	}
	if index != nil {
		// Close waits for in-flight reads; it also removes the old WAL.
		index.Close()
	}
	fresh, err := sessionview.OpenSearch(filepath.Join(p.root, ".ycc", "session-search.sqlite"))
	p.mu.Lock()
	p.index = fresh // nil on error: the next pass retries
	p.mu.Unlock()
	return fresh, err
}

func (p *projectSearch) current() *sessionview.SearchIndex {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.index
}

// refreshSearch returns a channel closed when a refresh pass that started no
// earlier than this call has finished.
func (s *Server) refreshSearch(project string, p *projectSearch) <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		if p.next == nil {
			p.next = make(chan struct{})
		}
		return p.next
	}
	p.running = true
	done := make(chan struct{})
	go func() {
		for {
			s.searchPass(project, p)
			p.mu.Lock()
			close(done)
			done, p.next = p.next, nil
			if done == nil {
				p.running = false
				p.mu.Unlock()
				return
			}
			p.mu.Unlock()
		}
	}()
	return done
}

// searchPass indexes every session in the project's history, newest first,
// within each live log's durable boundary, then drops sessions whose logs are
// gone.
//
// indexed counts sessions that are current in the index, so a pass whose
// writes fail ends with indexed < total.
func (s *Server) searchPass(project string, p *projectSearch) {
	ctx := context.Background()
	index, err := p.openIndex()
	if err != nil {
		log.Printf("ycc: session search: %s: open index: %v", project, err)
		return
	}
	sums, err := s.mgr.ListSessionHistory(project)
	if err != nil {
		log.Printf("ycc: session search: %s: %v", project, err)
		return
	}
	p.setProgress(0, len(sums))
	keep := make(map[string]bool, len(sums))
	indexed := 0
	for _, sum := range sums {
		keep[sum.ID] = true
		_, logPath, seq, offset, err := s.mgr.ProjectSessionLog(p.root, sum.ID)
		switch {
		case err == nil:
			err = index.IndexSession(ctx, sum.ID, logPath, seq, offset)
		case errors.Is(err, session.ErrUnknownSession) || errors.Is(err, os.ErrNotExist):
			// A live session that has not written its log yet.
			err = nil
		}
		if err != nil {
			log.Printf("ycc: session search: %s/%s: %v", project, sum.ID, err)
		} else {
			indexed++
		}
		p.setProgress(indexed, len(sums))
	}
	if err := index.Prune(ctx, keep); err != nil {
		log.Printf("ycc: session search: %s: prune: %v", project, err)
	}
}

func (p *projectSearch) setProgress(indexed, total int) {
	p.mu.Lock()
	p.indexed, p.total = indexed, total
	p.mu.Unlock()
}

func (p *projectSearch) progress() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.indexed, p.total
}

// SearchSessions refreshes the project's search index, waits up to searchWait
// for that refresh, and returns the best match of each matching session.
func (s *Server) SearchSessions(ctx context.Context, req *connect.Request[v1.SearchSessionsRequest]) (*connect.Response[v1.SearchSessionsResponse], error) {
	offset, err := sessionview.DecodeSearchCursor(req.Msg.Cursor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = defaultSearchHits
	}
	if limit > maxSearchHits {
		limit = maxSearchHits
	}
	root, err := s.mgr.ProjectWorkspace(req.Msg.Project)
	if err != nil {
		if errors.Is(err, session.ErrUnknownProject) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	p := s.projectSearch(root)
	done := s.refreshSearch(req.Msg.Project, p)
	timer := time.NewTimer(searchWait)
	defer timer.Stop()
	out := &v1.SearchSessionsResponse{}
	select {
	case <-done:
	case <-timer.C:
		out.Indexing = true
	case <-ctx.Done():
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	indexed, total := p.progress()
	out.IndexedSessions, out.TotalSessions = int32(indexed), int32(total)
	index := p.current()
	if index == nil {
		if out.Indexing {
			return connect.NewResponse(out), nil // still opening
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("session search index could not be opened"))
	}
	hits, more, err := index.Search(ctx, req.Msg.Query, offset, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if more {
		out.NextCursor = sessionview.EncodeSearchCursor(offset + limit)
	}
	if len(hits) == 0 {
		return connect.NewResponse(out), nil
	}
	sums, err := s.mgr.ListSessionHistory(req.Msg.Project)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	byID := make(map[string]session.SessionSummary, len(sums))
	for _, sum := range sums {
		byID[sum.ID] = sum
	}
	for _, h := range hits {
		sum, ok := byID[h.SessionID]
		if !ok {
			continue // log removed since the last pass pruned
		}
		hit := &v1.SessionSearchHit{Session: sessionSummaryProto(sum), RowId: h.RowID, Kind: h.Kind,
			MatchCount: int32(h.Matches), Timestamp: rfc3339(h.TS)}
		for _, span := range h.Snippet {
			hit.Snippet = append(hit.Snippet, &v1.SessionSearchSpan{Text: span.Text, Match: span.Match})
		}
		out.Hits = append(out.Hits, hit)
	}
	return connect.NewResponse(out), nil
}
