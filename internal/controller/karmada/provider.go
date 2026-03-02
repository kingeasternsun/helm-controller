package karmada

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fluxcd/pkg/auth"
	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
)

const ProviderName = "karmada"

type Provider struct{}

func (Provider) GetName() string {
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
	ctrl.LoggerFrom(ctx).Info("NewRESTConfig", "o.ClusterResource", o.ClusterResource)

	c := o.Client
	cluster := &clusterv1alpha1.Cluster{}
	if err := c.Get(ctx, client.ObjectKey{Name: o.ClusterResource}, cluster); err != nil {
		return nil, err
	}

	// ---- 优先处理  Legacy SecretRef 模式 ----
	if cluster.Spec.SecretRef != nil {
		klog.Info("SecretRef")
		ctrl.LoggerFrom(ctx).Info("Use SecretRef to get cluster secret")
		s := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{
			Namespace: cluster.Spec.SecretRef.Namespace,
			Name:      cluster.Spec.SecretRef.Name,
		}, s); err != nil {
			return nil, fmt.Errorf("failed to get legacy cluster secret: %w", err)
		}
		return buildKarmadaRESTConfig(cluster, s, false)
	}

	// ---- 回退到  Impersonation 模式 ----
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

// NewControllerToken 返回一个 Token，用于与云供应商（此处为 Karmada）进行初始认证。
// 对于 Karmada 来说，我们通常不需要预取一个全局 Controller Token，
// 因为所有的访问凭证都是从具体集群（Cluster）关联的 Secret 中获取的。
func (p Provider) NewControllerToken(ctx context.Context, opts ...auth.Option) (auth.Token, error) {
	// 1. 应用 Options 获取上下文
	var o auth.Options
	o.Apply(opts...)

	// 2. 对于 Karmada Provider，我们返回一个满足 auth.Token 接口的基础结构。
	// 如果 Karmada 集群本身需要某种初始凭证（例如访问外部 KMS），可以在这里实现。
	// 在标准 Karmada 架构下，我们直接返回一个永不过期的静态占位 Token。

	return &auth.RESTConfig{
		// 这里可以保留为空，因为真正的 Host 和 Token
		// 会在后续的 NewRESTConfig 阶段根据具体的 ClusterResource 重新构造。
		ExpiresAt: time.Time{},
	}, nil
}

// GetAudiences 返回 ServiceAccount 换取 Token 时需要的受众标识。
// 在 Karmada 场景下，除非你结合了外部 OIDC/IAM，否则通常返回空或默认值。
func (p Provider) GetAudiences(ctx context.Context, sa corev1.ServiceAccount) ([]string, error) {
	// Karmada 认证目前不强制要求特定的 STS Audience
	return []string{}, nil
}

// GetIdentity 从 ServiceAccount 的注解中提取身份标识。
// 例如：karmada.io/identity: "custom-user"
func (p Provider) GetIdentity(sa corev1.ServiceAccount) (string, error) {
	if identity, ok := sa.Annotations["karmada.io/identity"]; ok {
		return identity, nil
	}
	return "", nil
}

// NewTokenForServiceAccount 将 K8s OIDC Token 换取为 Provider Token。
// 在 Karmada 中，我们通常不需要这种“交换”逻辑。
func (p Provider) NewTokenForServiceAccount(
	ctx context.Context,
	oidcToken string,
	sa corev1.ServiceAccount,
	opts ...auth.Option,
) (auth.Token, error) {
	// 直接复用 Controller Token 逻辑，或者根据需要封装 oidcToken。
	// 绝大多数情况下，Karmada 的认证直接基于 Secret。
	return p.NewControllerToken(ctx, opts...)
}
