# jira-companion

Localhost-only Jira Cloud helper: log the day's hours, create tasks fast.

## Run

    cp .env.example .env     # fill JIRA_BASE_URL, JIRA_EMAIL, JIRA_API_TOKEN
    docker compose up -d --build
    open http://localhost:8080

Port is published on 127.0.0.1 only. The server also rejects non-localhost
Host/Origin headers. State (today's entries, remembered dropdown values) lives
in the `jira-companion-data` volume. The API token never reaches the browser.

## Usage

- Work day hours: `8h`, `7h 30m`. Last value is prefilled next day.
- Your open assigned tasks (status category not Done) are listed; type time as
  `1h 30m`, `45m`. Other tasks can be added via search.
- Complete day: if the total differs from work hours, task times are scaled
  proportionally (to the minute) to match, worklogs are posted to Jira, and the
  interface resets. If a worklog fails, already posted ones stay marked; press
  Complete day again to continue.
- New task: project, parent (optional), assignee, reporter, estimate, start,
  due. Dropdown choices are remembered. Issue type is chosen automatically:
  sub-task under a standard issue, Task otherwise (incl. under an epic). The
  created task is added to today's list.

## Dev

    go test ./...
