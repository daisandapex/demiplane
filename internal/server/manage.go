// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/daisandapex/demiplane/v3/internal/logsafe"
	"github.com/daisandapex/demiplane/v3/internal/store"
)

// Management operations fail closed even on an otherwise open server. The
// control-origin write check also applies in the legacy combined topology.
func (s *Server) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.isCrossOriginWrite(r) {
			http.Error(w, "cross-origin write rejected", http.StatusForbidden)
			return
		}
		if s.authToken == "" {
			http.Error(w, "configure a token to use owner management", http.StatusForbidden)
			return
		}
		s.requireAuth(next)(w, r)
	}
}

func (s *Server) routeList(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("all") == "1" {
		s.requireOwner(s.handleOwnerList)(w, r)
		return
	}
	s.requireAuth(s.handleList)(w, r)
}

func artifactListItem(a store.Artifact, base string) listItem {
	item := listItem{
		Slug: a.Slug, URL: base + "/" + a.Slug, Filename: a.Filename,
		ContentType: a.ContentType, Size: a.Size, CreatedAt: a.CreatedAt.Format(time.RFC3339),
		Owner: a.Owner, Private: a.Private, Password: a.HasPassword(),
		ExpiresAt: rfc3339OrEmpty(a.ExpiresAt), Series: a.Series, Kind: "artifact", Render: "raw", Reply: a.Replies,
	}
	if a.RenderSpec != "" {
		item.Render = "md"
	}
	return item
}

func (s *Server) handleOwnerList(w http.ResponseWriter, r *http.Request) {
	phase, after := "artifact", ""
	var rowID int64
	if cursor := r.URL.Query().Get("after"); cursor != "" {
		var ok bool
		phase, after, ok = strings.Cut(cursor, ":")
		valid := false
		if phase == "artifact" {
			var err error
			rowID, err = strconv.ParseInt(after, 10, 64)
			valid = err == nil && rowID > 0 && strconv.FormatInt(rowID, 10) == after
		} else if phase == "site" {
			valid = store.ValidateNamedSlug(after) == nil
		}
		if !ok || !valid {
			http.Error(w, "bad after cursor", http.StatusBadRequest)
			return
		}
	}
	items := make([]listItem, 0, listPageSize+1)
	base := s.contentBase(r)
	var artifactCursor string
	if phase == "artifact" {
		arts, err := s.store.OwnerArtifacts(r.Context(), store.DefaultOwner, rowID, listPageSize+1)
		if err != nil {
			managementFailure(w, err)
			return
		}
		for i, a := range arts {
			items = append(items, artifactListItem(a.Artifact, base))
			if i == listPageSize-1 {
				artifactCursor = "artifact:" + strconv.FormatInt(a.RowID, 10)
			}
		}
	}
	if len(items) <= listPageSize {
		sites, err := s.store.ListSites()
		if err != nil {
			managementFailure(w, err)
			return
		}
		for _, name := range sites {
			if phase == "site" && name <= after {
				continue
			}
			items = append(items, listItem{Slug: name, URL: base + "/" + name + "/", Filename: name, Owner: store.DefaultOwner, Kind: "site", Render: "raw"})
			if len(items) > listPageSize {
				break
			}
		}
	}
	resp := map[string]any{"owner": true}
	if len(items) > listPageSize {
		items = items[:listPageSize]
		last := items[len(items)-1]
		if last.Kind == "artifact" {
			resp["next"] = artifactCursor
		} else {
			resp["next"] = "site:" + last.Slug
		}
	}
	resp["artifacts"], resp["count"] = items, len(items)
	writeJSON(w, http.StatusOK, resp)
}

func managementFailure(w http.ResponseWriter, err error) {
	log.Printf("owner management failed: %s", logsafe.Err(err))
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if store.ValidateNamedSlug(name) != nil {
		http.Error(w, "invalid site name", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteSite(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		managementFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Redirect only the split content root. Compare both advertised and direct
// listener hosts, including ports, so proxy configuration cannot create a loop.
func (s *Server) handleContentRoot(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	direct := scheme + "://" + r.Host
	content := direct
	if s.contentBaseURL != "" || s.contentPort != "" {
		content = s.contentBase(r)
	}
	if redirectHostsDiffer(s.rootRedirect, direct) && redirectHostsDiffer(s.rootRedirect, content) {
		w.Header().Set("Location", s.rootRedirect)
		w.WriteHeader(http.StatusFound)
		return
	}

	s.serveContentNotFound(w, r)
}

// Refuse both equivalent default-port hosts and identical effective ports.
// The latter also catches a scheme change on an explicitly written port.
func redirectHostsDiffer(destination, content string) bool {
	dest, destOK := redirectHost(destination, false)
	host, hostOK := redirectHost(content, false)
	if !destOK || !hostOK || dest == host {
		return false
	}
	dest, _ = redirectHost(destination, true)
	host, _ = redirectHost(content, true)
	return dest != host
}

func redirectHost(raw string, keepDefaultPort bool) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	_, ok := normalizeOrigin(raw)
	if !ok {
		return "", false
	}
	// Ignore scheme when comparing the destination with the content host.
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return "", false
		}
		port = strconv.FormatUint(n, 10)
	}
	defaultPort := "80"
	if u.Scheme == "https" {
		defaultPort = "443"
	}
	if port == "" {
		port = defaultPort
	}
	if keepDefaultPort || port != defaultPort {
		host = net.JoinHostPort(host, port)
	}
	return host, true
}
