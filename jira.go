package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Jira struct {
	base string
	auth string
	hc   *http.Client

	mu         sync.Mutex
	me         *Ref
	startField string
	startDone  bool
}

func NewJira(base, email, token string) *Jira {
	return &Jira{
		base: strings.TrimRight(base, "/"),
		auth: "Basic " + b64(email+":"+token),
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
}

type JiraError struct {
	Status int
	Msg    string
}

func (e *JiraError) Error() string { return fmt.Sprintf("jira %d: %s", e.Status, e.Msg) }

func (j *Jira) do(method, path string, q url.Values, body, out any) error {
	u := j.base + path
	if q != nil {
		u += "?" + q.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", j.auth)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := j.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode >= 300 {
		return &JiraError{resp.StatusCode, errText(data, resp.Status)}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func errText(data []byte, fallback string) string {
	var e struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if json.Unmarshal(data, &e) != nil {
		return fallback
	}
	var parts []string
	parts = append(parts, e.ErrorMessages...)
	for k, v := range e.Errors {
		parts = append(parts, k+": "+v)
	}
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, "; ")
}

func (j *Jira) Me() (*Ref, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.me != nil {
		return j.me, nil
	}
	var r struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	if err := j.do("GET", "/rest/api/3/myself", nil, nil, &r); err != nil {
		return nil, err
	}
	j.me = &Ref{ID: r.AccountID, Label: r.DisplayName}
	return j.me, nil
}

type Issue struct {
	ID      string `json:"id"` // issue key
	Key     string `json:"key"`
	Summary string `json:"summary"`
	Label   string `json:"label"`
}

func (j *Jira) SearchIssues(jql string, max int) ([]Issue, error) {
	var r struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
			} `json:"fields"`
		} `json:"issues"`
	}
	q := url.Values{"jql": {jql}, "fields": {"summary"}, "maxResults": {fmt.Sprint(max)}}
	if err := j.do("GET", "/rest/api/3/search/jql", q, nil, &r); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(r.Issues))
	for _, i := range r.Issues {
		out = append(out, Issue{ID: i.Key, Key: i.Key, Summary: i.Fields.Summary, Label: i.Key + "  " + i.Fields.Summary})
	}
	return out, nil
}

func (j *Jira) MyActive() ([]Issue, error) {
	return j.SearchIssues(`assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC`, 30)
}

