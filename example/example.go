/*
 * @Author: jinde.zgm
 * @Date: 2020-12-04 23:38:23
 * @Descripttion:
 */
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jindezgm/konfig"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type logConfig struct {
	LogLevel     int  `json:"logLevel"`
	AlsoToStderr bool `json:"alsoToStderr"`
}

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.TODO())
	defer cancel()
	kfg, err := konfig.NewWithClientset(ctx, clientset, "test", "test-sa")
	kfg.RegValue(&logConfig{}, "json")
	kfg.MountEnv()
	for {
		value, _ := kfg.Get()
		lc := value.(*logConfig)
		if lc.AlsoToStderr {
			logLevel, _ := kfg.GetInt64("logLevel")
			fmt.Println("Hello world", lc.LogLevel, logLevel, os.Getenv("logLevel"))
		}
		time.Sleep(time.Second)
	}
}
