package fusionhttp

import (
	"fmt"

	"ikhnos/internal/fusionapi"
)

// The parts of the reference that are not one route: who may read what, how a time range and a limit are written, and how a failure
// looks. They are served inside the description (as x-guide, an extension a generic client ignores) and the page shows them beside the
// routes. The numbers that the code owns are taken from it, so they cannot drift from what the server does; the rest is checked by a test
// of the server (TestTheGuideOfTheDocsPageIsWhatTheServerDoes), which asks for each behaviour the text claims.
//
// The text is a small markdown: paragraphs, "- " lists, "|" tables, ``` blocks, "### " headings, **bold**, `code` and [text](#anchor).

func guide() []obj {
	maxDays := int(fusionapi.MaxWindow.Hours() / 24)
	searchDays := int(fusionapi.MaxSearchWindow.Hours() / 24)
	logDays := int(fusionapi.MaxLogWindow.Hours() / 24)
	section := func(id, title, body string) obj { return obj{"id": id, "title": title, "body": body} }
	return []obj{
		section("auth", "Credentials and what they may read", `Every route of the data API needs a credential, sent as `+"`Authorization: Bearer <credential>`"+`. Only this page and `+"`openapi.json`"+` do not. Three kinds work:

- **A FUSION access token** (`+"`cnf_...`"+`). An administrator of the organisation mints it and sees it once. It can only read, only the signals, namespaces and clusters it was minted with, and it expires. This is what a script, a Grafana datasource or a decision engine holds.
- **A personal access token** of an administrator of the organisation. It reads everything the administrator may.
- **A signed-in session** of such an administrator (the browser cookie), which is what this page uses while the token field is empty.

A credential sent on purpose beats a cookie the browser sends along: a token typed above reads with the token's rights even when you are signed in as an administrator. Ask [GET /status](#op-getStatus) what yours may read: its `+"`access`"+` says the signals, namespaces and clusters, whether raw queries are allowed, and when the token expires.

### What a token's limits do

| Limit | What it does |
| --- | --- |
| Signals (`+"`metrics`, `logs`, `traces`"+`) | A route that reads a signal the token lacks answers 403 (`+"`this token cannot read logs`"+`). A fused read leaves that part out and says `+"`not allowed`"+` for it in `+"`sources`"+`. |
| Namespaces | Only telemetry of the namespaces listed can be read. Telemetry that carries no namespace is invisible to a token with a namespace limit. Empty means every namespace. |
| Clusters | The same, by cluster. Empty means every cluster. |
| Raw queries | PromQL, LogQL and TraceQL as written (`+"`query`, `q`, `promql`"+` and the `+"`/prometheus`, `/loki`, `/tempo`"+` routes) cannot be narrowed to a namespace or cluster, so they are for a credential with neither limit. Any other answers 403 and says to use the structured filters. |

A signed-in person who is not an administrator of the organisation, or who has still to choose a new password, answers 403 too. A missing, unknown or expired credential answers 401 `+"`sign in required`"+`; the same text for each, so it does not tell which.`),

		section("conventions", "Time ranges, durations and limits", fmt.Sprintf(`### The time range

`+"`from`"+` and `+"`to`"+` take an RFC 3339 time, unix seconds, or `+"`now`"+` and `+"`now-<duration>`"+` such as `+"`now-15m`"+`. A missing `+"`to`"+` is now, and a missing `+"`from`"+` is %d hour before `+"`to`"+`. `+"`from`"+` must be before `+"`to`"+`. One request reads at most %d days; a trace search reads at most %d and a log read at most %d at a time, and a longer range is a 400 that says so.

The lists answer with the range they covered as `+"`from`"+` and `+"`to`"+`, so a relative range can be repeated exactly.

### Durations

`+"`step`, `pad`, `window`, `min_duration`"+` and the like take a duration (`+"`500ms`, `30s`, `2m`, `1h`, `7d`, `2w`, `1w3d12h`"+`) or plain seconds. Negative durations are refused.

### Limits and truncation

`+"`limit`"+` is the most results; each route states its default and maximum in its parameters, and a larger value is cut to the maximum. A value that is not a whole number of at least 1 is a 400. The list answers carry the `+"`limit`"+` that was applied and `+"`truncated`"+`: true when the store had more than that. There is no cursor and no next page, so a truncated answer is narrowed (a tighter range, a filter, `+"`application`"+`) or, for logs, `+"`order`"+` picks which end of the range is kept.

### Reading what is not one signal

An answer built from several stores (a fused trace, the lists of applications and services) can succeed in part. `+"`sources`"+` says for each part `+"`ok`"+`, `+"`not requested`"+`, `+"`not allowed`"+`, `+"`unavailable`"+` or `+"`error`"+`, and `+"`warnings`"+` says why in words. In a batch each trace has its own `+"`status`"+`.`,
			int(fusionapi.DefaultWindow.Hours()), maxDays, searchDays, logDays)),

		section("errors", "Errors", fmt.Sprintf(`Every refusal is JSON with the same one field, whatever the status:

`+"```"+`
{"error": "this route does not take \"frm\"; it takes no parameters"}
`+"```"+`

The message is meant to be read by a person: it says what was wrong and, where there is one, what to do instead. On the routes of this API (the backends' own routes forward what they are sent) a parameter a route does not list is a 400 and not silently dropped, so a typo is told.

| Status | When | What to do |
| --- | --- | --- |
| 400 | A parameter is unknown, malformed or out of range, two that exclude each other are given, or a required one is missing. | Fix the request; repeating it will not help. |
| 401 | No credential, or one that is unknown or expired. | Send a valid one. |
| 403 | The credential is valid but may not read this (its signals, namespaces or clusters, a raw query with a limited token, or not an administrator). | Use a credential with the rights, or the structured filters. |
| 404 | No such application, service or trace, or none that this credential may see. | Check the name and the range. |
| 429 | Too many reads: the limit is per credential, and a batch of n traces counts as n. The answer carries `+"`Retry-After: 5`"+`. Too many failed sign-ins from one address is a 429 too, without that header. | Wait for the seconds given, or a minute for failed sign-ins. |
| 503 | FUSION is off, not installed, still starting, or a store cannot be reached. No `+"`Retry-After`"+` is sent. | Ask [GET /status](#op-getStatus) which part is down, and try again in a few seconds. |
| 504 | The request used its %d seconds, or a store took too long. | Narrow the time range or the filters. |

On the `+"`/prometheus`, `/loki`, `/tempo`"+` routes a backend's own answer to your query (including its 400, 404 and 422 and the error shape it uses) is passed on unchanged, so a client written for it keeps working. FUSION adds 422 when a store's answer is larger than this API returns (narrow the range or the query) and 502 when a store failed and its own text is not repeated.

When a fused read cannot read one store it still answers 200 with the rest, and says what was left out in `+"`sources`"+` and `+"`warnings`"+`. When the request's time budget runs out in a batch, the traces not yet read come back with their own `+"`status`"+` 504 inside a 200.`,
			int(RequestTimeout.Seconds()))),
	}
}
