package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/certrenew"
)

func findProblem(r *runner, code string) *continuumv1.Problem {
	for _, p := range r.probs.list() {
		if p.Code == code {
			return p
		}
	}
	return nil
}

// A renewal that fails with weeks left is retried quietly; one the server refused, or one running out of time, is shown;
// a success takes it away.
func TestFailedCertRenewalBecomesAProblemOnlyWhenItMatters(t *testing.T) {
	r := &runner{probs: newProblemSet()}
	target := certrenew.Target{Namespace: "continuum-system", Name: "export-tls"}
	fail := func(left time.Duration, refused bool) certrenew.Result {
		return certrenew.Result{Target: target, NotAfter: time.Now().Add(left), Err: errors.New("the server did not renew it: no longer sends"), Refused: refused}
	}

	r.noteCertRenewal(fail(15*24*time.Hour, false))
	if findProblem(r, CodeCertRenewalFailing) != nil {
		t.Fatal("a transient failure with 15 days left was shown as a problem")
	}
	r.noteCertRenewal(fail(5*24*time.Hour, false))
	p := findProblem(r, CodeCertRenewalFailing)
	if p == nil || p.Severity != continuumv1.Problem_WARN || !strings.Contains(p.Message, "export-tls") || !strings.Contains(p.Message, "retried automatically") {
		t.Fatalf("5 days left: %+v", p)
	}
	r.noteCertRenewal(fail(24*time.Hour, false))
	if p = findProblem(r, CodeCertRenewalFailing); p == nil || p.Severity != continuumv1.Problem_ERROR {
		t.Fatalf("1 day left: %+v", p)
	}
	r.noteCertRenewal(certrenew.Result{Target: target, NotAfter: time.Now().Add(10 * 24 * time.Hour), Renewed: true})
	if findProblem(r, CodeCertRenewalFailing) != nil {
		t.Fatal("a successful renewal left the problem standing")
	}
	r.noteCertRenewal(fail(15*24*time.Hour, true))
	if p = findProblem(r, CodeCertRenewalFailing); p == nil || !strings.Contains(p.Message, "refused") || !strings.Contains(p.Message, "Install again") {
		t.Fatalf("a refusal must be shown at once, with what to do: %+v", p)
	}
}
