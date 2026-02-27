package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	intcontroller "github.com/fluxcd/helm-controller/internal/controller"
	"github.com/fluxcd/pkg/apis/meta"
	clusterv1alpha1 "github.com/karmada-io/karmada/pkg/apis/cluster/v1alpha1"
)

// kubectl --kubeconfig ~/.karmada/karmada-apiserver.config create configmap karmada-provider-tk-dev-02 -n karmada-system --from-literal=cluster=tk-dev-02 --from-literal=provider=karmada
// go run main_demo.go --karmada-kubeconfig ~/.karmada/karmada-apiserver.config --cluster tk-dev-02
func main() {
	var kubeconfig string
	var clusterName string

	flag.StringVar(&kubeconfig, "karmada-kubeconfig", "", "Path to karmada kubeconfig")
	flag.StringVar(&clusterName, "cluster", "", "Karmada member cluster name")
	flag.Parse()

	if kubeconfig == "" || clusterName == "" {
		log.Fatalf("usage: demo --karmada-kubeconfig <path> --cluster <name>")
	}

	ctx := context.Background()
	// setupLog = ctrl.Log.WithName("setup")

	// 1️⃣ 构建 Karmada REST config
	karmadaRestConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("failed to build karmada rest config: %v", err)
	}

	// 2️⃣ 初始化 Scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = clusterv1alpha1.Install(scheme)

	// 3️⃣ 创建 controller-runtime client
	ctrlClient, err := ctrlclient.New(karmadaRestConfig, ctrlclient.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.Fatalf("failed to create controller-runtime client: %v", err)
	}

	// 6️⃣ 构建 wrapped rest.Config
	restConfig, err := intcontroller.GetRESTConfig(ctx, meta.KubeConfigReference{
		ConfigMapRef: &meta.LocalObjectReference{
			Name: "karmada-provider-" + clusterName,
		},
	}, "karmada-system", ctrlClient)
	if err != nil {
		log.Fatalf("failed to get rest config: %v", err)
	}

	// 7️⃣ 创建 member cluster clientset
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("failed to create clientset: %v", err)
	}

	// 8️⃣ 查询 Node
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Fatalf("failed to list nodes: %v", err)
	}

	fmt.Printf("Nodes in cluster %s:\n", clusterName)
	for _, node := range nodes.Items {
		fmt.Println(" -", node.Name)
	}

	// 🔹 创建 Namespace（如果不存在）
	ns := "demo"
	_, _ = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
		},
	}, metav1.CreateOptions{})

	// 🔹 创建 Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo-pod",
			Namespace: ns,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: "swr.cn-north-4.myhuaweicloud.com/ddn-k8s/ghcr.io/nginx/nginx-gateway-fabric/nginx:2.0.1",
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	fmt.Println("\nCreating Pod...")
	_, err = clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		log.Fatalf("failed to create pod: %v", err)
	}

	// 🔹 每秒查询 Pod 状态
	fmt.Println("Watching Pod status...")
	for {
		time.Sleep(1 * time.Second)

		p, err := clientset.CoreV1().Pods(ns).Get(ctx, "demo-pod", metav1.GetOptions{})
		if err != nil {
			log.Fatalf("failed to get pod: %v", err)
		}

		fmt.Printf("Current Phase:%v , Node %v", p.Status.Phase, p.Spec.NodeName)

		if p.Status.Phase == corev1.PodRunning ||
			p.Status.Phase == corev1.PodFailed ||
			p.Status.Phase == corev1.PodSucceeded {
			fmt.Println("Pod reached terminal or running state.")
			break
		}
	}
}
