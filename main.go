package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

type app struct {
	st   *Store
	jira *Jira
	base string
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	base, email, token := os.Getenv("JIRA_BASE_URL"), os.Getenv("JIRA_EMAIL"), os.Getenv("JIRA_API_TOKEN")
	if base == "" || email == "" || token == "" {
		log.Fatal("JIRA_BASE_URL, JIRA_EMAIL and JIRA_API_TOKEN must be set")
	}
	st, err := OpenStore(env("DATA_DIR", "./data"))
	if err != nil {
		log.Fatal(err)
	}
	a := &app{st: st, jira: NewJira(base, email, token), base: strings.TrimRight(base, "/")}

	sub, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/me", a.handleMe)
	mux.HandleFunc("GET /api/search/projects", a.handleProjects)
	mux.HandleFunc("GET /api/search/issues", a.handleIssues)
	mux.HandleFunc("GET /api/search/users", a.handleUsers)
	mux.HandleFunc("GET /api/day", a.handleDayGet)
	mux.HandleFunc("PUT /api/day", a.handleDayPut)
	mux.HandleFunc("POST /api/day/refresh", a.handleRefresh)
	mux.HandleFunc("POST /api/day/complete", a.handleComplete)
	mux.HandleFunc("POST /api/tasks", a.handleCreate)
	mux.HandleFunc("GET /api/weeks", a.handleWeeks)

	addr := env("LISTEN_ADDR", "127.0.0.1:8080")
	log.Printf("jira-companion listening on %s (jira %s)", addr, a.base)
	log.Fatal(http.ListenAndServe(addr, localOnly(mux)))
}

