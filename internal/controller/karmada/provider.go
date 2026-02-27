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
	_ []auth.Token,
	opts ...auth.Option,
) (*auth.RESTConfig, error) {
	var o auth.Options
	o.Apply(opts...)

	c := o.Client
	cluster := &clusterv1alpha1.Cluster{}
	if err := c.Get(ctx, client.ObjectKey{Name: o.ClusterResource}, cluster); err != nil {
		return nil, err
	}

	// ---- 优先处理 Impersonation 模式 ----
	if cluster.Spec.ImpersonatorSecretRef != nil {
		s := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{
			Namespace: cluster.Spec.ImpersonatorSecretRef.Namespace,
			Name:      cluster.Spec.ImpersonatorSecretRef.Name,
		}, s); err != nil {
			return nil, fmt.Errorf("failed to get impersonator secret: %w", err)
		}

		// 关键：添加前缀协议 "karmada-impersonate:"
		// 同时兼容 Secret Data 中的 caBundle (Karmada 常用) 和 ca.crt
		return buildKarmadaRESTConfig(cluster, s, true)
	}

	// ---- 回退到 Legacy SecretRef 模式 ----
	if cluster.Spec.SecretRef != nil {
		s := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{
			Namespace: cluster.Spec.SecretRef.Namespace,
			Name:      cluster.Spec.SecretRef.Name,
		}, s); err != nil {
			return nil, fmt.Errorf("failed to get legacy cluster secret: %w", err)
		}
		return buildKarmadaRESTConfig(cluster, s, false)
	}

	return nil, fmt.Errorf("cluster %s has no valid credentials", cluster.Name)
}

func buildKarmadaRESTConfig(cluster *clusterv1alpha1.Cluster, secret *corev1.Secret, isImpersonation bool) (*auth.RESTConfig, error) {
	token := string(secret.Data["token"])
	if token == "" {
		return nil, fmt.Errorf("token not found in secret %s/%s", secret.Namespace, secret.Name)
	}

	caData := secret.Data["ca.crt"]
	if len(caData) == 0 {
		caData = secret.Data["caBundle"] // 适配你 YAML 中的 caBundle 字段
	}

	conf := &auth.RESTConfig{
		Host:      cluster.Spec.APIEndpoint,
		CAData:    caData,
		ExpiresAt: time.Time{},
	}

	if isImpersonation {
		conf.BearerToken = "karmada-impersonate:" + token
	} else {
		conf.BearerToken = token
	}

	return conf, nil
}
