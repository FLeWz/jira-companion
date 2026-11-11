package main

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

type WeekRow struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
	Prev    int    `json:"prev"` // minutes, previous week
	Cur     int    `json:"cur"`  // minutes, current week
}

type WeeksDTO struct {
	PrevFrom string    `json:"prevFrom"`
	CurFrom  string    `json:"curFrom"`
	CurTo    string    `json:"curTo"`
	Rows     []WeekRow `json:"rows"`
	Prev     int       `json:"prev"`
	Cur      int       `json:"cur"`
}

// weekStart returns Monday 00:00 local of t's week.
func weekStart(t time.Time) time.Time {
	off := (int(t.Weekday()) + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day()-off, 0, 0, 0, 0, t.Location())
}

func (j *Jira) worklogs(key, accountID string, from, to time.Time) ([]struct {
	Started time.Time
	Minutes int
}, error) {
	var r struct {
		Worklogs []struct {
			Author struct {
				AccountID string `json:"accountId"`
			} `json:"author"`
			Started          string `json:"started"`
			TimeSpentSeconds int    `json:"timeSpentSeconds"`
		} `json:"worklogs"`
	}
	q := url.Values{
		"startedAfter":  {strconv.FormatInt(from.UnixMilli()-1, 10)},
		"startedBefore": {strconv.FormatInt(to.UnixMilli(), 10)},
		"maxResults":    {"1000"},
	}
	if err := j.do("GET", "/rest/api/3/issue/"+url.PathEscape(key)+"/worklog", q, nil, &r); err != nil {
		return nil, err
	}
	var out []struct {
		Started time.Time
		Minutes int
	}
	for _, w := range r.Worklogs {
		if w.Author.AccountID != accountID {
			continue
		}
		t, err := time.Parse("2006-01-02T15:04:05.000-0700", w.Started)
		if err != nil {
			continue
		}
		out = append(out, struct {
			Started time.Time
			Minutes int
		}{t, w.TimeSpentSeconds / 60})
	}
	return out, nil
}

func (a *app) handleWeeks(w http.ResponseWriter, r *http.Request) {
	me, err := a.jira.Me()
	if err != nil {
		jiraErr(w, err)
		return
	}
	now := time.Now()
	cur := weekStart(now)
	prev := cur.AddDate(0, 0, -7)
	end := cur.AddDate(0, 0, 7)
	const d = "2006-01-02"

	jql := `worklogAuthor = currentUser() AND worklogDate >= "` + prev.Format(d) + `" AND worklogDate < "` + end.Format(d) + `" ORDER BY key`
	issues, err := a.jira.SearchIssues(jql, 100)
	if err != nil {
		jiraErr(w, err)
		return
	}

	rows := make([]WeekRow, len(issues))
	errs := make([]error, len(issues))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, is := range issues {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = WeekRow{Key: is.Key, Summary: is.Summary}
			wl, err := a.jira.worklogs(is.Key, me.ID, prev, end)
			if err != nil {
				errs[i] = err
				return
			}
			for _, l := range wl {
				if l.Started.Before(cur) {
					rows[i].Prev += l.Minutes
				} else {
					rows[i].Cur += l.Minutes
				}
			}
		}()
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			jiraErr(w, e)
			return
		}
	}

	out := WeeksDTO{PrevFrom: prev.Format(d), CurFrom: cur.Format(d), CurTo: end.AddDate(0, 0, -1).Format(d), Rows: []WeekRow{}}
	for _, row := range rows {
		if row.Prev == 0 && row.Cur == 0 {
			continue
		}
		out.Rows = append(out.Rows, row)
		out.Prev += row.Prev
		out.Cur += row.Cur
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Key < out.Rows[j].Key })
	writeJSON(w, 200, out)
}
