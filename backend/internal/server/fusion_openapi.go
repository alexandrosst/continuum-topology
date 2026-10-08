package server

import (
	"embed"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// The OpenAPI description of the data API and the page that renders it. Both are built from fusion_ops.go, so the page
// can never describe a route or a parameter the server does not have, nor miss one it has (fusion_openapi_test.go checks
// both directions).

//go:embed fusion_docs
var fusionDocsFS embed.FS

// fusionDocsCSP lets the docs page run its own script and style and call this server, and nothing else: no inline
// script, nothing from another host.
const fusionDocsCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src data:; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

var (
	specOnce sync.Once
	specJSON []byte
)

func (a *Admin) registerFusionDocs(api *http.ServeMux) {
	asset := func(name, ctype string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := fusionDocsFS.ReadFile("fusion_docs/" + name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", ctype)
			w.Header().Set("Content-Security-Policy", fusionDocsCSP)
			_, _ = w.Write(b)
		})
	}
	// Describing the API is not reading data: these need no credential. What they show is what the code would tell anyone.
	api.Handle("GET "+fusionAPIPath+"/docs", asset("index.html", "text/html; charset=utf-8"))
	api.Handle("GET "+fusionAPIPath+"/docs/app.js", asset("app.js", "text/javascript; charset=utf-8"))
	api.Handle("GET "+fusionAPIPath+"/docs/app.css", asset("app.css", "text/css; charset=utf-8"))
	api.HandleFunc("GET "+fusionAPIPath+"/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		specOnce.Do(func() { specJSON, _ = json.Marshal(fusionOpenAPI()) })
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(specJSON)
	})
}

type obj = map[string]any

func paramSchema(p fusionParam) obj {
	item := obj{"type": p.Type}
	if len(p.Enum) > 0 {
		item["enum"] = p.Enum
	}
	if p.List || p.Multi {
		return obj{"type": "array", "items": item}
	}
	return item
}

func openAPIParam(name, in string, p fusionParam, note string) obj {
	desc := p.Desc
	if note != "" {
		desc = note
	}
	o := obj{"name": name, "in": in, "description": desc, "schema": paramSchema(p)}
	if in == "path" {
		o["required"] = true
	}
	if p.List {
		o["style"], o["explode"] = "form", false
	}
	if p.Multi {
		o["style"], o["explode"], o["x-multiline"] = "form", true, true
	}
	if p.Default != "" {
		o["x-default"] = p.Default
	}
	if p.Example != "" {
		o["example"] = p.Example
	}
	return o
}

