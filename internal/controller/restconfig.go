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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	intkarmada "github.com/fluxcd/helm-controller/internal/controller/karmada"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/auth"
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

	ctrl.LoggerFrom(ctx).Info("GetRESTConfig", "cm", cm.Data, "namespace", namespace, "name", kubeConfigRef.ConfigMapRef.Name)

	// use karmada provider
	provider := intkarmada.Provider{}

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
	// if len(conf.CAData) == 0 {
	// 	restConfig.TLSClientConfig.Insecure = true
	// }
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
// RoundTrip implements http.RoundTripper.
func (r *restConfigRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// 调用上面定义的 NewRESTConfig (通过 auth.GetRESTConfig 间接调用)
	details, err := auth.GetRESTConfig(req.Context(), r.provider, r.opts...)
	if err != nil {
		return nil, err
	}

	token := details.BearerToken

	// ---- 解析特殊协议前缀 ----
	if strings.HasPrefix(token, "karmada-impersonate:") {
		realToken := strings.TrimPrefix(token, "karmada-impersonate:")

		// 1. 设置认证 Token (来自 Impersonator Secret)
		req.Header.Set("Authorization", "Bearer "+realToken)

		// 2. 设置冒充 Header (Karmada 核心逻辑)
		// 这是让 member 集群 API Server 识别调用的关键
		req.Header.Set("Impersonate-User", "system:serviceaccount:karmada-system:karmada-controller")

		// 可选：根据集群安全配置，可能还需要设置 Group
		// req.Header.Set("Impersonate-Group", "system:serviceaccounts:karmada-system")
	} else {
		// ---- 普通模式：直接使用 Token ----
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return r.base.RoundTrip(req)
}
