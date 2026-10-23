// Package purge is the "ikhnos purge-identity" role: it deletes the Secret that holds an agent's identity (its key and its
// certificate). The agent chart runs it from a post-delete hook when the install asked for it (identity.purgeOnUninstall), so
// that one `helm uninstall` leaves nothing of the agent behind in the cluster. Without it the Secret stays on purpose (it carries
// helm.sh/resource-policy: keep), so an uninstall by mistake cannot cost an agent its identity.
package purge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"ikhnos/internal/cli"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// PartOfLabel and PartOf are what every object of the chart carries; a Secret without them is not the chart's and is left alone.
const (
	PartOfLabel = "app.kubernetes.io/part-of"
	PartOf      = "ikhnos"
)

// ErrNotOurs is returned for a Secret that exists but is not one of this chart's.
var ErrNotOurs = errors.New("the Secret is not labelled as part of Ikhnos, so it is not deleted")

// Secret deletes the named Secret, once, and says what it did. A Secret that is already gone is success (the hook may run
// again). The delete is conditional on the UID that was read, so a Secret that was replaced in between is not the one removed.
func Secret(ctx context.Context, client kubernetes.Interface, namespace, name string, log *slog.Logger) error {
	sec, err := client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		log.Info("the identity Secret is already gone", "namespace", namespace, "secret", name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the Secret: %w", err)
	}
	if sec.Labels[PartOfLabel] != PartOf {
		return fmt.Errorf("%s/%s: %w", namespace, name, ErrNotOurs)
	}
	uid := sec.UID
	opts := metav1.DeleteOptions{}
	if uid != "" {
		opts.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	err = client.CoreV1().Secrets(namespace).Delete(ctx, name, opts)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete the Secret: %w", err)
	}
	log.Info("deleted the identity Secret", "namespace", namespace, "secret", name)
	return nil
}

// Main returns the process exit code.
func Main(args []string) int {
	fs := cli.NewFlagSet("ikhnos purge-identity", os.Stderr)
	ns := fs.String("namespace", cli.Env("POD_NAMESPACE", ""), "the namespace of the Secret (the pod's own)")
	name := fs.String("secret", cli.Env("IKHNOS_IDENTITY_SECRET", ""), "the Secret that holds the agent's identity")
	timeout := fs.Duration("timeout", 30*time.Second, "give up after this long")
	kubeconfig := fs.String("kubeconfig", "", "development only: use this kubeconfig instead of the in-cluster service account")
	if code, done := cli.Parse(fs, args); done {
		return code
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *ns == "" || *name == "" {
		return cli.Fatal(log, fmt.Errorf("--namespace and --secret are required (the chart sets POD_NAMESPACE and IKHNOS_IDENTITY_SECRET)"))
	}
	var rc *rest.Config
	var err error
	if *kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return cli.Fatal(log, err)
	}
	rc.UserAgent = "ikhnos-purge-identity/" + cli.Version
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return cli.Fatal(log, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := Secret(ctx, client, *ns, *name, log); err != nil {
		return cli.Fatal(log, err)
	}
	return 0
}