var (
	keyRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-\d+$`)
	wordSplit = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	projRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

func jqlStr(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// BuildIssueJQL builds a search for issues by key or summary prefix words.
func BuildIssueJQL(q, project string, noSubtasks bool) string {
	var conds []string
	if project != "" && projRe.MatchString(project) {
		conds = append(conds, "project = "+jqlStr(project))
	}
	if noSubtasks {
		conds = append(conds, "issuetype not in subTaskIssueTypes()")
	}
	q = strings.TrimSpace(q)
	var text []string
	for _, w := range wordSplit.Split(q, -1) {
		if w != "" {
			text = append(text, "summary ~ "+jqlStr(w+"*"))
		}
	}
	switch {
	case keyRe.MatchString(q):
		conds = append(conds, "(key = "+jqlStr(strings.ToUpper(q))+" OR ("+strings.Join(text, " AND ")+"))")
	case len(text) > 0:
		conds = append(conds, strings.Join(text, " AND "))
	}
	jql := strings.Join(conds, " AND ")
	return jql + " ORDER BY updated DESC"
}

func (j *Jira) SearchProjects(q string) ([]Ref, error) {
	var r struct {
		Values []struct{ Key, Name string } `json:"values"`
	}
	qs := url.Values{"query": {q}, "maxResults": {"20"}, "orderBy": {"name"}}
	if err := j.do("GET", "/rest/api/3/project/search", qs, nil, &r); err != nil {
		return nil, err
	}
	out := []Ref{}
	for _, p := range r.Values {
		out = append(out, Ref{ID: p.Key, Label: p.Key + "  " + p.Name})
	}
	return out, nil
}

func (j *Jira) SearchUsers(q, project string, assignable bool) ([]Ref, error) {
	qs := url.Values{"query": {q}, "maxResults": {"20"}}
	path := "/rest/api/3/user/search"
	if assignable && project != "" {
		path = "/rest/api/3/user/assignable/search"
		qs.Set("project", project)
	}
	var r []struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		AccountType string `json:"accountType"`
		Active      bool   `json:"active"`
	}
	if err := j.do("GET", path, qs, nil, &r); err != nil {
		return nil, err
	}
	out := []Ref{}
	for _, u := range r {
		if u.Active && (u.AccountType == "" || u.AccountType == "atlassian") {
			out = append(out, Ref{ID: u.AccountID, Label: u.DisplayName})
		}
	}
	return out, nil
}

// StartDateField finds the custom field id of "Start date" ("" if none).
func (j *Jira) StartDateField() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.startDone {
		return j.startField
	}
	var fs []struct{ ID, Name string }
	if err := j.do("GET", "/rest/api/3/field", nil, nil, &fs); err != nil {
		return ""
	}
	j.startDone = true
	for _, f := range fs {
		if strings.EqualFold(f.Name, "Start date") {
			j.startField = f.ID
			break
		}
	}
	return j.startField
}

type issueType struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Subtask        bool   `json:"subtask"`
	HierarchyLevel int    `json:"hierarchyLevel"`
}

// PickIssueType: subtask type when parent is a standard issue, otherwise a
// standard "Task" (also under an epic or with no parent).
func (j *Jira) PickIssueType(project, parent string) (string, error) {
	wantSub := false
	if parent != "" {
		var p struct {
			Fields struct {
				IssueType issueType `json:"issuetype"`
			} `json:"fields"`
		}
		if err := j.do("GET", "/rest/api/3/issue/"+url.PathEscape(parent), url.Values{"fields": {"issuetype"}}, nil, &p); err != nil {
			return "", err
		}
		t := p.Fields.IssueType
		wantSub = !t.Subtask && t.HierarchyLevel <= 0
		if t.Subtask {
			return "", fmt.Errorf("parent %s is a sub-task and cannot have children", parent)
		}
	}
	var r struct {
		IssueTypes []issueType `json:"issueTypes"`
		Values     []issueType `json:"values"`
	}
	if err := j.do("GET", "/rest/api/3/issue/createmeta/"+url.PathEscape(project)+"/issuetypes", nil, nil, &r); err != nil {
		return "", err
	}
	types := append(r.IssueTypes, r.Values...)
	best := ""
	for _, t := range types {
		if wantSub != t.Subtask || (!wantSub && t.HierarchyLevel != 0) {
			continue
		}
		if best == "" || strings.EqualFold(t.Name, "Task") || (wantSub && strings.EqualFold(t.Name, "Sub-task")) {
			best = t.ID
		}
	}
	if best == "" {
		kind := "Task"
		if wantSub {
			kind = "sub-task"
		}
		return "", fmt.Errorf("project %s has no %s issue type", project, kind)
	}
	return best, nil
}

func (j *Jira) CreateIssue(fields map[string]any) (string, error) {
	var r struct {
		Key string `json:"key"`
	}
	if err := j.do("POST", "/rest/api/3/issue", nil, map[string]any{"fields": fields}, &r); err != nil {
		return "", err
	}
	return r.Key, nil
}

func (j *Jira) AddWorklog(key string, minutes int, started time.Time, comment string) error {
	body := map[string]any{
		"timeSpentSeconds": minutes * 60,
		"started":          started.Format("2006-01-02T15:04:05.000-0700"),
	}
	if comment != "" {
		body["comment"] = map[string]any{"type": "doc", "version": 1, "content": []any{
			map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": comment}}},
		}}
	}
	return j.do("POST", "/rest/api/3/issue/"+url.PathEscape(key)+"/worklog", nil, body, nil)
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
