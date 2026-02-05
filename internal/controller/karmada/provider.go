package karmada

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fluxcd/pkg/auth"
	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
)

const ProviderName = "karmada"

type Provider struct{}

func (Provider) Name() string {
	return ProviderName
}

// 设计说明
// Karmada 不需要动态换 token
// Cluster Secret 是“长期凭证”
// 所以：返回一个空 option slice
func (Provider) GetAccessTokenOptionsForCluster(
	opts ...auth.Option,
) ([][]auth.Option, error) {

	// 单一、静态“token”，不需要额外 option
	return [][]auth.Option{
		{},
	}, nil
}

// 这是 真正和 Karmada API 打交道的地方。
// 行为目标
// 从 opts 里拿到：
//   clusterName
//   ctrlClient
// 用 ctrlClient 访问 Karmada API Server
// 查：
//   Cluster
// 	 Cluster.Spec.SecretRef
//   对应 Secret
// 构造 *auth.RESTConfig

func (Provider) NewRESTConfig(
	ctx context.Context,
	_ []auth.Token, // karmada provider 不使用 accessTokens
	opts ...auth.Option,
) (*auth.RESTConfig, error) {

	var o auth.Options
	o.Apply(opts...)

	// ---- 基础校验 ----
	if o.ClusterResource == "" {
		return nil, fmt.Errorf("cluster is required for karmada provider")
	}
	if o.Client == nil {
		return nil, fmt.Errorf("controller-runtime client is required")
	}

	c := o.Client

	// ---- 1. 查询 Karmada Cluster ----
	cluster := &clusterv1alpha1.Cluster{}
	if err := c.Get(ctx,
		client.ObjectKey{Name: o.ClusterResource},
		cluster,
	); err != nil {
		return nil, fmt.Errorf("failed to get karmada Cluster %q: %w",
			o.ClusterResource, err)
	}

	// ---- 2. 获取 Secret（兼容 secretRef 模式）----
	var secret *corev1.Secret
	if cluster.Spec.SecretRef != nil {
		s := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{
			Namespace: cluster.Spec.SecretRef.Namespace,
			Name:      cluster.Spec.SecretRef.Name,
		}, s); err != nil {
			return nil, fmt.Errorf("failed to get cluster secret %s/%s: %w",
				cluster.Spec.SecretRef.Namespace,
				cluster.Spec.SecretRef.Name,
				err)
		}
		secret = s
	}

	if secret == nil {
		return nil, fmt.Errorf("cluster %s has neither ClusterCredential nor SecretRef",
			cluster.Name)
	}

	// ---- 4. 构造 member cluster 的 RESTConfig ----
	return buildRESTConfig(cluster, secret)
}

func buildRESTConfig(
	cluster *clusterv1alpha1.Cluster,
	secret *corev1.Secret,
) (*auth.RESTConfig, error) {

	server := cluster.Spec.APIEndpoint
	if server == "" {
		return nil, fmt.Errorf("cluster %s has empty apiEndpoint", cluster.Name)
	}

	token, ok := secret.Data["token"]
	if !ok || len(token) == 0 {
		return nil, fmt.Errorf(
			"secret %s/%s does not contain token",
			secret.Namespace,
			secret.Name,
		)
	}

	conf := &auth.RESTConfig{
		Host:        cluster.Spec.APIEndpoint,
		BearerToken: string(token),
		CAData:      secret.Data["ca.crt"],
		// 不设置 ExpiresAt → Flux 会认为是 long-lived token
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	return conf, nil
}
