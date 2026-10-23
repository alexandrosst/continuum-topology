package purge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func secret(ns, name string, labels map[string]string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels, UID: types.UID("uid-1")}, Data: map[string][]byte{"key": []byte("k")}}
}

func exists(t *testing.T, c *fake.Clientset, ns, name string) bool {
	t.Helper()
	_, err := c.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err == nil {
		return true
	}
	if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return false
}

func TestTheIdentitySecretOfTheChartIsDeleted(t *testing.T) {
	c := fake.NewSimpleClientset(secret("ikhnos-system", "ikhnos-agent-identity", map[string]string{PartOfLabel: PartOf}))
	if err := Secret(context.Background(), c, "ikhnos-system", "ikhnos-agent-identity", quiet); err != nil {
		t.Fatal(err)
	}
	if exists(t, c, "ikhnos-system", "ikhnos-agent-identity") {
		t.Fatal("the Secret is still there")
	}
}

func TestASecretThatIsAlreadyGoneIsSuccessSoTheHookCanRunAgain(t *testing.T) {
	c := fake.NewSimpleClientset()
	if err := Secret(context.Background(), c, "ikhnos-system", "ikhnos-agent-identity", quiet); err != nil {
		t.Fatalf("a missing Secret is a failure: %v", err)
	}
}

func TestASecretThatIsNotTheCharsIsLeftAlone(t *testing.T) {
	for name, labels := range map[string]map[string]string{"no labels": nil, "another owner": {PartOfLabel: "something-else"}} {
		c := fake.NewSimpleClientset(secret("ns", "tls", labels))
		err := Secret(context.Background(), c, "ns", "tls", quiet)
		if !errors.Is(err, ErrNotOurs) {
			t.Errorf("%s: got %v, want ErrNotOurs", name, err)
		}
		if !exists(t, c, "ns", "tls") {
			t.Errorf("%s: a Secret that is not ours was deleted", name)
		}
	}
}

func TestTheDeleteIsConditionalOnWhatWasRead(t *testing.T) {
	c := fake.NewSimpleClientset(secret("ns", "id", map[string]string{PartOfLabel: PartOf}))
	var seen *metav1.Preconditions
	c.PrependReactor("delete", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		seen = a.(ktesting.DeleteActionImpl).DeleteOptions.Preconditions
		return false, nil, nil
	})
	if err := Secret(context.Background(), c, "ns", "id", quiet); err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.UID == nil || *seen.UID != "uid-1" {
		t.Fatalf("the delete did not carry the UID that was read: %+v", seen)
	}
}

func TestAFailureToDeleteIsReportedSoUninstallDoesNotClaimSuccess(t *testing.T) {
	c := fake.NewSimpleClientset(secret("ns", "id", map[string]string{PartOfLabel: PartOf}))
	c.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("secrets"), "id", errors.New("no"))
	})
	if err := Secret(context.Background(), c, "ns", "id", quiet); err == nil {
		t.Fatal("a refused delete was reported as success")
	}
}