func isLocalHost(h string) bool {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// localOnly rejects foreign Host headers (DNS rebinding) and cross-site writes.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLocalHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || !isLocalHost(u.Host) {
					http.Error(w, "forbidden origin", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func jiraErr(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	var je *JiraError
	if errors.As(err, &je) && je.Status >= 400 && je.Status < 500 {
		code = http.StatusBadRequest
	}
	writeErr(w, code, err)
}

func (a *app) handleMe(w http.ResponseWriter, r *http.Request) {
	me, err := a.jira.Me()
	if err != nil {
		jiraErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"me": me, "baseUrl": a.base})
}

func (a *app) handleProjects(w http.ResponseWriter, r *http.Request) {
	res, err := a.jira.SearchProjects(r.URL.Query().Get("q"))
	if err != nil {
		jiraErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// /api/search/issues?q=&project=&parents=1
func (a *app) handleIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	jql := BuildIssueJQL(q.Get("q"), q.Get("project"), q.Get("parents") == "1")
	res, err := a.jira.SearchIssues(jql, 20)
	if err != nil {
		jiraErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// /api/search/users?q=&project=&kind=assignee|reporter
func (a *app) handleUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := a.jira.SearchUsers(q.Get("q"), q.Get("project"), q.Get("kind") == "assignee")
	if err != nil {
		jiraErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// ---- day ----

type EntryDTO struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Summary string `json:"summary"`
	Time    string `json:"time"`
	Comment string `json:"comment"`
	Logged  bool   `json:"logged"`
}

type DayDTO struct {
	Date     string     `json:"date"`
	Workday  string     `json:"workday"`
	Entries  []EntryDTO `json:"entries"`
	Remember Remember   `json:"remember"`
	Warning  string     `json:"warning,omitempty"`
}

// dto builds the response. Lock held.
func (a *app) dto(warning string) DayDTO {
	d := DayDTO{Date: a.st.S.Date, Workday: FormatMinutes(a.st.S.WorkdayMin), Remember: a.st.S.Remember, Warning: warning, Entries: []EntryDTO{}}
	for _, e := range a.st.S.Entries {
		d.Entries = append(d.Entries, EntryDTO{e.ID, e.Key, e.Summary, FormatMinutes(e.Minutes), e.Comment, e.Logged})
	}
	return d
}

// mergeMine adds active Jira tasks not yet on the day. Lock held.
func (a *app) mergeMine() error {
	issues, err := a.jira.MyActive()
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, e := range a.st.S.Entries {
		have[e.Key] = true
	}
	for _, i := range issues {
		if !have[i.Key] {
			a.st.S.Entries = append(a.st.S.Entries, Entry{ID: NewID(), Key: i.Key, Summary: i.Summary})
		}
	}
	a.st.S.Loaded = true
	return a.st.Save()
}

func (a *app) handleDayGet(w http.ResponseWriter, r *http.Request) {
	a.st.Lock()
	defer a.st.Unlock()
	a.st.Rollover()
	warn := ""
	if !a.st.S.Loaded {
		if err := a.mergeMine(); err != nil {
			warn = "could not load active tasks: " + err.Error()
		}
	}
	writeJSON(w, 200, a.dto(warn))
}

func (a *app) handleRefresh(w http.ResponseWriter, r *http.Request) {
	a.st.Lock()
	defer a.st.Unlock()
	a.st.Rollover()
	if err := a.mergeMine(); err != nil {
		jiraErr(w, err)
		return
	}
	writeJSON(w, 200, a.dto(""))
}

func (a *app) handleDayPut(w http.ResponseWriter, r *http.Request) {
	var in DayDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	wd, err := ParseMinutes(in.Workday)
	if err != nil || wd > 24*60 {
		writeErr(w, 400, fmt.Errorf("invalid work hours %q", in.Workday))
		return
	}
	a.st.Lock()
	defer a.st.Unlock()
	a.st.Rollover()
	old := map[string]Entry{}
	for _, e := range a.st.S.Entries {
		old[e.ID] = e
	}
	var entries []Entry
	for _, e := range in.Entries {
		if prev, ok := old[e.ID]; ok && e.ID != "" && prev.Logged {
			entries = append(entries, prev) // logged entries are frozen
			continue
		}
		m, err := ParseMinutes(e.Time)
		if err != nil {
			writeErr(w, 400, fmt.Errorf("%s: %w", e.Key, err))
			return
		}
		id := e.ID
		if id == "" {
			id = NewID()
		}
		entries = append(entries, Entry{ID: id, Key: e.Key, Summary: e.Summary, Minutes: m, Comment: strings.TrimSpace(e.Comment)})
	}
	a.st.S.WorkdayMin = wd
	a.st.S.Entries = entries
	if err := a.st.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, a.dto(""))
}

type completeResp struct {
	Adjusted bool   `json:"adjusted"`
	Logged   int    `json:"logged"`  // minutes
	Entries  int    `json:"entries"` // worklogs posted
	Day      DayDTO `json:"day"`
}

func (a *app) handleComplete(w http.ResponseWriter, r *http.Request) {
	a.st.Lock()
	defer a.st.Unlock()
	a.st.Rollover()
	s := &a.st.S
	if s.WorkdayMin <= 0 {
		writeErr(w, 400, errors.New("set work day hours first"))
		return
	}
	loaded := 0
	var idx, mins []int
	for i, e := range s.Entries {
		switch {
		case e.Logged:
			loaded += e.Minutes
		case e.Minutes > 0:
			idx = append(idx, i)
			mins = append(mins, e.Minutes)
		}
	}
	if len(idx) == 0 {
		writeErr(w, 400, errors.New("enter time on at least one task"))
		return
	}
	target := s.WorkdayMin - loaded
	if target <= 0 {
		writeErr(w, 400, errors.New("already logged more than the work day"))
		return
	}
	sum := 0
	for _, m := range mins {
		sum += m
	}
	adjusted := sum != target
	if adjusted {
		for k, v := range Adjust(mins, target) {
			s.Entries[idx[k]].Minutes = v
		}
	}
	if err := a.st.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}

	// Worklogs stack from 08:00 so they do not overlap; clamp to stay on today.
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
	for _, e := range s.Entries {
		if e.Logged {
			start = start.Add(time.Duration(e.Minutes) * time.Minute)
		}
	}
	posted, postedMin := 0, 0
	for _, i := range idx {
		e := &s.Entries[i]
		if e.Minutes == 0 {
			continue
		}
		st := start
		if st.YearDay() != now.YearDay() || st.Day() != now.Day() {
			st = time.Date(now.Year(), now.Month(), now.Day(), 23, 0, 0, 0, now.Location())
		}
		if err := a.jira.AddWorklog(e.Key, e.Minutes, st, e.Comment); err != nil {
			a.st.Save()
			writeJSON(w, 502, map[string]any{
				"error": fmt.Sprintf("%s: %v (%d of %d worklogs saved, retry to continue)", e.Key, err, posted, len(idx)),
				"day":   a.dto(""),
			})
			return
		}
		e.Logged = true
		posted++
		postedMin += e.Minutes
		start = start.Add(time.Duration(e.Minutes) * time.Minute)
		a.st.Save()
	}

	s.Remember.Workday = s.WorkdayMin
	a.st.resetDay()
	if err := a.st.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, completeResp{Adjusted: adjusted, Logged: postedMin, Entries: posted, Day: a.dto("")})
}

// ---- create task ----

type createReq struct {
	Summary  string `json:"summary"`
	Project  *Ref   `json:"project"`
	Parent   *Ref   `json:"parent"`
	Assignee *Ref   `json:"assignee"`
	Reporter *Ref   `json:"reporter"`
	Estimate string `json:"estimate"`
	Start    string `json:"start"`
	Due      string `json:"due"`
}

func validDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func (a *app) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in createReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	in.Summary = strings.TrimSpace(in.Summary)
	switch {
	case in.Summary == "":
		writeErr(w, 400, errors.New("summary required"))
		return
	case in.Project == nil || in.Project.ID == "":
		writeErr(w, 400, errors.New("project required"))
		return
	}
	for _, d := range []string{in.Start, in.Due} {
		if d != "" && !validDate(d) {
			writeErr(w, 400, fmt.Errorf("invalid date %q", d))
			return
		}
	}
	if in.Start != "" && in.Due != "" && in.Due < in.Start {
		writeErr(w, 400, errors.New("due date before start date"))
		return
	}
	est, err := ParseMinutes(in.Estimate)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("estimate: %w", err))
		return
	}
	parent := ""
	if in.Parent != nil {
		parent = in.Parent.ID
	}
	typeID, err := a.jira.PickIssueType(in.Project.ID, parent)
	if err != nil {
		jiraErr(w, err)
		return
	}
	f := map[string]any{
		"project":   map[string]any{"key": in.Project.ID},
		"issuetype": map[string]any{"id": typeID},
		"summary":   in.Summary,
	}
	if parent != "" {
		f["parent"] = map[string]any{"key": parent}
	}
	if in.Assignee != nil && in.Assignee.ID != "" {
		f["assignee"] = map[string]any{"accountId": in.Assignee.ID}
	}
	if in.Reporter != nil && in.Reporter.ID != "" {
		f["reporter"] = map[string]any{"accountId": in.Reporter.ID}
	}
	if est > 0 {
		f["timetracking"] = map[string]any{"originalEstimate": FormatMinutes(est)}
	}
	if in.Due != "" {
		f["duedate"] = in.Due
	}
	startField := ""
	if in.Start != "" {
		if startField = a.jira.StartDateField(); startField != "" {
			f[startField] = in.Start
		}
	}

	warning := ""
	key, err := a.jira.CreateIssue(f)
	if err != nil && startField != "" && strings.Contains(err.Error(), startField) {
		delete(f, startField)
		warning = "start date not on project's create screen, skipped"
		key, err = a.jira.CreateIssue(f)
	}
	if err != nil {
		jiraErr(w, err)
		return
	}

	a.st.Lock()
	defer a.st.Unlock()
	a.st.Rollover()
	rm := &a.st.S.Remember
	rm.Project, rm.Parent, rm.Assignee, rm.Reporter = in.Project, in.Parent, in.Assignee, in.Reporter
	a.st.S.Entries = append(a.st.S.Entries, Entry{ID: NewID(), Key: key, Summary: in.Summary})
	if err := a.st.Save(); err != nil {
		log.Printf("save: %v", err)
	}
	writeJSON(w, 200, map[string]any{"key": key, "url": a.base + "/browse/" + key, "warning": warning})
}