// fusionOpenAPI is the OpenAPI 3.0 description of the data API, generated from fusionOps.
func fusionOpenAPI() obj {
	var schemas obj
	raw, err := fusionDocsFS.ReadFile("fusion_docs/schemas.json")
	if err == nil {
		err = json.Unmarshal(raw, &schemas)
	}
	if err != nil {
		panic("fusion_docs/schemas.json: " + err.Error()) // it is embedded: a test catches this long before a user would
	}
	errResp := func(desc string) obj {
		return obj{"description": desc, "content": obj{"application/json": obj{"schema": obj{"$ref": "#/components/schemas/Error"}}}}
	}
	paths := obj{}
	tagSeen := map[string]bool{}
	var tags []obj
	for i, op := range fusionOps(nil) {
		if !tagSeen[op.Tag] {
			tagSeen[op.Tag] = true
			tags = append(tags, obj{"name": op.Tag})
		}
		var params []obj
		for _, n := range op.PathParams {
			params = append(params, openAPIParam(n, "path", fusionPathParams[n], ""))
		}
		for _, n := range op.Params {
			params = append(params, openAPIParam(n, "query", fusionParams[n], op.Notes[n]))
		}
		ok := obj{"description": "OK", "content": obj{"application/json": obj{"schema": obj{"$ref": "#/components/schemas/" + op.Response}}}}
		if op.Stream {
			ok["content"].(obj)["application/x-ndjson"] = obj{"schema": obj{"type": "string", "description": "One JSON object per line."}}
		}
		o := obj{"tags": []string{op.Tag}, "summary": op.Summary, "operationId": opID(op), "parameters": params, "x-order": i,
			"responses": obj{"200": ok, "400": obj{"$ref": "#/components/responses/BadRequest"}, "401": obj{"$ref": "#/components/responses/Unauthorized"},
				"403": obj{"$ref": "#/components/responses/Forbidden"}, "429": obj{"$ref": "#/components/responses/TooManyRequests"},
				"503": obj{"$ref": "#/components/responses/Unavailable"}, "504": obj{"$ref": "#/components/responses/Timeout"}}}
		if op.Description != "" {
			o["description"] = op.Description
		}
		if op.Body != "" {
			o["requestBody"] = obj{"required": true, "content": obj{"application/json": obj{
				"schema": obj{"$ref": "#/components/schemas/" + op.Body}, "example": json.RawMessage(op.BodyExample)}}}
		}
		path := fusionAPIPath + op.Path
		if paths[path] == nil {
			paths[path] = obj{}
		}
		paths[path].(obj)[strings.ToLower(op.Method)] = o
	}
	return obj{
		"openapi": "3.0.3",
		"info": obj{"title": "FUSION data API", "version": "1",
			"description": "Read the metrics, logs and traces FUSION saved, one signal at a time or joined around a trace, with a credential that can read nothing else.\n\n" +
				"**Credentials.** A FUSION access token (`cnf_...`, minted by an administrator, read-only, limited to the signals, namespaces and clusters it names, and it expires), sent as `Authorization: Bearer <token>`. An administrator of the organisation can also use a personal access token, or a signed-in browser session.\n\n" +
				"**The fused object.** `GET /traces/{id}?fused=true` returns a trace whose spans carry the log lines written under their ids (an exact join) and the metric points of their own time, and whose resources carry the series of the same service, namespace and pod (associated, not proven: a metric sample carries no trace id). Add `include=context_logs,system_logs` for the lines around the trace that carry no trace id.\n\n" +
				"**Reading many.** `GET /traces?fused=true` and `POST /traces/batch` read up to 25 traces in parallel inside a bounded share of the stores' capacity, so they do not hold up other reads; add `stream=true` to receive each trace as soon as it is ready.\n\n" +
				"**The backends' own APIs.** `/prometheus/...`, `/loki/...` and `/tempo/...` serve each backend's read endpoints exactly as it answers them (same paths after the prefix, same format), so a Grafana datasource, `promtool` or `logcli` pointed at `/api/v1/fusion/prometheus` (or `/loki`, `/tempo`) with the token works unchanged. Nothing that writes or administers a backend is served. Only an administrator, or a token with no namespace or cluster limit, may use them.\n\n" +
				"**Limits.** 600 reads a minute per credential (a bulk read of n traces counts as n), 30 seconds per request, 16 MiB per store answer."},
		"servers":  []obj{{"url": "/"}},
		"tags":     tags,
		"security": []obj{{"bearerAuth": []string{}}},
		"paths":    paths,
		"components": obj{
			"securitySchemes": obj{"bearerAuth": obj{"type": "http", "scheme": "bearer", "description": "A FUSION access token (`cnf_...`) or an administrator's personal access token."}},
			"schemas":         schemas,
			"responses": obj{
				"BadRequest":      errResp("The request is wrong; the message says how."),
				"Unauthorized":    errResp("No valid credential."),
				"Forbidden":       errResp("The credential may not read this: its signals, namespaces or clusters do not include it, or it is not an administrator."),
				"TooManyRequests": errResp("Slow down; see the Retry-After header."),
				"Unavailable":     errResp("FUSION is off, or the store is still starting."),
				"Timeout":         errResp("The stores took too long; narrow the time range or the filters."),
			},
		},
	}
}

func opID(op fusionOp) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(op.Method))
	for _, seg := range strings.FieldsFunc(op.Path, func(r rune) bool { return r == '/' || r == '{' || r == '}' || r == '_' }) {
		b.WriteString(strings.ToUpper(seg[:1]) + seg[1:])
	}
	return b.String()
}
