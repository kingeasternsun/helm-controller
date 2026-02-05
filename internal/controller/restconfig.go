// This file is a copy of https://github.com/fluxcd/pkg/blob/main/auth/utils/restconfig.go
// and replace provider with karmada.Provider.
package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/auth"
	"github.com/fluxcd/pkg/auth/generic"
)

func GetRESTConfig(ctx context.Context,
	kubeConfigRef meta.KubeConfigReference,
	namespace string, ctrlClient client.Client,
	opts ...auth.Option) (*rest.Config, error) {

	// Get ConfigMap.
	cmKey := client.ObjectKey{
		Name:      kubeConfigRef.ConfigMapRef.Name,
		Namespace: namespace,
	}
	var cm corev1.ConfigMap
	if err := ctrlClient.Get(ctx, cmKey, &cm); err != nil {
		return nil, fmt.Errorf("failed to get configmap %s: %w", cmKey.String(), err)
	}

	// TODO: Repace this with karmada provider
	provider := generic.Provider{}

	// Configure options.
	if c, ok := cm.Data[meta.KubeConfigKeyCluster]; ok {
		opts = append(opts, auth.WithClusterResource(c))
	}
	if a, ok := cm.Data[meta.KubeConfigKeyAddress]; ok {
		opts = append(opts, auth.WithClusterAddress(a))
	}
	if ca, ok := cm.Data[meta.KubeConfigKeyCACert]; ok {
		opts = append(opts, auth.WithCAData(ca))
	}
	opts = append(opts, auth.WithClient(ctrlClient))
	opts = append(opts, auth.WithServiceAccountNamespace(namespace))
	if name, ok := cm.Data[meta.KubeConfigKeyServiceAccountName]; ok {
		opts = append(opts, auth.WithServiceAccountName(name))
	}
	if a, ok := cm.Data[meta.KubeConfigKeyAudiences]; ok {
		var audiences []string
		for aud := range strings.SplitSeq(a, "\n") {
			aud = strings.TrimSpace(aud)
			if aud == "" {
				continue
			}
			audiences = append(audiences, aud)
		}
		opts = append(opts, auth.WithAudiences(audiences...))
	}

	conf, err := auth.GetRESTConfig(ctx, provider, opts...)
	if err != nil {
		return nil, err
	}

	// Build wrapped *rest.Config that will call
	// auth.GetRESTConfig for every HTTP request.
	restConfig := &rest.Config{
		Host:            conf.Host,
		TLSClientConfig: rest.TLSClientConfig{CAData: conf.CAData},
	}
	restConfig.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return &restConfigRoundTripper{
			base:     base,
			provider: provider,
			opts:     opts,
		}
	})

	return restConfig, nil
}

// restConfigRoundTripper is an http.RoundTripper that wraps the base
// RoundTripper and retrieves a bearer token for the remote cluster
// using auth.GetRESTConfig before each HTTP request.
type restConfigRoundTripper struct {
	base     http.RoundTripper
	provider auth.RESTConfigProvider
	opts     []auth.Option
}

// RoundTrip implements http.RoundTripper.
func (r *restConfigRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	details, err := auth.GetRESTConfig(req.Context(), r.provider, r.opts...)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+details.BearerToken)
	return r.base.RoundTrip(req)
}
