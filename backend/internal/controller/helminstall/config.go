package helminstall

import (
	"fmt"
	"log/slog"

	"helm.sh/helm/v3/pkg/action"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Config makes the Helm configuration for a namespace from a cluster connection: releases are kept as Secrets in that namespace,
// which is where `helm` itself keeps them.
func Config(rc *rest.Config, log *slog.Logger) func(namespace string) (*action.Configuration, error) {
	return func(namespace string) (*action.Configuration, error) {
		g := &getter{rc: rc, namespace: namespace}
		cfg := new(action.Configuration)
		debug := func(format string, a ...any) {
			if log != nil {
				log.Debug(fmt.Sprintf(format, a...))
			}
		}
		if err := cfg.Init(g, namespace, "secret", debug); err != nil {
			return nil, err
		}
		return cfg, nil
	}
}

// getter gives Helm its connections to the cluster from a rest.Config, with no kubeconfig file in the way.
type getter struct {
	rc        *rest.Config
	namespace string
}

func (g *getter) ToRESTConfig() (*rest.Config, error) { return rest.CopyConfig(g.rc), nil }

func (g *getter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(g.rc)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(dc), nil
}

func (g *getter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(dc), nil
}

func (g *getter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewDefaultClientConfig(*clientcmdapi.NewConfig(), &clientcmd.ConfigOverrides{Context: clientcmdapi.Context{Namespace: g.namespace}})
}
